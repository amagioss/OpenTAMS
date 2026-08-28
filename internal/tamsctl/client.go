package tamsctl

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
	"strings"
	"time"

	"github.com/google/uuid"
)

// Internally-generated request headers. These are never taken from user input:
// X-Request-ID correlates logs; X-Idempotency-Key makes POST /segments
// idempotent (required by the API).
const (
	headerRequestID      = "X-Request-ID"
	headerIdempotencyKey = "X-Idempotency-Key"
)

// headerPagingNextKey is the response header carrying the cursor for the next
// page of a paginated listing (empty/absent on the last page).
const headerPagingNextKey = "X-Paging-NextKey"

// Page is a paginated listing response: the raw JSON array of items plus the
// cursor for the next page (empty when there are no more pages).
type Page struct {
	Items     json.RawMessage
	NextToken string
}

// ErrTokenExpired is returned (wrapped) when the configured token's `exp`
// claim is in the past, before any request is sent.
var ErrTokenExpired = errors.New("token expired")

// ErrUnauthorized is returned (wrapped) when the server responds 401.
var ErrUnauthorized = errors.New("unauthorized")

// APIError is a non-2xx response that is not a 401.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	body := strings.TrimSpace(e.Body)
	if body == "" {
		return fmt.Sprintf("server returned %d %s", e.Status, http.StatusText(e.Status))
	}
	return fmt.Sprintf("server returned %d: %s", e.Status, body)
}

// Client is a thin TAMS v1 HTTP client for the operator CLI. It marshals
// minimal request payloads by hand and returns raw JSON response bodies for
// the command layer to format.
type Client struct {
	base  string
	token string
	hc    *http.Client
	now   func() time.Time
}

// NewClient builds a client for baseURL using the given bearer token. baseURL
// is the full TAMS API root including the deployment's version prefix, e.g.
// http://localhost:8080/tams/v1 or https://aws.example/api/v1 — the prefix is
// deployment-specific and not assumed here. Only spec-relative resource paths
// (/flows, /sources, …) are appended.
func NewClient(baseURL, token string) *Client {
	return &Client{
		base:  strings.TrimRight(baseURL, "/"),
		token: token,
		hc:    &http.Client{Timeout: 30 * time.Second},
		now:   time.Now,
	}
}

// FrameRate is a fixed video frame rate (frames per second) as a rational.
type FrameRate struct {
	Numerator   int
	Denominator int
}

// FlowCreateParams is the minimal video-flow create/update payload. A video
// flow needs a frame rate: either FrameRate (fixed) or VFR (variable). The two
// are mutually exclusive.
type FlowCreateParams struct {
	ID          string
	SourceID    string
	Codec       string
	Format      string
	FrameWidth  int
	FrameHeight int
	FrameRate   *FrameRate
	VFR         bool
}

// SegmentRegisterParams mirrors a single POST /segments array element.
type SegmentRegisterParams struct {
	ObjectID        string   `json:"object_id"`
	Timerange       string   `json:"timerange,omitempty"`
	TSOffset        string   `json:"ts_offset,omitempty"`
	ObjectTimerange string   `json:"object_timerange,omitempty"`
	LastDuration    string   `json:"last_duration,omitempty"`
	KeyFrameCount   *int     `json:"key_frame_count,omitempty"`
	SampleOffset    *int     `json:"sample_offset,omitempty"`
	SampleCount     *int     `json:"sample_count,omitempty"`
	GetURLs         []string `json:"-"`
}

// SegmentGetParams holds GET /segments query options.
type SegmentGetParams struct {
	Timerange              string
	ObjectID               string
	ReverseOrder           bool
	VerboseStorage         bool
	AcceptGetURLs          string
	AcceptStorageIDs       string
	Presigned              bool
	IncludeObjectTimerange bool
	Limit                  int
	PageToken              string
}

// SourceGetParams holds GET /sources query options. Tags maps a tag name to
// the value it must equal (rendered as tag.{name}={value}); TagsExist lists
// tag names that must be present (rendered as tag_exists.{name}=true).
type SourceGetParams struct {
	Label     string
	Format    string
	Tags      map[string]string
	TagsExist []string
	Limit     int
	PageToken string
}

func (c *Client) flowPath(flowID string) string {
	return "/flows/" + flowID
}

// CreateFlow PUTs a minimal video flow at /flows/{id}.
func (c *Client) CreateFlow(ctx context.Context, p FlowCreateParams) (json.RawMessage, error) {
	essence := map[string]any{
		"frame_width":  p.FrameWidth,
		"frame_height": p.FrameHeight,
	}
	switch {
	case p.VFR:
		essence["vfr"] = true
	case p.FrameRate != nil:
		fr := map[string]any{"numerator": p.FrameRate.Numerator}
		if p.FrameRate.Denominator > 0 {
			fr["denominator"] = p.FrameRate.Denominator
		}
		essence["frame_rate"] = fr
	}
	body := map[string]any{
		"id":                 p.ID,
		"source_id":          p.SourceID,
		"format":             p.Format,
		"codec":              p.Codec,
		"essence_parameters": essence,
	}
	return c.doJSON(ctx, http.MethodPut, c.flowPath(p.ID), nil, body)
}

