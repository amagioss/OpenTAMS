package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
)

type capturedReq struct {
	method string
	path   string
	query  string
	auth   string
	idem   string
	body   []byte
}

func newCapturingServer(t *testing.T, status int) (*httptest.Server, *capturedReq) {
	t.Helper()
	var got capturedReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method = r.Method
		got.path = r.URL.Path
		got.query = r.URL.RawQuery
		got.auth = r.Header.Get("Authorization")
		got.idem = r.Header.Get("X-Idempotency-Key")
		got.body, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func TestNewClientTunesConnectionPool(t *testing.T) {
	// A load generator hammering one host must reuse keep-alive
	// connections, not pay TCP/TLS setup per request. The transport's
	// idle/total per-host pools must scale with the in-flight bound.
	c := newClient("http://x", "t", time.Second, 128)
	tr, ok := c.hc.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T, want *http.Transport", c.hc.Transport)
	}
	if tr.MaxIdleConnsPerHost < 128 {
		t.Errorf("MaxIdleConnsPerHost = %d, want >= 128", tr.MaxIdleConnsPerHost)
	}
	if tr.MaxConnsPerHost != 128 {
		t.Errorf("MaxConnsPerHost = %d, want 128", tr.MaxConnsPerHost)
	}
}

func TestClientListSegments(t *testing.T) {
	srv, got := newCapturingServer(t, http.StatusOK)
	c := newClient(srv.URL, "dummy-tok", 5*time.Second, 16)
	fid := uuid.New()

	status, err := c.listSegments(context.Background(), fid, "[0:0_600:0)", 100)
	if err != nil {
		t.Fatalf("listSegments: %v", err)
	}
	if status != http.StatusOK {
		t.Errorf("status = %d, want 200", status)
	}
	if got.method != http.MethodGet {
		t.Errorf("method = %s, want GET", got.method)
	}
	if want := "/tams/v1/flows/" + fid.String() + "/segments"; got.path != want {
		t.Errorf("path = %s, want %s", got.path, want)
	}
	if got.auth != "Bearer dummy-tok" {
		t.Errorf("auth = %q, want Bearer dummy-tok", got.auth)
	}
	// timerange + limit must be in the query, properly encoded.
	if got.query == "" || !contains(got.query, "timerange=") || !contains(got.query, "limit=100") {
		t.Errorf("query missing timerange/limit: %q", got.query)
	}
}

func TestClientRegisterSegmentsSendsIdempotencyAndArrayBody(t *testing.T) {
	srv, got := newCapturingServer(t, http.StatusCreated)
	c := newClient(srv.URL, "tok", 5*time.Second, 16)
	fid := uuid.New()

	segs := []segmentPost{
		{ObjectID: "o1", Timerange: "[0:0_6:0)", TSOffset: "0:0"},
		{ObjectID: "o2", Timerange: "[6:0_12:0)", TSOffset: "0:0"},
	}
	res, err := c.registerSegments(context.Background(), fid, segs, "idem-key-123")
	if err != nil {
		t.Fatalf("registerSegments: %v", err)
	}
	if res.status != http.StatusCreated {
		t.Errorf("status = %d, want 201", res.status)
	}
	if got.method != http.MethodPost {
		t.Errorf("method = %s, want POST", got.method)
	}
	if got.idem != "idem-key-123" {
		t.Errorf("X-Idempotency-Key = %q, want idem-key-123", got.idem)
	}
	// Body must be a JSON array of segment objects.
	var arr []map[string]any
	if err := json.Unmarshal(got.body, &arr); err != nil {
		t.Fatalf("body not a JSON array: %v (%s)", err, got.body)
	}
	if len(arr) != 2 || arr[0]["object_id"] != "o1" {
		t.Errorf("body wrong: %s", got.body)
	}
}

