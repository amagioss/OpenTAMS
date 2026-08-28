package main

import "testing"

// isFetchableURL is the only thing standing between a hostile service
// instance and an outbound request from the operator's machine, so it gets a
// test even though the sibling demo tools carry none.
func Test_isFetchableURL(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"https", "https://media.example.com/seg/1.ts", true},
		{"http", "http://media.example.com/seg/1.ts", true},
		{"http with port", "http://media.example.com:8080/seg/1.ts", true},
		{"file", "file:///etc/passwd", false},
		{"gopher", "gopher://example.com/1", false},
		{"data", "data:text/plain;base64,aGk=", false},
		{"s3", "s3://bucket/key", false},
		{"scheme relative", "//example.com/seg/1.ts", false},
		{"relative", "/seg/1.ts", false},
		{"no host", "http:///seg/1.ts", false},
		{"empty", "", false},
		// RFC 3986 §3.1: schemes are case-insensitive, and url.Parse
		// normalises them, so this is accepted by design.
		{"uppercase scheme", "HTTP://example.com/x", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isFetchableURL(tc.raw); got != tc.want {
				t.Errorf("isFetchableURL(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}
