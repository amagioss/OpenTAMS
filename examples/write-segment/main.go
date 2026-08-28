// Example: create a Source, create a Flow, allocate storage on the Flow,
// then register one Segment against it.
//
// One end-to-end TAMS write conversation, single file, stdlib only — no
// generated client, no SDK, so the wire protocol is obvious. Production
// code should prefer a typed client built from the OpenAPI spec
// (see ../README.md).
//
// Run against a local stack:
//
//	docker compose -f deployments/docker/docker-compose.yml up --build
//	go run ./examples/write-segment
//
// Configurable via environment variables:
//
//	OPENTAMS_BASE_URL  default http://localhost:8080
//	OPENTAMS_TOKEN     default "dev" (works against the local stack only)
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
	"os"
	"time"
)

func main() {
	baseURL := envOr("OPENTAMS_BASE_URL", "http://localhost:8080")
	token := envOr("OPENTAMS_TOKEN", "dev")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c := &client{base: baseURL, token: token, http: &http.Client{Timeout: 10 * time.Second}}

	sourceID := newUUID()
	flowID := newUUID()
	objectID := newUUID()
	idemKey := "example-write-segment-" + newUUID()

	logf("source_id   = %s", sourceID)
	logf("flow_id     = %s", flowID)
	logf("object_id   = %s", objectID)
	logf("idem_key    = %s", idemKey)

	// 1. Create the Source. In TAMS, a Source is the abstract origin of
	//    media (a camera, a file, a synthesised stream) — independent of
	//    any specific encoding or container.
	logf("→ POST /tams/v1/sources")
	if err := c.do(ctx, http.MethodPost, "/tams/v1/sources", map[string]any{
		"id":     sourceID,
		"format": "urn:x-nmos:format:data",
		"label":  "example-source",
		"tags":   map[string]string{},
	}, nil); err != nil {
		fail("create source: %v", err)
	}

	// 2. Create the Flow. A Flow is one specific encoding of a Source.
	//    Multiple Flows can share a Source (e.g. a 1080p video Flow and
	//    a 720p video Flow that both render the same camera).
	logf("→ PUT /tams/v1/flows/%s", flowID)
	if err := c.do(ctx, http.MethodPut, "/tams/v1/flows/"+flowID, map[string]any{
		"id":        flowID,
		"source_id": sourceID,
		"format":    "urn:x-nmos:format:data",
		"codec":     "application/json",
		"label":     "example-flow",
		"essence_parameters": map[string]any{
			"data_type": "urn:x-tams:data:test",
		},
	}, nil); err != nil {
		fail("create flow: %v", err)
	}

	// 3. Allocate storage. We ask for a presigned PUT URL for one object
	//    using `limit` mode — the server picks the object_id. The
	//    alternative `object_ids` mode lets the client supply IDs it
	//    has already chosen; the server re-presigns each call but
	//    rejects (409) any object_id already attached to a registered
	//    segment, so it's safe to retry on transient failures up to
	//    the point of segment registration.
	logf("→ POST /tams/v1/flows/%s/storage", flowID)
	var alloc struct {
		Media []struct {
			ObjectID string `json:"object_id"`
			PutURL   struct {
				URL string `json:"url"`
			} `json:"put_url"`
		} `json:"media"`
	}
	if err := c.do(ctx, http.MethodPost, "/tams/v1/flows/"+flowID+"/storage",
		map[string]any{"limit": 1}, &alloc); err != nil {
		fail("allocate storage: %v", err)
	}
	if len(alloc.Media) == 0 {
		fail("storage allocation returned no media entries")
	}
	allocatedID := alloc.Media[0].ObjectID
	logf("  allocated object_id=%s (presigned PUT URL omitted)", allocatedID)

	// In a real workflow you'd PUT your media bytes to alloc.Media[0].PutURL.URL
	// here. We skip that step — this example is about the metadata API,
	// not S3 mechanics.

	// 4. Register a Segment claiming a time-range on the Flow that points
	//    at the allocated object. The X-Idempotency-Key header is required
	//    by REQ-IDEM-01 — POSTing the same key twice returns the cached
	//    response rather than creating a duplicate segment.
	logf("→ POST /tams/v1/flows/%s/segments  (X-Idempotency-Key=%s)", flowID, idemKey)
	if err := c.do(ctx, http.MethodPost, "/tams/v1/flows/"+flowID+"/segments",
		[]map[string]any{{
			"object_id": allocatedID,
			"timerange": "[0:0_10:0)",
		}}, nil, withHeader("X-Idempotency-Key", idemKey)); err != nil {
		fail("register segment: %v", err)
	}

	logf("done.")
	logf("")
	logf("To list it back:")
	logf("  FLOW_ID=%s go run ./examples/read-segments", flowID)
}

// --- HTTP plumbing -----------------------------------------------------------

type client struct {
	base  string
	token string
	http  *http.Client
}

type reqOpt func(*http.Request)

func withHeader(k, v string) reqOpt {
	return func(r *http.Request) { r.Header.Set(k, v) }
}

// do issues a JSON request and decodes the response (if `out` is non-nil).
// Any non-2xx is returned as an error including the response body, which
// for OpenTAMS will be a RFC 9457 `application/problem+json` document.
func (c *client) do(ctx context.Context, method, path string, body, out any, opts ...reqOpt) error {
	var bodyReader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, o := range opts {
		o(req)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s → %d: %s", method, path, resp.StatusCode, string(respBody))
	}
	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// --- helpers -----------------------------------------------------------------

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// newUUID returns a v4-shaped lowercase hex string. We don't import
// google/uuid here because the example deliberately uses only stdlib.
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
