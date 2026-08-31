// Command opentamsassemble downloads every segment in an OpenTAMS Flow's
// timerange and concatenates the bytes to stdout (or `-o file`). For
// MPEG-TS flows that is a complete, decoder-ready stream — no
// transcoding, no manifest, no player-side logic required.
//
//	opentamsassemble <flowId> [--range "[0:0_30:0)"] [-o out.ts]
//
// Pair with the publisher's --copy mode for a fully transcode-free
// loop: upload → replace → download → cat → play.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	api "github.com/amagioss/opentams/gen/api"
	"github.com/amagioss/opentams/tools/internal/opentamsclient"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

type opts struct {
	BaseURL string
	Token   string
	FlowID  string
	Range   string
	Out     string
}

func parseArgs() (*opts, error) {
	o := &opts{
		BaseURL: envOr("OPENTAMS_BASE_URL", "http://localhost:8080"),
		Token:   envOr("OPENTAMS_TOKEN", "dev"),
		Range:   "_",
		Out:     "-",
	}
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--range":
			if i+1 >= len(args) {
				return nil, errors.New("--range needs a value")
			}
			o.Range = args[i+1]
			i++
		case "-o", "--output":
			if i+1 >= len(args) {
				return nil, errors.New("-o needs a value")
			}
			o.Out = args[i+1]
			i++
		case "--base-url":
			if i+1 >= len(args) {
				return nil, errors.New("--base-url needs a value")
			}
			o.BaseURL = args[i+1]
			i++
		case "--token":
			if i+1 >= len(args) {
				return nil, errors.New("--token needs a value")
			}
			o.Token = args[i+1]
			i++
		case "-h", "--help":
			usage()
			os.Exit(0)
		default:
			if strings.HasPrefix(args[i], "-") {
				return nil, fmt.Errorf("unknown flag: %s", args[i])
			}
			if o.FlowID != "" {
				return nil, fmt.Errorf("multiple flow IDs given: %s and %s", o.FlowID, args[i])
			}
			o.FlowID = args[i]
		}
	}
	if o.FlowID == "" {
		usage()
		return nil, errors.New("flow id is required")
	}
	return o, nil
}

func usage() {
	fmt.Fprintln(os.Stderr, `opentamsassemble <flowId> [--range "<tams>"] [-o out.ts]

Downloads every segment in the flow's timerange and writes the
concatenated bytes to stdout (default) or the path supplied with -o.

Environment:
  OPENTAMS_BASE_URL   default http://localhost:8080
  OPENTAMS_TOKEN      default "dev"`)
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	o, err := parseArgs()
	if err != nil {
		return err
	}
	client := opentamsclient.New(o.BaseURL, o.Token)

	segments, err := client.ListAllSegments(ctx, o.FlowID, o.Range)
	if err != nil {
		return fmt.Errorf("list segments: %w", err)
	}
	if len(segments) == 0 {
		return fmt.Errorf("no segments in range %s", o.Range)
	}
	fmt.Fprintf(os.Stderr, "downloading %d segment(s) from flow %s\n", len(segments), o.FlowID)

	out, closer, err := openOutput(o.Out)
	if err != nil {
		return err
	}
	defer func() { _ = closer() }()

	httpClient := &http.Client{Timeout: 5 * time.Minute}
	totalBytes := int64(0)
	for i, s := range segments {
		u := pickGetURL(s)
		if u == "" {
			return fmt.Errorf("segment %d (%s) has no usable get_urls entry", i, s.Timerange)
		}
		n, err := streamSegment(ctx, httpClient, u, out)
		if err != nil {
			return fmt.Errorf("segment %d (%s): %w", i, s.Timerange, err)
		}
		totalBytes += n
		fmt.Fprintf(os.Stderr, "  [%d/%d] %s  %.2f MiB\n", i+1, len(segments), s.Timerange, float64(n)/(1<<20))
	}
	fmt.Fprintf(os.Stderr, "wrote %.2f MiB total\n", float64(totalBytes)/(1<<20))
	return nil
}

func openOutput(path string) (io.Writer, func() error, error) {
	if path == "-" || path == "" {
		return os.Stdout, func() error { return nil }, nil
	}
	f, err := os.Create(path) //nolint:gosec // operator-supplied path
	if err != nil {
		return nil, nil, err
	}
	return f, f.Close, nil
}

func streamSegment(ctx context.Context, httpClient *http.Client, rawURL string, dst io.Writer) (int64, error) {
	// Re-validated at the point of use, not only where the URL was chosen:
	// this is the only sink that turns a service-supplied string into an
	// outbound request, and a future caller must not be able to reach it
	// without passing the scheme check.
	if !isFetchableURL(rawURL) {
		return 0, fmt.Errorf("refusing to fetch %q: only http and https URLs are followed", rawURL)
	}
	// gosec G704 flags both this and the Do below as SSRF-by-taint, because
	// the URL originates in a server response. That is what the tool is for:
	// it downloads the media the service instance tells it to. The scheme is
	// validated immediately above; gosec's taint analysis cannot see that,
	// since the check is a function call rather than an inline comparison.
	// Host-level restrictions (private ranges, redirect re-validation, DNS
	// rebinding) are deliberately NOT provided here — see the note on
	// pickGetURL. Suppressed knowingly, with that gap stated.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody) //nolint:gosec // G704: scheme-validated above; host-level policy is separate work
	if err != nil {
		return 0, err
	}
	resp, err := httpClient.Do(req) //nolint:gosec // G704: same request, already scheme-validated
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	return io.Copy(dst, resp.Body)
}

// pickGetURL chooses an HTTP(S) URL from a segment's get_urls list,
// skipping the canonical `s3://` URI that OpenTAMS also returns.
//
// The URLs come from the server's response, so this tool fetches whatever
// the service instance points it at. That is the job — but it means a
// compromised or hostile service instance chooses the request target, so
// the scheme is checked rather than assumed. Anything that is not http or
// https is skipped, not fetched.
//
// This is a scheme check only. It does not defend against a hostile
// service instance naming a private-network address (link-local metadata
// endpoints in particular), a redirect chain that lands somewhere else, or
// DNS rebinding. Those need a real threat review, which is tracked
// separately; do not read this function as providing them.
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
		if !isFetchableURL(g.Url) {
			continue
		}
		return g.Url
	}
	return ""
}

// isFetchableURL reports whether raw is an absolute http or https URL with a
// host. Everything else — file, gopher, data, a scheme-relative or relative
// reference — is refused.
func isFetchableURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	return u.Host != ""
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
