// Package opentamsclient is the demo-tools client wrapper around the
// OpenTAMS HTTP API. Shared by the opentamspub / opentamsedit /
// opentamsplay binaries under tools/. Not part of the server build.
package opentamsclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"

	api "github.com/amagioss/opentams/gen/api"
)

const (
	defaultBaseURL  = "http://localhost:8080"
	defaultToken    = "dev"
	defaultTimeout  = 30 * time.Second
	defaultPageSize = 250
)

// Client is a thin wrapper around the OpenTAMS HTTP API for the demo
// tools. It is not safe for concurrent use of the same idempotency-key
// generator from multiple goroutines, but the underlying http.Client is.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// New returns a client configured from base / token, falling back to the
// dev defaults (localhost:8080, Bearer dev) when either is empty.
func New(base, token string) *Client {
	if base == "" {
		base = defaultBaseURL
	}
	if token == "" {
		token = defaultToken
	}
	return &Client{
		BaseURL: base,
		Token:   token,
		HTTP:    &http.Client{Timeout: defaultTimeout},
	}
}

// NewIdempotencyKey returns a fresh UUIDv4 string usable as an
// X-Idempotency-Key header value. POST /flows/{id}/segments requires
// one per request per REQ-IDEM-01.
func NewIdempotencyKey() string { return uuid.NewString() }

// HTTPError is returned when the server replies with a non-2xx status.
// Body contains the raw response payload — for OpenTAMS errors this is
// an RFC 9457 application/problem+json document.
type HTTPError struct {
	Method     string
	URL        string
	StatusCode int
	Body       []byte
}

func (e *HTTPError) Error() string {
	body := string(e.Body)
	const maxBody = 512
	if len(body) > maxBody {
		body = body[:maxBody] + "…"
	}
	return fmt.Sprintf("%s %s → %d: %s", e.Method, e.URL, e.StatusCode, body)
}

// IsConflict reports whether err wraps an HTTPError with a 409 status.
// Useful for idempotency: a concurrent client that already registered
// the same segment returns 409 here.
func IsConflict(err error) bool {
	if herr, ok := errors.AsType[*HTTPError](err); ok {
		return herr.StatusCode == http.StatusConflict
	}
	return false
}

// do is the single JSON request primitive. It marshals body (if non-nil)
// and decodes into out (if non-nil). Extra headers are applied last so
// callers can override Content-Type when needed. The response body is
// fully read and closed before do returns; the returned http.Header is
// a snapshot of the response headers (e.g. X-Paging-NextKey).
func (c *Client) do(ctx context.Context, method, path string, body, out any, extraHeaders map[string]string) (http.Header, error) {
	var bodyReader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(resp.Body)
	headers := resp.Header.Clone()
	if resp.StatusCode >= 300 {
		return headers, &HTTPError{Method: method, URL: c.BaseURL + path, StatusCode: resp.StatusCode, Body: respBody}
	}
	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return headers, fmt.Errorf("decode response: %w", err)
		}
	}
	return headers, nil
}

// PutFlow creates or replaces a Flow at the given id. The body is the
// caller-built Flow union — pass api.Flow with the appropriate variant.
func (c *Client) PutFlow(ctx context.Context, flowID string, flow any) error {
	_, err := c.do(ctx, http.MethodPut, "/tams/v1/flows/"+flowID, flow, nil, nil)
	return err
}

// GetFlow fetches a Flow by id. The response is decoded into the
// generic Flow union which carries every essence variant.
func (c *Client) GetFlow(ctx context.Context, flowID string) (*api.Flow, error) {
	var f api.Flow
	if _, err := c.do(ctx, http.MethodGet, "/tams/v1/flows/"+flowID, nil, &f, nil); err != nil {
		return nil, err
	}
	return &f, nil
}

// AllocatedObject is one slot returned by POST /flows/{id}/storage.
// We flatten the inline anonymous struct from api.FlowStorage so callers
// don't have to drill through the optional pointer chain.
type AllocatedObject struct {
	ObjectID string
	PutURL   string
	Headers  map[string]string
}

