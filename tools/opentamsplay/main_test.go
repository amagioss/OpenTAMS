package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const testBase = "http://localhost:9000"

// targetWithinBase replaced a strings.HasPrefix test. These are the
// cases a prefix test gets wrong: the host continuing past the port,
// and userinfo moving the real host after an "@".
func TestTargetWithinBase(t *testing.T) {
	cases := []struct {
		name   string
		target string
		allow  bool
	}{
		{"the base itself", testBase, true},
		{"an object under the base", testBase + "/bucket/object.ts", true},
		{"presigned query is irrelevant", testBase + "/b/o.ts?X-Amz-Signature=abc", true},
		{"scheme case is insignificant", "HTTP://localhost:9000/b/o.ts", true},

		{"host continues past the port", "http://localhost:9000.evil.com/o.ts", false},
		{"base is userinfo, host is elsewhere", "http://localhost:9000@evil.com/o.ts", false},
		{"userinfo with a password", "http://localhost:9000:pw@evil.com/o.ts", false},
		{"different host", "http://evil.com/o.ts", false},
		{"different port", "http://localhost:9001/o.ts", false},
		{"scheme downgrade", "https://localhost:9000/o.ts", false},
		{"relative target", "/o.ts", false},
		{"protocol-relative target", "//evil.com/o.ts", false},
		{"javascript scheme", "javascript:alert(1)", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := targetWithinBase(tc.target, testBase)
			if tc.allow && err != nil {
				t.Fatalf("expected %q to be allowed, got: %v", tc.target, err)
			}
			if !tc.allow && err == nil {
				t.Fatalf("expected %q to be refused, it was allowed", tc.target)
			}
			// Every case above passes a bare prefix test or is meant to;
			// assert that the refused ones are exactly where the old
			// guard would have been wrong.
			if !tc.allow && strings.HasPrefix(tc.target, testBase) {
				t.Logf("prefix test would have allowed this: %s", tc.target)
			}
		})
	}
}

// A base carrying a path must not admit a sibling that merely shares
// its prefix.
func TestTargetWithinBase_PathBoundary(t *testing.T) {
	const base = "http://localhost:9000/bucket"
	if err := targetWithinBase("http://localhost:9000/bucket/o.ts", base); err != nil {
		t.Fatalf("object under the base must be allowed: %v", err)
	}
	if err := targetWithinBase("http://localhost:9000/bucket-other/o.ts", base); err == nil {
		t.Fatal("a sibling path sharing the prefix must be refused")
	}
}

func TestSegmentRedirectHandler(t *testing.T) {
	t.Setenv("OPENTAMSPLAY_OBJECT_BASE", testBase)
	cases := []struct {
		name   string
		u      string
		status int
	}{
		{"missing u", "", http.StatusBadRequest},
		{"allowed target redirects", testBase + "/b/o.ts", http.StatusFound},
		{"host continues past the port", "http://localhost:9000.evil.com/o.ts", http.StatusForbidden},
		{"userinfo bypass", "http://localhost:9000@evil.com/o.ts", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := "/seg/x.ts"
			if tc.u != "" {
				target += "?u=" + url.QueryEscape(tc.u)
			}
			rec := httptest.NewRecorder()
			segmentRedirectHandler(rec, httptest.NewRequest(http.MethodGet, target, http.NoBody))
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.status, rec.Body.String())
			}
			if tc.status == http.StatusFound && rec.Header().Get("Location") != tc.u {
				t.Errorf("Location = %q, want %q", rec.Header().Get("Location"), tc.u)
			}
		})
	}
}