// GetFlow returns /flows/{id}.
func (c *Client) GetFlow(ctx context.Context, flowID string) (json.RawMessage, error) {
	return c.doJSON(ctx, http.MethodGet, c.flowPath(flowID), nil, nil)
}

// DeleteFlow deletes /flows/{id}.
func (c *Client) DeleteFlow(ctx context.Context, flowID string) (json.RawMessage, error) {
	return c.doJSON(ctx, http.MethodDelete, c.flowPath(flowID), nil, nil)
}

// PutFlowLabel sets /flows/{id}/label.
func (c *Client) PutFlowLabel(ctx context.Context, flowID, label string) (json.RawMessage, error) {
	return c.doJSON(ctx, http.MethodPut, c.flowPath(flowID)+"/label", nil, label)
}

// PutFlowReadOnly sets /flows/{id}/read_only.
func (c *Client) PutFlowReadOnly(ctx context.Context, flowID string, readOnly bool) (json.RawMessage, error) {
	return c.doJSON(ctx, http.MethodPut, c.flowPath(flowID)+"/read_only", nil, readOnly)
}

// PutFlowAvgBitRate sets /flows/{id}/avg_bit_rate.
func (c *Client) PutFlowAvgBitRate(ctx context.Context, flowID string, rate int) (json.RawMessage, error) {
	return c.doJSON(ctx, http.MethodPut, c.flowPath(flowID)+"/avg_bit_rate", nil, rate)
}

// PutFlowTag sets /flows/{id}/tags/{name}.
func (c *Client) PutFlowTag(ctx context.Context, flowID, name, value string) (json.RawMessage, error) {
	return c.doJSON(ctx, http.MethodPut, c.flowPath(flowID)+"/tags/"+name, nil, value)
}

// PutFlowCollection sets /flows/{id}/flow_collection from a raw JSON document.
func (c *Client) PutFlowCollection(ctx context.Context, flowID string, collection json.RawMessage) (json.RawMessage, error) {
	return c.doJSON(ctx, http.MethodPut, c.flowPath(flowID)+"/flow_collection", nil, collection)
}

func (c *Client) segmentsPath(flowID string) string {
	return c.flowPath(flowID) + "/segments"
}

// RegisterSegments POSTs a batch of fully-specified segments.
func (c *Client) RegisterSegments(ctx context.Context, flowID string, segs []SegmentRegisterParams) (json.RawMessage, error) {
	arr := make([]map[string]any, 0, len(segs))
	for i := range segs {
		s := &segs[i]
		m := map[string]any{"object_id": s.ObjectID}
		putIfSet(m, "timerange", s.Timerange)
		putIfSet(m, "ts_offset", s.TSOffset)
		putIfSet(m, "object_timerange", s.ObjectTimerange)
		putIfSet(m, "last_duration", s.LastDuration)
		if s.KeyFrameCount != nil {
			m["key_frame_count"] = *s.KeyFrameCount
		}
		if s.SampleOffset != nil {
			m["sample_offset"] = *s.SampleOffset
		}
		if s.SampleCount != nil {
			m["sample_count"] = *s.SampleCount
		}
		if len(s.GetURLs) > 0 {
			m["get_urls"] = toGetURLs(s.GetURLs)
		}
		arr = append(arr, m)
	}
	respBody, _, err := c.do(ctx, http.MethodPost, c.segmentsPath(flowID), nil, arr, newIdempotencyHeader())
	return respBody, err
}

// newIdempotencyHeader generates a fresh idempotency key for one logical write.
// Each CLI invocation is a single operation, so a new UUID per call is correct.
func newIdempotencyHeader() map[string]string {
	return map[string]string{headerIdempotencyKey: uuid.NewString()}
}

// DeleteSegments DELETEs segments filtered by timerange and/or object_id.
func (c *Client) DeleteSegments(ctx context.Context, flowID, timerange, objectID string) (json.RawMessage, error) {
	q := url.Values{}
	setIf(q, "timerange", timerange)
	setIf(q, "object_id", objectID)
	return c.doJSON(ctx, http.MethodDelete, c.segmentsPath(flowID), q, nil)
}

