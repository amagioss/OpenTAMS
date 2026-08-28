package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	api "github.com/amagioss/opentams/gen/api"
	"github.com/amagioss/opentams/tools/internal/opentamsclient"
)

type forkOpts struct {
	BaseURL   string
	Token     string
	From      string
	To        string
	Replace   string
	SlateFile string
	SlateText string
	OutDir    string
}

func newForkCmd() *cobra.Command {
	o := &forkOpts{}
	cmd := &cobra.Command{
		Use:   "fork",
		Short: "Create a new Flow that mirrors --from but replaces a timerange with a break slate.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if o.From == "" || o.Replace == "" {
				return errors.New("--from and --replace are required")
			}
			if o.To == "" {
				o.To = uuid.NewString()
			}
			if o.SlateFile == "" && o.SlateText == "" {
				o.SlateText = "BREAK"
			}
			return runFork(cmd.Context(), o)
		},
	}
	cmd.Flags().StringVar(&o.BaseURL, "base-url", "http://localhost:8080", "OpenTAMS base URL (env OPENTAMS_BASE_URL overrides)")
	cmd.Flags().StringVar(&o.Token, "token", "dev", "Bearer token (env OPENTAMS_TOKEN overrides)")
	cmd.Flags().StringVar(&o.From, "from", "", "Parent Flow ID (read-only)")
	cmd.Flags().StringVar(&o.To, "to", "", "New Flow ID (default: new UUIDv4)")
	cmd.Flags().StringVar(&o.Replace, "replace", "", "Timerange to overlay with the slate, e.g. [12:0_18:0)")
	cmd.Flags().StringVar(&o.SlateFile, "slate", "", "Path to an existing slate clip to re-encode (overrides --slate-text)")
	cmd.Flags().StringVar(&o.SlateText, "slate-text", "", "Text drawn over a black slate (default: BREAK)")
	cmd.Flags().StringVar(&o.OutDir, "out-dir", "", "Persistent slate output dir (default: ephemeral tempdir)")
	return cmd
}

// parentFlowParams is the subset of essence params the slate generator
// needs to produce a TS chunk identical to the parent flow.
type parentFlowParams struct {
	SourceID   string
	Format     string
	Codec      string
	Container  string
	Label      string
	Width      int
	Height     int
	FrameRate  int // numerator; denominator assumed = 1 for the demo
	ChunkSecs  int // segment_duration numerator, or 6 if absent
	OriginFlow string
}

func runFork(ctx context.Context, o *forkOpts) error {
	client := opentamsclient.New(
		envOr("OPENTAMS_BASE_URL", o.BaseURL),
		envOr("OPENTAMS_TOKEN", o.Token),
	)

	replaceStart, replaceEnd, err := opentamsclient.ParseRange(o.Replace)
	if err != nil {
		return fmt.Errorf("--replace: %w", err)
	}
	if replaceEnd <= replaceStart {
		return errors.New("--replace must be a forward, non-empty range")
	}

	parent, err := loadParentParams(ctx, client, o.From)
	if err != nil {
		return fmt.Errorf("read parent flow: %w", err)
	}
	logf("parent flow %s: %dx%d @%dfps", o.From, parent.Width, parent.Height, parent.FrameRate)

	workDir := o.OutDir
	if workDir == "" {
		tmp, err := os.MkdirTemp("", "opentamsedit-*")
		if err != nil {
			return err
		}
		workDir = tmp
		defer func() { _ = os.RemoveAll(workDir) }()
	} else if err := os.MkdirAll(workDir, 0o750); err != nil {
		return err
	}

	slatePath, err := buildSlate(ctx, workDir, parent, o, replaceStart, replaceEnd-replaceStart)
	if err != nil {
		return fmt.Errorf("build slate: %w", err)
	}

	if err := putForkedFlow(ctx, client, parent, o.To); err != nil {
		return fmt.Errorf("create fork flow: %w", err)
	}

	allocations, err := client.AllocateStorage(ctx, o.To, 1)
	if err != nil || len(allocations) == 0 {
		return fmt.Errorf("allocate slate object: %w", err)
	}
	slateObj := allocations[0]
	if err := uploadFile(ctx, client, slatePath, slateObj); err != nil {
		return fmt.Errorf("upload slate: %w", err)
	}

	parentSegs, err := client.ListAllSegments(ctx, o.From, "")
	if err != nil {
		return fmt.Errorf("list parent segments: %w", err)
	}
	forked, err := overlaySegments(parentSegs, replaceStart, replaceEnd, slateObj.ObjectID)
	if err != nil {
		return err
	}
	logf("parent had %d segment(s); fork has %d (slate replaces %s)",
		len(parentSegs), len(forked), opentamsclient.HalfOpenRange(replaceStart, replaceEnd))

	if err := client.RegisterSegments(ctx, o.To, forked, opentamsclient.NewIdempotencyKey()); err != nil {
		return fmt.Errorf("register fork segments: %w", err)
	}
	fmt.Println(o.To)
	return nil
}

