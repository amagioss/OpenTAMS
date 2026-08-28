package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/amagioss/opentams/tools/internal/opentamsclient"
)

// commonFlags holds the connection + flow identity flags shared by the
// file and live subcommands.
type commonFlags struct {
	BaseURL  string
	Token    string
	FlowID   string
	SourceID string
	Label    string
	ChunkDur time.Duration
	Width    int
	Height   int
	FPS      int
	StartTS  string // RFC3339 or "now" or "0"; "" → 0
	Copy     bool   // pass through with `-c copy`, no transcode

	// copyCodecMime is populated by applyCopyModeFlowParams when --copy
	// is set; empty in encode mode (we always write "video/h264" then).
	copyCodecMime string
}

const (
	defaultChunkDur     = 6 * time.Second
	defaultWidth        = 1280
	defaultHeight       = 720
	defaultFPS          = 25
	defaultStorageBatch = 16
	maxStorageBatch     = 1024
)

func resolveIDs(c *commonFlags) {
	if c.FlowID == "" {
		c.FlowID = uuid.NewString()
	}
	if c.SourceID == "" {
		c.SourceID = uuid.NewString()
	}
	if c.Label == "" {
		c.Label = "opentamspub-" + c.FlowID[:8]
	}
}

// startNs returns the demo's reference clock origin in TAI nanoseconds.
// Defaults to 0 so demo ranges like `--replace [3:0_8:0)` line up with
// segments anchored at `[0:0_6:0)`, `[6:0_…)`. Pass "now" to anchor at
// wall clock (real TAMS deployments care about the leap-second offset;
// the demo does not), or an RFC3339 timestamp for a deterministic
// non-zero anchor.
func startNs(c *commonFlags) (int64, error) {
	switch c.StartTS {
	case "", "0", "0:0":
		return 0, nil
	case "now":
		return time.Now().UTC().UnixNano(), nil
	}
	t, err := time.Parse(time.RFC3339Nano, c.StartTS)
	if err != nil {
		return 0, fmt.Errorf("--start must be 0, now, or RFC3339 (e.g. 2026-01-01T00:00:00Z): %w", err)
	}
	return t.UTC().UnixNano(), nil
}

// applyCopyModeFlowParams ffprobes the input and updates the commonFlags
// width/height/codec so the Flow metadata matches the passed-through
// streams. Called only when --copy is set; transcode mode uses fixed
// libx264 parameters.
func applyCopyModeFlowParams(ctx context.Context, input string, c *commonFlags) error {
	codec, w, h, err := probeInput(ctx, input)
	if err != nil {
		return fmt.Errorf("probe %s: %w", input, err)
	}
	if w > 0 {
		c.Width = w
	}
	if h > 0 {
		c.Height = h
	}
	c.copyCodecMime = ffmpegCodecToMime(codec)
	return nil
}

// videoFlowBody builds the JSON body for PUT /flows/{id}. We use a
// map[string]any rather than the typed Flow union because the typed
// union forces every nested-optional pointer to be wired up — we'd
// rather hand-write the small subset of fields HLS playback needs.
func videoFlowBody(c *commonFlags) map[string]any {
	codec := c.copyCodecMime
	if codec == "" {
		codec = "video/h264"
	}
	essence := map[string]any{
		"frame_width":  c.Width,
		"frame_height": c.Height,
	}
	// In copy mode we don't force the input to a particular framerate;
	// flagging the flow as VFR avoids contradicting the actual stream.
	if c.Copy {
		essence["vfr"] = true
	} else {
		essence["frame_rate"] = map[string]any{"numerator": c.FPS, "denominator": 1}
	}
	return map[string]any{
		"id":                 c.FlowID,
		"source_id":          c.SourceID,
		"format":             "urn:x-nmos:format:video",
		"codec":              codec,
		"container":          "video/mp2t",
		"label":              c.Label,
		"essence_parameters": essence,
		"segment_duration": map[string]any{
			"numerator":   int(c.ChunkDur / time.Second),
			"denominator": 1,
		},
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// newClient wires up the shared HTTP client honouring the standard env
// vars used everywhere else in the repo (examples/, smoke.sh, …).
func newClient(c *commonFlags) *opentamsclient.Client {
	return opentamsclient.New(
		envOr("OPENTAMS_BASE_URL", c.BaseURL),
		envOr("OPENTAMS_TOKEN", c.Token),
	)
}
