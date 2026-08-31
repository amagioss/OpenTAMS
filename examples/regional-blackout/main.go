// Example: derive a regional variant of a Flow that withholds one time
// window, without copying a single byte of media.
//
// A rights restriction means one region must not receive a portion of a
// programme. The usual answer is a second package: re-run the packager,
// write a second set of segment files, ship a second manifest. TAMS
// needs neither. Segments are references to immutable Media Objects, so
// a regional Flow lists the same object_ids as the world Flow and simply
// omits the ones it must not carry.
//
// The restricted media is not hidden from the regional client, it is
// unreachable by it: a Flow's segment list is the only path to a
// presigned URL for an object, so an object nothing references cannot be
// fetched through that Flow.
//
// Run against a local stack, after publishing a clip:
//
//	make run                                   # in another terminal
//	FLOW_ID=$(./scripts/demo.sh file clip.mp4 | tail -1)
//	FLOW_ID=$FLOW_ID go run ./examples/regional-blackout
//
// Configurable via environment variables:
//
//	OPENTAMS_BASE_URL  default http://localhost:8080
//	OPENTAMS_TOKEN     default "dev" (local stack only)
//	FLOW_ID            required — the world feed to derive from
//	BLACKOUT           default: the middle segment of the world feed.
//	                   A TAMS timerange, e.g. "[12:0_18:0)". Must begin
//	                   and end on a segment boundary — see README.md.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// segmentBatchLimit is the server's cap on a single POST /segments body.
// Long flows are registered in batches, each under its own idempotency
// key so a retry of one batch never replays another.
const segmentBatchLimit = 500

type segment struct {
	ObjectID  string `json:"object_id"`
	Timerange string `json:"timerange"`
	GetURLs   []struct {
		URL string `json:"url"`
	} `json:"get_urls"`
}