// ListSegments GETs one page of segments with the given filters.
func (c *Client) ListSegments(ctx context.Context, flowID string, p SegmentGetParams) (*Page, error) {
	q := url.Values{}
	setIf(q, "timerange", p.Timerange)
	setIf(q, "object_id", p.ObjectID)
	setBool(q, "reverse_order", p.ReverseOrder)
	setBool(q, "verbose_storage", p.VerboseStorage)
	setIf(q, "accept_get_urls", p.AcceptGetURLs)
	setIf(q, "accept_storage_ids", p.AcceptStorageIDs)
	setBool(q, "presigned", p.Presigned)
	setBool(q, "include_object_timerange", p.IncludeObjectTimerange)
	setInt(q, "limit", p.Limit)
	setIf(q, "page", p.PageToken)
	return c.doList(ctx, c.segmentsPath(flowID), q)
}

// AllocateStorage POSTs /flows/{id}/storage with exactly one of limit or
// objectIDs populated (the command layer enforces the exclusivity).
func (c *Client) AllocateStorage(ctx context.Context, flowID string, limit int, objectIDs []string) (json.RawMessage, error) {
	body := map[string]any{}
	if len(objectIDs) > 0 {
		body["object_ids"] = objectIDs
	} else {
		body["limit"] = limit
	}
	return c.doJSON(ctx, http.MethodPost, c.flowPath(flowID)+"/storage", nil, body)
}

// ListSources GETs one page of /sources with the given filters.
func (c *Client) ListSources(ctx context.Context, p SourceGetParams) (*Page, error) {
	q := url.Values{}
	setIf(q, "label", p.Label)
	setIf(q, "format", p.Format)
	for name, val := range p.Tags {
		q.Set("tag."+name, val)
	}
	for _, name := range p.TagsExist {
		q.Set("tag_exists."+name, "true")
	}
	setInt(q, "limit", p.Limit)
	setIf(q, "page", p.PageToken)
	return c.doList(ctx, "/sources", q)
}

// doJSON sends a request with only the auto-generated headers (X-Request-ID)
// and returns just the response body.
func (c *Client) doJSON(ctx context.Context, method, path string, query url.Values, body any) (json.RawMessage, error) {
	respBody, _, err := c.do(ctx, method, path, query, body, nil)
	return respBody, err
}

// do sends a request and returns the raw response body and response headers,
// mapping 401 and other non-2xx statuses to typed errors. body may be nil, a
// json.RawMessage, or any json-marshalable value. extraHeaders carries per-call
// headers (e.g. the idempotency key); X-Request-ID is generated internally on
// every request and must not be supplied by callers.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any, extraHeaders map[string]string) (json.RawMessage, http.Header, error) {
	if IsExpired(c.token, c.now()) {
		exp, _ := ParseExpiry(c.token)
		return nil, nil, fmt.Errorf("%w at %s; refresh it with 'tamsctl config set-token' or set %s", ErrTokenExpired, exp.Format(time.RFC3339), EnvToken)
	}

	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, nil, fmt.Errorf("marshal request body: %w", err)
		}
		reader = bytes.NewReader(raw)
	}

	u := c.base + path
	if enc := query.Encode(); enc != "" {
		u += "?" + enc
	}

	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return nil, nil, fmt.Errorf("build request: %w", err)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	// X-Request-ID is client-generated for log correlation; the server echoes
	// it and includes it in error bodies.
	req.Header.Set(headerRequestID, uuid.NewString())
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // body fully read below.

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("read response: %w", err)
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, nil, fmt.Errorf("%w: token rejected by server; refresh it with 'tamsctl config set-token' or set %s", ErrUnauthorized, EnvToken)
	case resp.StatusCode >= 300:
		return nil, nil, &APIError{Status: resp.StatusCode, Body: string(respBody)}
	}
	return respBody, resp.Header, nil
}

// doList issues a GET and returns the body plus the next-page cursor read from
// the X-Paging-NextKey response header.
func (c *Client) doList(ctx context.Context, path string, query url.Values) (*Page, error) {
	body, header, err := c.do(ctx, http.MethodGet, path, query, nil, nil)
	if err != nil {
		return nil, err
	}
	return &Page{Items: body, NextToken: header.Get(headerPagingNextKey)}, nil
}

func toGetURLs(urls []string) []map[string]any {
	out := make([]map[string]any, 0, len(urls))
	for _, u := range urls {
		out = append(out, map[string]any{"url": u})
	}
	return out
}

func putIfSet(m map[string]any, key, val string) {
	if val != "" {
		m[key] = val
	}
}

func setIf(q url.Values, key, val string) {
	if val != "" {
		q.Set(key, val)
	}
}

func setBool(q url.Values, key string, val bool) {
	if val {
		q.Set(key, "true")
	}
}

func setInt(q url.Values, key string, val int) {
	if val > 0 {
		q.Set(key, strconv.Itoa(val))
	}
}