// loadParentParams reads --from as a raw JSON object so we can pluck
// fields out of the union variant without knowing which one it is at
// compile time. The set of fields we use is a subset shared by every
// video Flow variant (codec, source_id, essence_parameters.*).
func loadParentParams(ctx context.Context, client *opentamsclient.Client, flowID string) (parentFlowParams, error) {
	var raw map[string]any
	if err := clientGetRaw(ctx, client, "/tams/v1/flows/"+flowID, &raw); err != nil {
		return parentFlowParams{}, err
	}
	p := parentFlowParams{OriginFlow: flowID}
	p.SourceID, _ = raw["source_id"].(string)
	p.Format, _ = raw["format"].(string)
	p.Codec, _ = raw["codec"].(string)
	p.Container, _ = raw["container"].(string)
	if lab, ok := raw["label"].(string); ok {
		p.Label = lab
	}
	if ep, ok := raw["essence_parameters"].(map[string]any); ok {
		p.Width = intField(ep, "frame_width", 1280)
		p.Height = intField(ep, "frame_height", 720)
		if fr, ok := ep["frame_rate"].(map[string]any); ok {
			p.FrameRate = intField(fr, "numerator", 25)
		} else {
			p.FrameRate = 25
		}
	}
	if sd, ok := raw["segment_duration"].(map[string]any); ok {
		p.ChunkSecs = intField(sd, "numerator", 6)
	} else {
		p.ChunkSecs = 6
	}
	if p.SourceID == "" {
		return parentFlowParams{}, fmt.Errorf("parent flow %s has no source_id", flowID)
	}
	return p, nil
}

func intField(m map[string]any, k string, fallback int) int {
	if v, ok := m[k]; ok {
		switch t := v.(type) {
		case float64:
			return int(t)
		case int:
			return t
		case json.Number:
			if i, err := t.Int64(); err == nil {
				return int(i)
			}
		}
	}
	return fallback
}

// clientGetRaw reads `path` as a JSON object so we can pluck fields out
// of the Flow union variant without knowing which variant at compile
// time. The shared client's typed Flow getter would force us to commit
// to one variant; we want the raw map.
func clientGetRaw(ctx context.Context, client *opentamsclient.Client, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, client.BaseURL+path, http.NoBody)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+client.Token)
	req.Header.Set("Accept", "application/json")
	resp, err := client.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return &opentamsclient.HTTPError{Method: http.MethodGet, URL: client.BaseURL + path, StatusCode: resp.StatusCode, Body: body}
	}
	if out != nil && len(body) > 0 {
		return json.Unmarshal(body, out)
	}
	return nil
}

// uploadFile streams a local file to a presigned PUT URL.
func uploadFile(ctx context.Context, client *opentamsclient.Client, path string, obj opentamsclient.AllocatedObject) error {
	f, err := os.Open(path) //nolint:gosec // demo tool: operator-controlled path
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

func putForkedFlow(ctx context.Context, client *opentamsclient.Client, p parentFlowParams, newID string) error {
	body := map[string]any{
		"id":        newID,
		"source_id": p.SourceID,
		"format":    p.Format,
		"codec":     p.Codec,
		"label":     p.Label + " (break-slate)",
		"essence_parameters": map[string]any{
			"frame_width":  p.Width,
			"frame_height": p.Height,
			"frame_rate":   map[string]any{"numerator": p.FrameRate, "denominator": 1},
		},
		"segment_duration": map[string]any{
			"numerator":   p.ChunkSecs,
			"denominator": 1,
		},
	}
	if p.Container != "" {
		body["container"] = p.Container
	}
	return client.PutFlow(ctx, newID, body)
}

// buildSlate writes a single MPEG-TS file at workDir/slate.ts whose
// codec, resolution, frame rate, and audio match the parent flow.
// Duration is derived from the replace range; the output's MPEG-TS PTS
// is shifted by `startNs` so the slate plugs into the parent's timeline
// with continuous timestamps. That makes the chunks safely cat-able
// into a single TS file that a non-HLS player can decode without a
// playlist.
func buildSlate(ctx context.Context, workDir string, p parentFlowParams, o *forkOpts, startNs, durationNs int64) (string, error) {
	out := filepath.Join(workDir, "slate.ts")
	durSec := float64(durationNs) / 1e9
	if durSec <= 0 {
		return "", errors.New("non-positive slate duration")
	}
	startSec := float64(startNs) / 1e9
	durStr := strconv.FormatFloat(durSec, 'f', 6, 64)
	tsOffset := strconv.FormatFloat(startSec, 'f', 6, 64)
	res := fmt.Sprintf("%dx%d", p.Width, p.Height)
	fps := strconv.Itoa(p.FrameRate)

	var args []string
	if o.SlateFile != "" {
		// Re-encode an existing clip to match parent essence.
		args = []string{
			"-y", "-i", o.SlateFile,
			"-t", durStr,
			"-vf", fmt.Sprintf("scale=%s,fps=%s", res, fps),
			"-c:v", "libx264", "-preset", "veryfast", "-profile:v", "main", "-level", "4.0", "-pix_fmt", "yuv420p",
			"-c:a", "aac", "-ar", "48000", "-ac", "2", "-b:a", "128k",
			"-output_ts_offset", tsOffset,
			"-mpegts_copyts", "1",
			"-f", "mpegts", out,
		}
	} else {
		text := escapeDrawtext(o.SlateText)
		args = []string{
			"-y",
			"-f", "lavfi", "-i", fmt.Sprintf("color=c=black:s=%s:r=%s:d=%s", res, fps, durStr),
			"-f", "lavfi", "-i", fmt.Sprintf("anullsrc=channel_layout=stereo:sample_rate=48000:d=%s", durStr),
			"-vf", fmt.Sprintf("drawtext=text='%s':fontcolor=white:fontsize=72:x=(w-text_w)/2:y=(h-text_h)/2", text),
			"-c:v", "libx264", "-preset", "veryfast", "-profile:v", "main", "-level", "4.0", "-pix_fmt", "yuv420p",
			"-c:a", "aac", "-ar", "48000", "-ac", "2", "-b:a", "128k",
			"-shortest",
			"-output_ts_offset", tsOffset,
			"-mpegts_copyts", "1",
			"-f", "mpegts", out,
		}
	}
	cmd := exec.CommandContext(ctx, "ffmpeg", args...) //nolint:gosec // operator-supplied parameters
	cmd.Stdout = io.Discard
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return out, nil
}

// escapeDrawtext escapes the few characters drawtext treats specially
// when the text is given inline (':' delimits filter options, '\\'
// escapes, single quotes already wrap the value).
func escapeDrawtext(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\'', ':', '\\':
			out = append(out, '\\', c)
		default:
			out = append(out, c)
		}
	}
	return string(out)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

