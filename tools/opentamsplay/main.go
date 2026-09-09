// Command opentamsplay is a tiny HLS gateway over the OpenTAMS read
// path. It turns:
//
//	GET /play/{flowId}.m3u8[?range=<tams-timerange>&live=true]
//
// into a TAMS segment listing and emits an HLS manifest pointing at
// the presigned GET URLs TAMS already returned. For live mode the
// manifest is rebuilt on every request and #EXT-X-ENDLIST is omitted,
// so VLC keeps polling — the gateway re-asks TAMS each poll.
//
// Default listen address is :8090.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"strconv"
	"strings"
	"syscall"
	"time"

	api "github.com/amagioss/opentams/gen/api"
	"github.com/amagioss/opentams/tools/internal/opentamsclient"
)

const (
	defaultListenAddr = ":8090"
	playPrefix        = "/play/"
	segPrefix         = "/seg/"
	mediaSuffix       = ".m3u8"
	manifestTimeout   = 20 * time.Second
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	listen := envOr("OPENTAMSPLAY_LISTEN", defaultListenAddr)
	client := opentamsclient.New(
		envOr("OPENTAMS_BASE_URL", "http://localhost:8080"),
		envOr("OPENTAMS_TOKEN", "dev"),
	)

	mux := http.NewServeMux()
	mux.HandleFunc(playPrefix, makeManifestHandler(client))
	mux.HandleFunc(segPrefix, segmentRedirectHandler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	srv := &http.Server{
		Addr:              listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	logf("opentamsplay listening on %s", listen)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func makeManifestHandler(client *opentamsclient.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flowID, err := parseFlowID(r.URL.Path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		live := r.URL.Query().Get("live") == "true"
		rng := r.URL.Query().Get("range")
		if rng == "" {
			rng = "_"
		}

		ctx, cancel := context.WithTimeout(r.Context(), manifestTimeout)
		defer cancel()
		segments, err := client.ListAllSegments(ctx, flowID, rng)
		if err != nil {
			http.Error(w, fmt.Sprintf("list segments: %v", err), http.StatusBadGateway)
			return
		}
		if len(segments) == 0 {
			http.Error(w, "no segments in range "+rng, http.StatusNotFound)
			return
		}
		manifest := renderManifest(segments, live)
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(manifest)
	}
}

// parseFlowID extracts the flow UUID from /play/{flowId}.m3u8.
func parseFlowID(urlPath string) (string, error) {
	rest := strings.TrimPrefix(urlPath, playPrefix)
	if rest == urlPath {
		return "", fmt.Errorf("path must start with %s", playPrefix)
	}
	base := path.Base(rest)
	if !strings.HasSuffix(base, mediaSuffix) {
		return "", fmt.Errorf("path must end in %s", mediaSuffix)
	}
	id := strings.TrimSuffix(base, mediaSuffix)
	if id == "" {
		return "", errors.New("empty flow id")
	}
	return id, nil
}

// segmentRedirectHandler turns /seg/{anything}.ts?u=<urlencoded-presigned>
// into a 302 to the presigned URL. The on-disk-looking `.ts` filename is
// the whole point: ffmpeg's HLS demuxer rejects segment URLs whose path
// has no extension in its `allowed_extensions` list, so we wrap each
// presigned URL behind a stable `.ts` filename here.
//
// To stop the endpoint from acting as an open redirector, the decoded
// target must point at the configured object-store base (defaults to
// http://localhost:9000, override via OPENTAMSPLAY_OBJECT_BASE).
func segmentRedirectHandler(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Query().Get("u")
	if target == "" {
		http.Error(w, "missing u= parameter", http.StatusBadRequest)
		return
	}
	decoded, err := url.QueryUnescape(target)
	if err != nil {
		http.Error(w, "u= is not URL-encoded: "+err.Error(), http.StatusBadRequest)
		return
	}
	allowedBase := envOr("OPENTAMSPLAY_OBJECT_BASE", "http://localhost:9000")
	if err := targetWithinBase(decoded, allowedBase); err != nil {
		http.Error(w, "redirect target outside allowed base "+allowedBase+": "+err.Error(), http.StatusForbidden)
		return
	}
	// gosec G710: targetWithinBase constrains the redirect to
	// OPENTAMSPLAY_OBJECT_BASE above, so this is not an open redirector.
	// Demo tool; production code should layer in CSRF protection on top.
	http.Redirect(w, r, decoded, http.StatusFound) //nolint:gosec // see comment
}

func renderManifest(segments []api.FlowSegment, live bool) []byte {
	durs := make([]time.Duration, 0, len(segments))
	urls := make([]string, 0, len(segments))
	for i, s := range segments {
		d := segmentDuration(s)
		u := pickGetURL(s)
		if u == "" {
			// Skip segments without a usable URL — the demo's storage
			// backend (MinIO via S3) always emits one, so this is rare.
			continue
		}
		// Wrap the presigned URL behind a /seg/{i}.ts redirect so HLS
		// demuxers (ffmpeg, in particular) see a `.ts` extension on the
		// playlist URI and don't trip their allowed_extensions check.
		// The player resolves the absolute-path URI against the
		// manifest URI's host, so the redirect endpoint co-locates with
		// the gateway automatically.
		wrapped := fmt.Sprintf("%sseg-%d.ts?u=%s", segPrefix, i, url.QueryEscape(u))
		durs = append(durs, d)
		urls = append(urls, wrapped)
	}
	target := 0
	for _, d := range durs {
		if s := int(d.Round(time.Second) / time.Second); s > target {
			target = s
		}
	}
	if target == 0 {
		target = 6 // fall-back; matches publisher default chunk size
	}

	var buf bytes.Buffer
	buf.WriteString("#EXTM3U\n")
	buf.WriteString("#EXT-X-VERSION:3\n")
	buf.WriteString("#EXT-X-TARGETDURATION:" + strconv.Itoa(target) + "\n")
	buf.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")
	if !live {
		buf.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n")
	}
	for i, u := range urls {
		buf.WriteString("#EXTINF:" + strconv.FormatFloat(durs[i].Seconds(), 'f', 3, 64) + ",\n")
		buf.WriteString(u + "\n")
	}
	if !live {
		buf.WriteString("#EXT-X-ENDLIST\n")
	}
	return buf.Bytes()
}

// segmentDuration derives the chunk duration from the segment's
// `timerange` field. The OpenAPI also exposes `last_duration` but that
// is keyed to the object, not the segment view, so it can disagree
// after a fork/trim.
func segmentDuration(s api.FlowSegment) time.Duration {
	startNs, endNs, err := opentamsclient.ParseRange(s.Timerange)
	if err != nil || endNs <= startNs {
		return 6 * time.Second
	}
	return time.Duration(endNs - startNs)
}

// pickGetURL chooses the HTTP(S) presigned URL out of a segment's
// get_urls list. OpenTAMS returns both the canonical `s3://` URI and a
// presigned HTTPS URL (one entry each) when ?presigned is not set; HLS
// players can only fetch over HTTP(S), so we skip the s3:// entry.
func pickGetURL(s api.FlowSegment) string {
	if s.GetUrls == nil {
		return ""
	}
	for _, g := range *s.GetUrls {
		if g.Url == "" {
			continue
		}
		if strings.HasPrefix(g.Url, "s3://") {
			continue
		}
		return g.Url
	}
	return ""
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

// targetWithinBase reports whether a redirect target really belongs to
// the allowed base, by comparing parsed URLs rather than raw strings.
//
// A string prefix test is not enough. With a base of
// http://localhost:9000 it admits http://localhost:9000.evil.com,
// because the host simply continues past the port, and
// http://localhost:9000@evil.com, where everything before the "@" is
// userinfo and the real host is evil.com. Both redirect off-host while
// passing a prefix check.
//
// Scheme and host must match exactly. The path must match at a
// separator, so /bucket is not treated as a prefix of /bucket-other.
// Userinfo on the target is refused outright: it has no legitimate use
// here and is the readable half of the second bypass above.
func targetWithinBase(target, base string) error {
	t, err := url.Parse(target)
	if err != nil {
		return fmt.Errorf("target is not a URL: %w", err)
	}
	b, err := url.Parse(base)
	if err != nil {
		return fmt.Errorf("base is not a URL: %w", err)
	}
	if !t.IsAbs() {
		return errors.New("target is not absolute")
	}
	if t.User != nil {
		return errors.New("target carries userinfo")
	}
	if !strings.EqualFold(t.Scheme, b.Scheme) {
		return fmt.Errorf("scheme %q is not %q", t.Scheme, b.Scheme)
	}
	if !strings.EqualFold(t.Host, b.Host) {
		return fmt.Errorf("host %q is not %q", t.Host, b.Host)
	}
	basePath := strings.TrimSuffix(b.Path, "/")
	if basePath != "" && t.Path != basePath && !strings.HasPrefix(t.Path, basePath+"/") {
		return fmt.Errorf("path %q is outside %q", t.Path, basePath)
	}
	return nil
}
