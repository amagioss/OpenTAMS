package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	api "github.com/amagioss/opentams/gen/api"
	"github.com/amagioss/opentams/tools/internal/opentamsclient"
)

const fileUploadConcurrency = 8

func newFileCmd() *cobra.Command {
	c := &commonFlags{}
	var outDir string
	cmd := &cobra.Command{
		Use:   "file <input>",
		Short: "Chunk a file and bulk-publish its segments to OpenTAMS.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolveIDs(c)
			start, err := startNs(c)
			if err != nil {
				return err
			}
			workDir := outDir
			if workDir == "" {
				tmp, err := os.MkdirTemp("", "opentamspub-*")
				if err != nil {
					return err
				}
				workDir = tmp
				defer func() { _ = os.RemoveAll(workDir) }()
			} else if err := os.MkdirAll(workDir, 0o750); err != nil {
				return err
			}
			if c.Copy {
				if err := applyCopyModeFlowParams(cmd.Context(), args[0], c); err != nil {
					return err
				}
			}
			return runFileMode(cmd.Context(), args[0], workDir, start, c)
		},
	}
	addCommonFlags(cmd, c)
	cmd.Flags().StringVar(&outDir, "out-dir", "", "Persistent ffmpeg output dir (default: ephemeral tempdir)")
	cmd.Flags().BoolVar(&c.Copy, "copy", false, "Pass input streams through with -c copy (no transcode)")
	return cmd
}

func addCommonFlags(cmd *cobra.Command, c *commonFlags) {
	cmd.Flags().StringVar(&c.BaseURL, "base-url", "http://localhost:8080", "OpenTAMS base URL (env OPENTAMS_BASE_URL overrides)")
	cmd.Flags().StringVar(&c.Token, "token", "dev", "Bearer token (env OPENTAMS_TOKEN overrides)")
	cmd.Flags().StringVar(&c.FlowID, "flow", "", "Flow ID (default: new UUIDv4)")
	cmd.Flags().StringVar(&c.SourceID, "source", "", "Source ID (default: new UUIDv4)")
	cmd.Flags().StringVar(&c.Label, "label", "", "Flow label (default: opentamspub-<flow-prefix>)")
	cmd.Flags().DurationVar(&c.ChunkDur, "chunk", defaultChunkDur, "Target chunk duration")
	cmd.Flags().IntVar(&c.Width, "width", defaultWidth, "Output frame width")
	cmd.Flags().IntVar(&c.Height, "height", defaultHeight, "Output frame height")
	cmd.Flags().IntVar(&c.FPS, "fps", defaultFPS, "Output frame rate")
	cmd.Flags().StringVar(&c.StartTS, "start", "", "Reference clock origin (RFC3339); default: now")
}

func runFileMode(ctx context.Context, input, workDir string, startTAINs int64, c *commonFlags) error {
	logf("flow_id   = %s", c.FlowID)
	logf("source_id = %s", c.SourceID)
	logf("workdir   = %s", workDir)

	if err := runFfmpeg(ctx, input, c, workDir); err != nil {
		return err
	}
	entries, _, err := readPlaylist(filepath.Join(workDir, "out.m3u8"))
	if err != nil {
		return fmt.Errorf("read playlist: %w", err)
	}
	if len(entries) == 0 {
		return fmt.Errorf("ffmpeg produced no chunks")
	}
	logf("chunked into %d segment(s)", len(entries))

	client := newClient(c)
	// Sources are created implicitly by PUT /flows/{id} (metastore.UpsertFlow);
	// there is no separate /sources POST endpoint to call first.
	if err := client.PutFlow(ctx, c.FlowID, videoFlowBody(c)); err != nil {
		return fmt.Errorf("create flow: %w", err)
	}

	objects, err := allocateAll(ctx, client, c.FlowID, len(entries))
	if err != nil {
		return err
	}
	if len(objects) != len(entries) {
		return fmt.Errorf("allocated %d slots for %d chunks", len(objects), len(entries))
	}

	if err := uploadAll(ctx, client, entries, objects); err != nil {
		return fmt.Errorf("upload chunks: %w", err)
	}

	segments := buildSegments(entries, objects, startTAINs)
	if err := client.RegisterSegments(ctx, c.FlowID, segments, opentamsclient.NewIdempotencyKey()); err != nil {
		return fmt.Errorf("register segments: %w", err)
	}
	logf("registered %d segment(s) on flow %s", len(segments), c.FlowID)
	logf("chunk boundaries (use these to pick aligned --replace ranges):")
	for _, seg := range segments {
		logf("  %s", seg.Timerange)
	}

	// Print the flow ID on stdout so demo.sh can capture it.
	fmt.Println(c.FlowID)
	return nil
}

// allocateAll asks for `n` presigned URLs in one bulk request, looping
// only if the server caps a single request below the requested count.
func allocateAll(ctx context.Context, client *opentamsclient.Client, flowID string, n int) ([]opentamsclient.AllocatedObject, error) {
	out := make([]opentamsclient.AllocatedObject, 0, n)
	remaining := n
	for remaining > 0 {
		req := min(remaining, maxStorageBatch)
		batch, err := client.AllocateStorage(ctx, flowID, req)
		if err != nil {
			return nil, fmt.Errorf("allocate storage (req=%d): %w", req, err)
		}
		if len(batch) == 0 {
			return nil, fmt.Errorf("server allocated 0 of %d requested objects", req)
		}
		out = append(out, batch...)
		remaining -= len(batch)
	}
	return out, nil
}

func uploadAll(ctx context.Context, client *opentamsclient.Client, entries []hlsEntry, objects []opentamsclient.AllocatedObject) error {
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(fileUploadConcurrency)
	for i := range entries {
		g.Go(func() error {
			return uploadChunk(gctx, client, entries[i].File, objects[i])
		})
	}
	return g.Wait()
}

func uploadChunk(ctx context.Context, client *opentamsclient.Client, path string, obj opentamsclient.AllocatedObject) error {
	f, err := os.Open(path) //nolint:gosec // demo tool: path is operator-controlled
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	return client.UploadObject(ctx, obj, f, info.Size())
}

func buildSegments(entries []hlsEntry, objects []opentamsclient.AllocatedObject, startTAINs int64) []api.FlowSegmentPost {
	out := make([]api.FlowSegmentPost, 0, len(entries))
	for i, e := range entries {
		startNs := startTAINs + e.StartOffset.Nanoseconds()
		endNs := startNs + e.Duration.Nanoseconds()
		out = append(out, api.FlowSegmentPost{
			ObjectId:  objects[i].ObjectID,
			Timerange: opentamsclient.HalfOpenRange(startNs, endNs),
		})
	}
	return out
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}
