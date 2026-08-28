package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/spf13/cobra"

	api "github.com/amagioss/opentams/gen/api"
	"github.com/amagioss/opentams/tools/internal/opentamsclient"
)

const (
	playlistPollInterval = 500 * time.Millisecond
	urlPoolRefillRatio   = 4 // refill when remaining ≤ batch/refill_ratio
)

func newLiveCmd() *cobra.Command {
	c := &commonFlags{}
	var input string
	var batch int
	cmd := &cobra.Command{
		Use:   "live",
		Short: "Continuously chunk + register a live input. SIGINT to stop.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if input == "" {
				return fmt.Errorf("--input is required")
			}
			resolveIDs(c)
			start, err := startNs(c)
			if err != nil {
				return err
			}
			workDir, err := os.MkdirTemp("", "opentamspub-live-*")
			if err != nil {
				return err
			}
			// Keep the chunk files around after exit so the operator can
			// re-play / inspect; live demos are small enough to not worry.
			logf("live workdir: %s (kept on exit)", workDir)
			return runLiveMode(cmd.Context(), input, workDir, start, batch, c)
		},
	}
	addCommonFlags(cmd, c)
	cmd.Flags().StringVar(&input, "input", "", "ffmpeg input (file, rtmp://, /dev/video0, etc.)")
	cmd.Flags().IntVar(&batch, "storage-batch", defaultStorageBatch, "Presigned-URL pool size; refilled when low")
	return cmd
}

// urlPool is a tiny consumer/producer queue of presigned URLs. The
// consumer (chunk registrar) takes one URL per new chunk; the producer
// (refiller) tops the pool up when it drops below 1/urlPoolRefillRatio.
type urlPool struct {
	mu      sync.Mutex
	cond    *sync.Cond
	urls    []opentamsclient.AllocatedObject
	closed  bool
	target  int
	flowID  string
	client  *opentamsclient.Client
	context context.Context
}

func newURLPool(ctx context.Context, client *opentamsclient.Client, flowID string, target int) *urlPool {
	p := &urlPool{target: target, flowID: flowID, client: client, context: ctx}
	p.cond = sync.NewCond(&p.mu)
	return p
}

func (p *urlPool) refillLoop() {
	for {
		p.mu.Lock()
		for !p.closed && len(p.urls) > p.target/urlPoolRefillRatio {
			p.cond.Wait()
		}
		if p.closed {
			p.mu.Unlock()
			return
		}
		need := p.target - len(p.urls)
		p.mu.Unlock()
		if need <= 0 {
			continue
		}
		batch, err := p.client.AllocateStorage(p.context, p.flowID, need)
		if err != nil {
			if errors.Is(p.context.Err(), context.Canceled) {
				return
			}
			logf("warning: refill allocate failed (%d): %v — retrying", need, err)
			select {
			case <-p.context.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		p.mu.Lock()
		p.urls = append(p.urls, batch...)
		p.cond.Broadcast()
		p.mu.Unlock()
	}
}

func (p *urlPool) take(ctx context.Context) (opentamsclient.AllocatedObject, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for len(p.urls) == 0 {
		if ctx.Err() != nil {
			return opentamsclient.AllocatedObject{}, ctx.Err()
		}
		if p.closed {
			return opentamsclient.AllocatedObject{}, errors.New("url pool closed")
		}
		// cond.Wait releases the mutex; we re-acquire on wakeup. The
		// refiller broadcasts when it has appended new URLs.
		p.cond.Wait()
	}
	u := p.urls[0]
	p.urls = p.urls[1:]
	p.cond.Broadcast()
	return u, nil
}

func (p *urlPool) close() {
	p.mu.Lock()
	p.closed = true
	p.cond.Broadcast()
	p.mu.Unlock()
}

func runLiveMode(ctx context.Context, input, workDir string, startTAINs int64, batch int, c *commonFlags) error {
	if batch < 1 {
		batch = defaultStorageBatch
	}
	if batch > maxStorageBatch {
		batch = maxStorageBatch
	}
	logf("flow_id   = %s", c.FlowID)
	logf("source_id = %s", c.SourceID)

	client := newClient(c)
	// Sources are created implicitly by PUT /flows/{id} (metastore.UpsertFlow);
	// there is no separate /sources POST endpoint to call first.
	if err := client.PutFlow(ctx, c.FlowID, videoFlowBody(c)); err != nil {
		return fmt.Errorf("create flow: %w", err)
	}

	pool := newURLPool(ctx, client, c.FlowID, batch)
	go pool.refillLoop()
	defer pool.close()

	fmt.Println(c.FlowID)

	// Run ffmpeg in a separate goroutine; the watcher runs in the
	// foreground. When ffmpeg exits (or ctx is cancelled), the watcher
	// stops once it has drained any remaining chunks.
	ffmpegErr := make(chan error, 1)
	go func() { ffmpegErr <- runFfmpeg(ctx, input, c, workDir) }()

	if err := watchAndRegister(ctx, workDir, c.FlowID, startTAINs, pool, client); err != nil {
		return err
	}
	if ferr := <-ffmpegErr; ferr != nil && !errors.Is(ferr, context.Canceled) {
		return ferr
	}
	return nil
}

func watchAndRegister(ctx context.Context, workDir, flowID string, startTAINs int64, pool *urlPool, client *opentamsclient.Client) error {
	playlist := filepath.Join(workDir, "out.m3u8")
	seen := 0
	ticker := time.NewTicker(playlistPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		entries, done, err := readPlaylist(playlist)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			logf("playlist read error: %v", err)
			continue
		}
		// Process anything that's been added since we last looked. We
		// rely on the playlist only containing closed chunks — ffmpeg
		// only appends an entry once a segment file is flushed.
		for i := seen; i < len(entries); i++ {
			if err := registerLiveChunk(ctx, client, flowID, entries[i], pool, startTAINs); err != nil {
				return err
			}
			seen = i + 1
		}
		if done {
			return nil
		}
	}
}

func registerLiveChunk(ctx context.Context, client *opentamsclient.Client, flowID string, e hlsEntry, pool *urlPool, startTAINs int64) error {
	obj, err := pool.take(ctx)
	if err != nil {
		return err
	}
	f, err := os.Open(e.File) //nolint:gosec // demo tool: ffmpeg-controlled path
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if err := client.UploadObject(ctx, obj, f, info.Size()); err != nil {
		return fmt.Errorf("upload %s: %w", filepath.Base(e.File), err)
	}
	startNs := startTAINs + e.StartOffset.Nanoseconds()
	endNs := startNs + e.Duration.Nanoseconds()
	seg := []api.FlowSegmentPost{{
		ObjectId:  obj.ObjectID,
		Timerange: opentamsclient.HalfOpenRange(startNs, endNs),
	}}
	if err := client.RegisterSegments(ctx, flowID, seg, opentamsclient.NewIdempotencyKey()); err != nil {
		return fmt.Errorf("register %s: %w", filepath.Base(e.File), err)
	}
	logf("registered %s @ %s (%.3fs)", filepath.Base(e.File), opentamsclient.HalfOpenRange(startNs, endNs), e.Duration.Seconds())
	return nil
}