// AllocateStorage requests N presigned PUT URLs in a single round-trip
// (`limit` mode). The server may cap to its own maximum; the returned
// slice length is what was actually allocated and callers should loop
// if more are needed.
func (c *Client) AllocateStorage(ctx context.Context, flowID string, limit int) ([]AllocatedObject, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("limit must be > 0")
	}
	body := api.FlowStoragePost{Limit: &limit}
	var resp api.FlowStorage
	if _, err := c.do(ctx, http.MethodPost, "/tams/v1/flows/"+flowID+"/storage", body, &resp, nil); err != nil {
		return nil, err
	}
	if resp.MediaObjects == nil {
		return nil, fmt.Errorf("storage response had no media_objects")
	}
	out := make([]AllocatedObject, 0, len(*resp.MediaObjects))
	for _, m := range *resp.MediaObjects {
		ao := AllocatedObject{ObjectID: m.ObjectId, PutURL: m.PutUrl.Url}
		if m.PutUrl.Headers != nil {
			ao.Headers = *m.PutUrl.Headers
		}
		if m.PutUrl.ContentType != nil && (ao.Headers == nil || ao.Headers["Content-Type"] == "") {
			if ao.Headers == nil {
				ao.Headers = map[string]string{}
			}
			ao.Headers["Content-Type"] = *m.PutUrl.ContentType
		}
		out = append(out, ao)
	}
	return out, nil
}

// UploadObject does an HTTP PUT of body to a presigned URL with any
// headers the server told us to set. The PUT URL is the operator-side
// MinIO/S3 endpoint, not a TAMS endpoint, so it carries no bearer auth.
func (c *Client) UploadObject(ctx context.Context, ao AllocatedObject, body io.Reader, size int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, ao.PutURL, body)
	if err != nil {
		return err
	}
	if size > 0 {
		req.ContentLength = size
		req.Header.Set("Content-Length", strconv.FormatInt(size, 10))
	}
	for k, v := range ao.Headers {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("PUT %s: %w", ao.PutURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		buf, _ := io.ReadAll(resp.Body)
		return &HTTPError{Method: http.MethodPut, URL: ao.PutURL, StatusCode: resp.StatusCode, Body: buf}
	}
	return nil
}

// RegisterSegments registers a batch of segments under one idempotency
// key. The server returns 200 with `flow-segment-bulk-failure` if some
// rows failed; we surface the raw HTTPError so callers can inspect.
func (c *Client) RegisterSegments(ctx context.Context, flowID string, segments []api.FlowSegmentPost, idempotencyKey string) error {
	if idempotencyKey == "" {
		idempotencyKey = NewIdempotencyKey()
	}
	headers := map[string]string{"X-Idempotency-Key": idempotencyKey}
	_, err := c.do(ctx, http.MethodPost, "/tams/v1/flows/"+flowID+"/segments", segments, nil, headers)
	return err
}

// ListSegmentsPage fetches one page of segments matching timerange.
// cursor is the previous page's X-Paging-NextKey value, or "" for the
// first page. nextCursor in the return is "" when no more pages remain.
func (c *Client) ListSegmentsPage(ctx context.Context, flowID, timerange, cursor string, limit int) (segments []api.FlowSegment, nextCursor string, err error) {
	q := url.Values{}
	if timerange != "" {
		q.Set("timerange", timerange)
	}
	if cursor != "" {
		q.Set("key", cursor)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	path := "/tams/v1/flows/" + flowID + "/segments"
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}
	headers, err := c.do(ctx, http.MethodGet, path, nil, &segments, nil)
	if err != nil {
		return nil, "", err
	}
	if headers != nil {
		nextCursor = headers.Get("X-Paging-NextKey")
	}
	return segments, nextCursor, nil
}

// ListAllSegments pages through every segment in the given range. The
// HLS gateway calls this on every manifest poll, so callers should set
// a reasonable upper bound on timerange to avoid unbounded scans.
func (c *Client) ListAllSegments(ctx context.Context, flowID, timerange string) ([]api.FlowSegment, error) {
	var all []api.FlowSegment
	cursor := ""
	for {
		page, next, err := c.ListSegmentsPage(ctx, flowID, timerange, cursor, defaultPageSize)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if next == "" {
			return all, nil
		}
		cursor = next
	}
}