// overlaySegments produces the segment list for the forked flow.
//
// The replace window MUST align to existing chunk boundaries. TAMS
// segments reference whole immutable objects; "trimming" by registering
// a shorter `timerange` on the same object_id is allowed at the
// metadata level but produces broken HLS playback (the player plays
// the entire TS file regardless of the advertised duration). For the
// demo to render correctly, we require boundary alignment and emit a
// helpful error listing the parent's segment timeranges otherwise.
func overlaySegments(parent []api.FlowSegment, replaceStart, replaceEnd int64, slateObjectID string) ([]api.FlowSegmentPost, error) {
	if err := checkAlignment(parent, replaceStart, replaceEnd); err != nil {
		return nil, err
	}
	out := make([]api.FlowSegmentPost, 0, len(parent)+1)
	for _, s := range parent {
		segStart, segEnd, err := opentamsclient.ParseRange(s.Timerange)
		if err != nil {
			return nil, fmt.Errorf("parse parent segment range %q: %w", s.Timerange, err)
		}
		// fully inside replace window → drop
		if segStart >= replaceStart && segEnd <= replaceEnd {
			continue
		}
		// otherwise copy verbatim — alignment was checked above so no
		// segment can straddle the boundary
		out = append(out, api.FlowSegmentPost{
			ObjectId:  s.ObjectId,
			Timerange: s.Timerange,
		})
	}
	out = append(out, api.FlowSegmentPost{
		ObjectId:  slateObjectID,
		Timerange: opentamsclient.HalfOpenRange(replaceStart, replaceEnd),
	})
	return out, nil
}

func checkAlignment(parent []api.FlowSegment, replaceStart, replaceEnd int64) error {
	for _, s := range parent {
		segStart, segEnd, err := opentamsclient.ParseRange(s.Timerange)
		if err != nil {
			return fmt.Errorf("parse parent segment range %q: %w", s.Timerange, err)
		}
		// A segment straddles the start boundary if it starts strictly
		// before and ends strictly after — same for end.
		if segStart < replaceStart && segEnd > replaceStart {
			return alignmentError(parent, replaceStart, replaceEnd, "start")
		}
		if segStart < replaceEnd && segEnd > replaceEnd {
			return alignmentError(parent, replaceStart, replaceEnd, "end")
		}
	}
	return nil
}

func alignmentError(parent []api.FlowSegment, replaceStart, replaceEnd int64, which string) error {
	lines := make([]string, 0, len(parent))
	for _, s := range parent {
		lines = append(lines, "  "+s.Timerange)
	}
	return fmt.Errorf(
		"--replace %s does not align with a chunk boundary at the %s; "+
			"pick a range that begins and ends on one of the parent segment boundaries below:\n%s",
		opentamsclient.HalfOpenRange(replaceStart, replaceEnd),
		which,
		joinLines(lines),
	)
}

func joinLines(lines []string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	return b.String()
}
