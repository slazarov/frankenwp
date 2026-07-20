package cache

import (
	"net/http"
	"testing"
)

func TestResponseForbidsCache(t *testing.T) {
	cases := []struct {
		name   string
		hdr    http.Header
		forbid bool
	}{
		{"plain", http.Header{"Content-Type": {"text/html"}}, false},
		{"set-cookie", http.Header{"Set-Cookie": {"a=b"}}, true},
		{"cc-private", http.Header{"Cache-Control": {"private"}}, true},
		{"cc-no-store", http.Header{"Cache-Control": {"no-store"}}, true},
		{"cc-no-cache", http.Header{"Cache-Control": {"public, no-cache"}}, true},
		{"cc-max-age-0", http.Header{"Cache-Control": {"max-age=0"}}, true},
		{"cc-public", http.Header{"Cache-Control": {"public, max-age=600"}}, false},
		{"vary-cookie", http.Header{"Vary": {"Accept-Encoding, Cookie"}}, true},
		{"vary-star", http.Header{"Vary": {"*"}}, true},
		{"vary-ae", http.Header{"Vary": {"Accept-Encoding"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := responseForbidsCache(tc.hdr)
			if got != tc.forbid {
				t.Fatalf("forbid = %v (%q), want %v", got, reason, tc.forbid)
			}
		})
	}
}

func TestCacheableStatus(t *testing.T) {
	cw := &CustomWriter{cacheResponseCodes: []string{"200", "404", "5"}}
	cases := []struct {
		status int
		want   bool
	}{
		{200, true},
		{404, true},
		{301, false},
		{503, false}, // 5xx wildcard must never cache
		{500, false},
	}
	for _, tc := range cases {
		if got := cw.cacheableStatus(tc.status); got != tc.want {
			t.Fatalf("cacheableStatus(%d) = %v, want %v", tc.status, got, tc.want)
		}
	}

	// Exact 3-digit opt-in allows caching a specific 5xx.
	cw2 := &CustomWriter{cacheResponseCodes: []string{"200", "503"}}
	if !cw2.cacheableStatus(503) {
		t.Fatalf("exact 503 should be cacheable")
	}
	if cw2.cacheableStatus(500) {
		t.Fatalf("500 should not be cacheable when only 503 is listed")
	}
}
