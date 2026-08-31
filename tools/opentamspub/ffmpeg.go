package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// hlsEntry is one chunk advertised by ffmpeg's HLS playlist.
type hlsEntry struct {
	File        string        // relative path inside the playlist dir
	Duration    time.Duration // EXTINF value
	StartOffset time.Duration // cumulative offset from playlist start
}

// buildFfmpegArgs returns the argv for an ffmpeg HLS segmentation run.
// In encode mode (the default) the configuration is fixed so the editor
// can synthesize slates with identical essence parameters (same codec,
// framerate, resolution, pixel format) without ffprobing the input
// first. In copy mode (`--copy`) ffmpeg passes the input streams
// through unchanged, splitting only at existing keyframes — no transcode.
func buildFfmpegArgs(input string, c *commonFlags, outDir string) []string {
	chunkSecs := max(int(c.ChunkDur/time.Second), 1)
	playlist := filepath.Join(outDir, "out.m3u8")
	segPattern := filepath.Join(outDir, "out_%05d.ts")
	if c.Copy {
		return []string{
			"-y",
			"-i", input,
			"-c", "copy",
			"-f", "hls",
			"-hls_time", strconv.Itoa(chunkSecs),
			"-hls_segment_type", "mpegts",
			"-hls_segment_filename", segPattern,
			"-hls_list_size", "0",
			"-hls_flags", "append_list+independent_segments",
			playlist,
		}
	}
	// `-force_key_frames expr:gte(t,n_forced*N)` makes libx264 emit an
	// IDR at exactly every multiple of N seconds in output time. Without
	// it, libx264 only guarantees an IDR every `-g` *frames*, and the
	// HLS muxer waits for the next IDR after `-hls_time`, so chunks
	// drift longer when the source has scene changes, B-frame churn, or
	// corrupted leading NALUs (e.g. broadcast TS captures). Forcing IDR
	// at fixed wall-clock instants makes chunking deterministic.
	forceKey := fmt.Sprintf("expr:gte(t,n_forced*%d)", chunkSecs)
	return []string{
		"-y",
		"-i", input,
		"-vf", fmt.Sprintf("scale=%d:%d,fps=%d", c.Width, c.Height, c.FPS),
		"-c:v", "libx264", "-preset", "veryfast", "-profile:v", "main",
		"-level", "4.0", "-pix_fmt", "yuv420p",
		"-g", strconv.Itoa(c.FPS * chunkSecs),
		"-force_key_frames", forceKey,
		"-c:a", "aac", "-ar", "48000", "-ac", "2", "-b:a", "128k",
		"-f", "hls",
		"-hls_time", strconv.Itoa(chunkSecs),
		"-hls_segment_type", "mpegts",
		"-hls_segment_filename", segPattern,
		"-hls_list_size", "0",
		"-hls_flags", "append_list+independent_segments",
		playlist,
	}
}

// runFfmpeg runs an ffmpeg HLS segmentation pipeline. Stderr is mirrored
// to the publisher's stderr behind a "[ffmpeg]" prefix so the user can
// see live progress without grepping ffmpeg's raw output.
func runFfmpeg(ctx context.Context, input string, c *commonFlags, outDir string) error {
	args := buildFfmpegArgs(input, c, outDir)
	cmd := exec.CommandContext(ctx, "ffmpeg", args...) //nolint:gosec // operator-supplied input is intentional for a demo publisher
	cmd.Stdout = io.Discard
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ffmpeg: %w", err)
	}
	go pipePrefixed(stderr, "[ffmpeg] ")
	if err := cmd.Wait(); err != nil {
		// Context cancellation is the normal "live mode SIGINT" path —
		// ffmpeg exits non-zero but the publisher itself is fine.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("ffmpeg exited: %w", err)
	}
	return nil
}

// probeInput uses ffprobe to read the input's video codec, width and
// height. It is only called in copy mode, where the Flow's essence
// parameters must reflect the actual passed-through stream (in encode
// mode we know the exact target codec because we transcode to it).
func probeInput(ctx context.Context, input string) (codec string, width, height int, err error) {
	args := []string{
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=codec_name,width,height",
		"-of", "default=nw=1",
		input,
	}
	cmd := exec.CommandContext(ctx, "ffprobe", args...) //nolint:gosec // operator-supplied input
	out, err := cmd.Output()
	if err != nil {
		return "", 0, 0, fmt.Errorf("ffprobe: %w", err)
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "codec_name":
			codec = v
		case "width":
			if n, perr := strconv.Atoi(v); perr == nil {
				width = n
			}
		case "height":
			if n, perr := strconv.Atoi(v); perr == nil {
				height = n
			}
		}
	}
	return codec, width, height, nil
}

// ffmpegCodecToMime maps ffprobe's codec_name to a MIME type as used in
// the Flow's `codec` field. Only the codecs we expect to see in TAMS
// demos are covered; unknown values fall back to "video/h264" which is
// the safest assumption for a presentation demo.
func ffmpegCodecToMime(codec string) string {
	switch codec {
	case "h264":
		return "video/h264"
	case "hevc", "h265":
		return "video/h265"
	case "mpeg2video":
		return "video/mpeg2"
	case "vp9":
		return "video/vp9"
	case "av1":
		return "video/av1"
	default:
		return "video/h264"
	}
}

func pipePrefixed(r io.Reader, prefix string) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		fmt.Fprintln(os.Stderr, prefix+sc.Text())
	}
}

// readPlaylist parses an ffmpeg HLS playlist and returns the chunks
// declared so far. It tolerates partial files (live mode rewrites the
// playlist after each chunk closes). `done` is true if #EXT-X-ENDLIST
// is present, signalling ffmpeg has finished writing.
func readPlaylist(path string) (entries []hlsEntry, done bool, err error) {
	f, err := os.Open(path) //nolint:gosec // demo tool: path is operator-controlled
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	var pendingDur time.Duration
	var offset time.Duration
	dir := filepath.Dir(path)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "#EXTINF:"):
			body := strings.TrimPrefix(line, "#EXTINF:")
			// `#EXTINF:6.000000,` — strip trailing comma + optional title.
			body = strings.TrimSuffix(strings.SplitN(body, ",", 2)[0], ",")
			secs, perr := strconv.ParseFloat(body, 64)
			if perr != nil {
				return nil, false, fmt.Errorf("playlist: bad EXTINF %q: %w", body, perr)
			}
			pendingDur = time.Duration(secs * float64(time.Second))
		case line == "#EXT-X-ENDLIST":
			done = true
		case line == "" || strings.HasPrefix(line, "#"):
			// header or blank — ignore.
		default:
			entries = append(entries, hlsEntry{
				File:        filepath.Join(dir, line),
				Duration:    pendingDur,
				StartOffset: offset,
			})
			offset += pendingDur
			pendingDur = 0
		}
	}
	if err := sc.Err(); err != nil {
		return nil, false, err
	}
	return entries, done, nil
}
