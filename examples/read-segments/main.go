// Example: list segments on a Flow filtered by time-range.
//
// Companion to ../write-segment/main.go. Pass the flow_id printed by
// the write example via the FLOW_ID environment variable.
//
//	FLOW_ID=<uuid> go run ./examples/read-segments
//
// Configurable via environment variables:
//
//	OPENTAMS_BASE_URL  default http://localhost:8080
//	OPENTAMS_TOKEN     default "dev"
//	FLOW_ID            required
//	TIMERANGE          default "[0:0_10:0)" — TAMS time-range syntax;
//	                   see docs/conformance.md for the supported formats.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

func main() {
	baseURL := envOr("OPENTAMS_BASE_URL", "http://localhost:8080")
	token := envOr("OPENTAMS_TOKEN", "dev")
	flowID := os.Getenv("FLOW_ID")
	timerange := envOr("TIMERANGE", "[0:0_10:0)")

	if flowID == "" {
		fail("FLOW_ID is required (use the flow_id printed by ./examples/write-segment)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	logf("listing segments on flow=%s timerange=%s", flowID, timerange)

	// The TAMS list-segments endpoint accepts a `timerange` query
	// parameter that filters segments whose declared time-range
	// intersects the supplied range. The TAMS time-range syntax uses
	// `[start_end)` notation with seconds:nanoseconds — see
	// docs/conformance.md for the full grammar OpenTAMS accepts.
	q := url.Values{}
	q.Set("timerange", timerange)
	path := "/tams/v1/flows/" + flowID + "/segments?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path, http.NoBody) //nolint:gosec // example program; baseURL is supplied by the operator running the example, not user input
	if err != nil {
		fail("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req) //nolint:gosec // example program; baseURL is operator-supplied, not user-tainted
	if err != nil {
		fail("GET segments: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		fail("GET segments → %d: %s", resp.StatusCode, string(body))
	}

	// Segments come back as a JSON array. We decode loosely (map[string]any)
	// rather than against a typed struct so the example doesn't go stale
	// the moment the OpenAPI spec gains a new optional field.
	var segments []map[string]any
	if err := json.Unmarshal(body, &segments); err != nil {
		fail("decode response: %v\nbody: %s", err, string(body))
	}

	logf("got %d segment(s):", len(segments))
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	for i, s := range segments {
		fmt.Printf("\n# segment %d\n", i+1)
		_ = enc.Encode(s)
	}

	// Pagination cursor exposure (see docs/conformance.md "Pagination
	// cursor format"): OpenTAMS emits two parallel headers carrying
	// the same cursor — `X-Paging-NextKey` (opaque cursor string) and
	// a non-canonical RFC 8288 `Link: <>; rel="next"; key="<cursor>"`
	// (URI slot empty, cursor in `key=`). A real client picks one and
	// passes the cursor back as the `key` query parameter on the next
	// request, repeating until neither header appears.
	if cursor := resp.Header.Get("X-Paging-NextKey"); cursor != "" {
		logf("X-Paging-NextKey: %s (pass as ?key=… for the next page)", cursor)
	}
	if linkHdr := resp.Header.Get("Link"); linkHdr != "" {
		logf("Link: %s", truncate(linkHdr, 200))
	}
}

// --- helpers -----------------------------------------------------------------

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