func main() {
	baseURL := envOr("OPENTAMS_BASE_URL", "http://localhost:8080")
	token := envOr("OPENTAMS_TOKEN", "dev")
	worldID := os.Getenv("FLOW_ID")
	if worldID == "" {
		fail("FLOW_ID is required — publish a clip first:\n" +
			"  FLOW_ID=$(./scripts/demo.sh file <clip.mp4> | tail -1)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	c := &client{base: baseURL, token: token, http: &http.Client{Timeout: 15 * time.Second}}

	// 1. Read the world Flow. We keep it as a raw map because the Flow
	//    schema is a union (video / audio / data) and the regional
	//    variant must carry the parent's essence parameters verbatim,
	//    whichever variant this is.
	var world map[string]any
	if err := c.do(ctx, http.MethodGet, "/tams/v1/flows/"+worldID, nil, &world); err != nil {
		fail("read world flow: %v", err)
	}

	// 2. Page through every segment on the world Flow. The cursor comes
	//    back in X-Paging-NextKey and goes out as the `key` query param.
	worldSegs, err := c.listSegments(ctx, worldID, "")
	if err != nil {
		fail("list world segments: %v", err)
	}
	if len(worldSegs) == 0 {
		fail("world flow %s has no segments", worldID)
	}

	// 3. Choose the restricted window. Default to the middle segment so
	//    the example runs with no configuration and always lands on a
	//    boundary.
	blackout := os.Getenv("BLACKOUT")
	if blackout == "" {
		blackout = worldSegs[len(worldSegs)/2].Timerange
	}
	loNs, hiNs, err := parseRange(blackout)
	if err != nil {
		fail("BLACKOUT %q: %v", blackout, err)
	}
	if err := checkAligned(worldSegs, loNs, hiNs); err != nil {
		fail("%v", err)
	}

	regionalID := newUUID()
	logf("world flow    : %s  (%d segments)", worldID, len(worldSegs))
	logf("regional flow : %s", regionalID)
	logf("restricted    : %s", blackout)
	logf("")

	// 4. Create the regional Flow. Same Source, same essence parameters:
	//    it is the same content under a different rights policy, not a
	//    different encoding. Server-managed fields must be omitted.
	regional := make(map[string]any, len(world))
	for k, v := range world {
		regional[k] = v
	}
	for _, k := range []string{"created", "metadata_updated", "segments_updated", "timerange", "collected_by"} {
		delete(regional, k)
	}
	regional["id"] = regionalID
	regional["label"] = labelOf(world) + " (regional)"

	logf("→ PUT /tams/v1/flows/%s", regionalID)
	if err := c.do(ctx, http.MethodPut, "/tams/v1/flows/"+regionalID, regional, nil); err != nil {
		fail("create regional flow: %v", err)
	}

	// 5. Register the world Flow's own object_ids against the regional
	//    Flow, skipping everything inside the restricted window. No
	//    ts_offset and no re-basing: the regional timeline stays aligned
	//    with the world clock, so timecodes and ad markers still line up.
	//    The result is a hole, not a shift.
	kept := make([]map[string]string, 0, len(worldSegs))
	var withheld int
	for _, s := range worldSegs {
		segLo, segHi, perr := parseRange(s.Timerange)
		if perr != nil {
			fail("parse world segment %q: %v", s.Timerange, perr)
		}
		if segLo >= loNs && segHi <= hiNs {
			withheld++
			continue
		}
		kept = append(kept, map[string]string{
			"object_id": s.ObjectID,
			"timerange": s.Timerange,
		})
	}

	for i := 0; i < len(kept); i += segmentBatchLimit {
		end := min(i+segmentBatchLimit, len(kept))
		key := "example-regional-blackout-" + newUUID()
		logf("→ POST /tams/v1/flows/%s/segments  (%d segments, X-Idempotency-Key=%s)",
			regionalID, end-i, key)
		if err := c.do(ctx, http.MethodPost, "/tams/v1/flows/"+regionalID+"/segments",
			kept[i:end], nil, withHeader("X-Idempotency-Key", key)); err != nil {
			fail("register segments: %v", err)
		}
	}

	// 6. The proof. Ask both Flows for the restricted window. The world
	//    Flow answers with an object and a URL to fetch it. The regional
	//    Flow answers with nothing — there is no reference, so there is
	//    no URL to hand out.
	logf("")
	logf("--- the restricted window, asked of both flows ---")
	logf("")

	worldHit, err := c.listSegments(ctx, worldID, blackout)
	if err != nil {
		fail("query world flow: %v", err)
	}
	logf("GET /flows/%s/segments?timerange=%s", short(worldID), blackout)
	report(worldHit)

	regionalHit, err := c.listSegments(ctx, regionalID, blackout)
	if err != nil {
		fail("query regional flow: %v", err)
	}
	logf("GET /flows/%s/segments?timerange=%s", short(regionalID), blackout)
	report(regionalHit)

	reused := len(kept)
	logf("--- summary ---")
	logf("")
	logf("  objects reused  : %d", reused)
	logf("  objects created : 0")
	logf("  bytes written   : 0")
	logf("  segments withheld: %d", withheld)
	logf("")
	logf("Both flows play from the same media. Compare them:")
	logf("  ./scripts/demo.sh play %s --open", worldID)
	logf("  ./scripts/demo.sh play %s --open", regionalID)
}

// report prints what a client learns from one segment query. An empty
// result is the interesting case: no object_id, and so no presigned URL.
func report(segs []segment) {
	if len(segs) == 0 {
		logf("  → [] — no segment, no object_id, no URL to fetch")
		logf("")
		return
	}
	for _, s := range segs {
		urlState := "no get_urls returned"
		if len(s.GetURLs) > 0 {
			urlState = "presigned URL returned (" + strconv.Itoa(len(s.GetURLs)) + ")"
		}
		logf("  → object_id=%s  %s", s.ObjectID, urlState)
	}
	logf("")
}

// --- TAMS timerange ----------------------------------------------------------

// parseRange parses the closed-open bracket form the server emits,
// "[<sec>:<ns>_<sec>:<ns>)", into nanosecond bounds. The example only
// needs this one shape; docs/conformance.md documents the full grammar.
func parseRange(s string) (lo, hi int64, err error) {
	body, ok := strings.CutPrefix(s, "[")
	if !ok {
		return 0, 0, fmt.Errorf("want a closed-open range starting with '['")
	}
	body, ok = strings.CutSuffix(body, ")")
	if !ok {
		return 0, 0, fmt.Errorf("want a closed-open range ending with ')'")
	}
	loStr, hiStr, ok := strings.Cut(body, "_")
	if !ok {
		return 0, 0, fmt.Errorf("want two bounds separated by '_'")
	}
	if lo, err = parseTS(loStr); err != nil {
		return 0, 0, fmt.Errorf("start bound: %w", err)
	}
	if hi, err = parseTS(hiStr); err != nil {
		return 0, 0, fmt.Errorf("end bound: %w", err)
	}
	if hi <= lo {
		return 0, 0, fmt.Errorf("end bound is not after the start bound")
	}
	return lo, hi, nil
}

func parseTS(s string) (int64, error) {
	secStr, nsStr, ok := strings.Cut(s, ":")
	if !ok {
		return 0, fmt.Errorf("want <seconds>:<nanoseconds>, got %q", s)
	}
	sec, err := strconv.ParseInt(secStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("seconds: %w", err)
	}
	ns, err := strconv.ParseInt(nsStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("nanoseconds: %w", err)
	}
	return sec*int64(time.Second) + ns, nil
}

// checkAligned rejects a window that cuts through a segment. TAMS
// segments reference whole immutable Media Objects, so a partial
// withholding would still hand the client the object that contains the
// restricted frames.
func checkAligned(segs []segment, lo, hi int64) error {
	for _, s := range segs {
		segLo, segHi, err := parseRange(s.Timerange)
		if err != nil {
			return fmt.Errorf("parse world segment %q: %w", s.Timerange, err)
		}
		if (segLo < lo && segHi > lo) || (segLo < hi && segHi > hi) {
			var b strings.Builder
			b.WriteString("BLACKOUT window cuts through a segment.\n")
			b.WriteString("TAMS segments reference whole immutable objects, so the window must\n")
			b.WriteString("begin and end on one of these boundaries:\n")
			for _, x := range segs {
				b.WriteString("  " + x.Timerange + "\n")
			}
			return fmt.Errorf("%s", b.String())
		}
	}
	return nil
}

// --- HTTP plumbing -----------------------------------------------------------

type client struct {
	base  string
	token string
	http  *http.Client
}

// listSegments pages through every segment matching timerange (pass ""
// for all). OpenTAMS returns the next cursor in X-Paging-NextKey; it
// goes back out as the `key` query parameter, and its absence ends the
// walk. See docs/conformance.md, "Pagination cursor format".
func (c *client) listSegments(ctx context.Context, flowID, timerange string) ([]segment, error) {
	var all []segment
	cursor := ""
	for {
		q := url.Values{}
		if timerange != "" {
			q.Set("timerange", timerange)
		}
		if cursor != "" {
			q.Set("key", cursor)
		}
		path := "/tams/v1/flows/" + flowID + "/segments"
		if enc := q.Encode(); enc != "" {
			path += "?" + enc
		}

		var page []segment
		hdr, err := c.doWithHeaders(ctx, http.MethodGet, path, nil, &page)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if cursor = hdr.Get("X-Paging-NextKey"); cursor == "" {
			return all, nil
		}
	}
}

type reqOpt func(*http.Request)

func withHeader(k, v string) reqOpt {
	return func(r *http.Request) { r.Header.Set(k, v) }
}

func (c *client) do(ctx context.Context, method, path string, body, out any, opts ...reqOpt) error {
	_, err := c.doWithHeaders(ctx, method, path, body, out, opts...)
	return err
}

// doWithHeaders issues a JSON request and decodes the response (if `out`
// is non-nil), returning the response headers so callers can read the
// pagination cursor. Any non-2xx is returned as an error including the
// body, which for OpenTAMS is an RFC 9457 application/problem+json
// document.
func (c *client) doWithHeaders(ctx context.Context, method, path string, body, out any, opts ...reqOpt) (http.Header, error) {
	var bodyReader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(buf)
	}
	//nolint:gosec // example program; the base URL comes from the operator running the example, not from user input
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, o := range opts {
		o(req)
	}

	resp, err := c.http.Do(req) //nolint:gosec // as above: operator-supplied base URL, not user-tainted
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s %s → %d: %s", method, path, resp.StatusCode, string(respBody))
	}
	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return nil, fmt.Errorf("decode response: %w", err)
		}
	}
	return resp.Header, nil
}

// --- helpers -----------------------------------------------------------------

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func labelOf(flow map[string]any) string {
	if s, ok := flow["label"].(string); ok && s != "" {
		return s
	}
	return "flow"
}

func short(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8] + "…"
}

// newUUID returns a v4-shaped lowercase hex string. The example uses
// only the standard library, so google/uuid is not imported.
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		fail("rand: %v", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