func TestClientDeleteSegments(t *testing.T) {
	srv, got := newCapturingServer(t, http.StatusNoContent)
	c := newClient(srv.URL, "tok", 5*time.Second, 16)
	fid := uuid.New()

	status, err := c.deleteSegments(context.Background(), fid, "[0:0_60:0)", "")
	if err != nil {
		t.Fatalf("deleteSegments: %v", err)
	}
	if status != http.StatusNoContent {
		t.Errorf("status = %d, want 204", status)
	}
	if got.method != http.MethodDelete {
		t.Errorf("method = %s, want DELETE", got.method)
	}
	if !contains(got.query, "timerange=") {
		t.Errorf("delete query missing timerange: %q", got.query)
	}
}

func TestClientAllocateStorage(t *testing.T) {
	srv, got := newCapturingServer(t, http.StatusCreated)
	c := newClient(srv.URL, "tok", 5*time.Second, 16)
	fid := uuid.New()

	status, err := c.allocateStorage(context.Background(), fid, 10)
	if err != nil {
		t.Fatalf("allocateStorage: %v", err)
	}
	if status != http.StatusCreated {
		t.Errorf("status = %d, want 201", status)
	}
	if want := "/tams/v1/flows/" + fid.String() + "/storage"; got.path != want {
		t.Errorf("path = %s, want %s", got.path, want)
	}
	var body map[string]any
	if err := json.Unmarshal(got.body, &body); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if body["limit"].(float64) != 10 {
		t.Errorf("limit = %v, want 10", body["limit"])
	}
}

func TestClientAllocateObjectIDsSendsObjectIDsMode(t *testing.T) {
	srv, got := newCapturingServer(t, http.StatusCreated)
	c := newClient(srv.URL, "tok", 5*time.Second, 16)
	fid := uuid.New()

	status, err := c.allocateObjectIDs(context.Background(), fid, []string{"o1", "o2"})
	if err != nil {
		t.Fatalf("allocateObjectIDs: %v", err)
	}
	if status != http.StatusCreated {
		t.Errorf("status = %d, want 201", status)
	}
	if got.method != http.MethodPost {
		t.Errorf("method = %s, want POST", got.method)
	}
	if want := "/tams/v1/flows/" + fid.String() + "/storage"; got.path != want {
		t.Errorf("path = %s, want %s", got.path, want)
	}
	// Exactly one of limit / object_ids may be present.
	var body map[string]json.RawMessage
	if err := json.Unmarshal(got.body, &body); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if _, has := body["limit"]; has {
		t.Errorf("object_ids mode body carries limit: %s", got.body)
	}
	var ids []string
	if err := json.Unmarshal(body["object_ids"], &ids); err != nil {
		t.Fatalf("object_ids not a string array: %v (%s)", err, got.body)
	}
	if len(ids) != 2 || ids[0] != "o1" || ids[1] != "o2" {
		t.Errorf("object_ids = %v, want [o1 o2]", ids)
	}
}

func TestClientRegisterSegmentsReportsFailedSegments(t *testing.T) {
	const failedBody = `{"failed_segments":[{"object_id":"o1","timerange":"[0:0_6:0)",` +
		`"error":{"type":"https://github.com/amagioss/opentams/problems/object-not-allocated","title":"Object Not Allocated"}}]}`
	cases := []struct {
		name       string
		status     int
		body       string
		wantFailed int
	}{
		{"all accepted", http.StatusCreated, "", 0},
		{"partial failure", http.StatusOK, failedBody, 1},
		{"empty failed list", http.StatusOK, `{"failed_segments":[]}`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.body != "" {
					w.Header().Set("Content-Type", "application/json")
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			t.Cleanup(srv.Close)
			c := newClient(srv.URL, "tok", 5*time.Second, 16)

			res, err := c.registerSegments(context.Background(), uuid.New(),
				[]segmentPost{{ObjectID: "o1", Timerange: "[0:0_6:0)"}}, "k")
			if err != nil {
				t.Fatalf("registerSegments: %v", err)
			}
			if res.status != tc.status {
				t.Errorf("status = %d, want %d", res.status, tc.status)
			}
			if res.failedSegments != tc.wantFailed {
				t.Errorf("failedSegments = %d, want %d", res.failedSegments, tc.wantFailed)
			}
		})
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
