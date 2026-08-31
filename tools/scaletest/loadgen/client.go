package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// Client is a thin TAMS HTTP client for the load generator. It builds
// requests against the generated routes by hand (there is no generated
// client) using minimal payload structs, and reports only the HTTP
// status + transport error — the engine times each call. All requests
// carry a bearer token (a dummy value is fine against the dev-auth
// provider; see docs/scale-test-plan.md §4).
type Client struct {
	base  string
	token string
	hc    *http.Client
}

// newClient builds the client with a connection pool sized to maxConns
// (the engine's --max-inflight). The default http.Transport keeps only
// 2 idle connections per host, which under high RPS forces a fresh
// TCP/TLS handshake per request — the generator would then measure its
// own connection-setup cost instead of server latency. Sizing the
// idle/total per-host pools to the in-flight bound keeps connections
// reused and bounded.
func newClient(baseURL, token string, timeout time.Duration, maxConns int) *Client {
	maxConns = max(maxConns, 2)
	tr := &http.Transport{
		MaxIdleConns:        maxConns,
		MaxIdleConnsPerHost: maxConns,
		MaxConnsPerHost:     maxConns,
		IdleConnTimeout:     90 * time.Second,
	}
	return &Client{
		base:  baseURL,
		token: token,
		hc:    &http.Client{Timeout: timeout, Transport: tr},
	}
}

// segmentPost is the POST /segments array element. Empty get_urls ⇒ the
// segment is "controlled" and the server stamps storage itself.
type segmentPost struct {
	ObjectID  string `json:"object_id"`
	Timerange string `json:"timerange"`
	TSOffset  string `json:"ts_offset,omitempty"`
}

// storagePost is the POST /storage body; Limit allocates that many
// server-generated object ids.
type storagePost struct {
	Limit int `json:"limit,omitempty"`
}

func (c *Client) segmentsPath(flowID uuid.UUID) string {
	return fmt.Sprintf("%s/tams/v1/flows/%s/segments", c.base, flowID)
}

func (c *Client) storagePath(flowID uuid.UUID) string {
	return fmt.Sprintf("%s/tams/v1/flows/%s/storage", c.base, flowID)
}

// do issues the request, sets auth, drains+closes the body, and returns
// the status code. The body is drained so the connection can be reused
// (keep-alive) under high RPS.
func (c *Client) do(req *http.Request) (int, error) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close() //nolint:errcheck // body fully drained below; close error is uninteresting.
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

func (c *Client) listSegments(ctx context.Context, flowID uuid.UUID, timerange string, limit int) (int, error) {
	q := url.Values{}
	if timerange != "" {
		q.Set("timerange", timerange)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	u := c.segmentsPath(flowID)
	if enc := q.Encode(); enc != "" {
		u += "?" + enc
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	return c.do(req)
}

func (c *Client) registerSegments(ctx context.Context, flowID uuid.UUID, segs []segmentPost, idemKey string) (int, error) {
	body, err := json.Marshal(segs)
	if err != nil {
		return 0, fmt.Errorf("marshal segments: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.segmentsPath(flowID), bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Idempotency-Key", idemKey)
	return c.do(req)
}

func (c *Client) deleteSegments(ctx context.Context, flowID uuid.UUID, timerange, objectID string) (int, error) {
	q := url.Values{}
	if timerange != "" {
		q.Set("timerange", timerange)
	}
	if objectID != "" {
		q.Set("object_id", objectID)
	}
	u := c.segmentsPath(flowID)
	if enc := q.Encode(); enc != "" {
		u += "?" + enc
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return 0, err
	}
	return c.do(req)
}

func (c *Client) allocateStorage(ctx context.Context, flowID uuid.UUID, limit int) (int, error) {
	body, err := json.Marshal(storagePost{Limit: limit})
	if err != nil {
		return 0, fmt.Errorf("marshal storage: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.storagePath(flowID), bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req)
}
