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

func TestShouldBypassCacheForContent(t *testing.T) {
	cases := []struct {
		name    string
		content string
		bypass  bool
	}{
		{"iubenda activate", `<iframe class="_iub_cs_activate" src="about:blank"></iframe>`, true},
		{"cmp lazyload", `<iframe class="cmplazyload" src="about:blank"></iframe>`, true},
		{"suppressed src", `<iframe data-suppressedsrc="https://maps.google.com"></iframe>`, true},
		{
			"real-world suppressed maps iframe",
			`<iframe src="about:blank" loading="lazy" class="_iub_cs_activate cmplazyload" ` +
				`data-suppressedsrc="https://www.google.com/maps/embed?pb=x" data-cmp-vendor="178"></iframe>`,
			true,
		},
		// Broad consent-banner signals that load site-wide must NOT bypass the
		// cache, otherwise caching is disabled for every page on the site.
		{"cookiebot banner", `<script src="https://consent.cookiebot.com/uc.js"></script>`, false},
		{"onetrust banner", `<script src="https://cdn.onetrust.com/OneTrust.js"></script>`, false},
		{"generic cookieconsent", `<div class="CookieConsent"></div>`, false},
		{"normal youtube embed", `<iframe src="https://www.youtube.com/embed/test"></iframe>`, false},
		{"plain html", `<html><body><h1>Hello</h1></body></html>`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldBypassCacheForContent([]byte(tc.content)); got != tc.bypass {
				t.Fatalf("shouldBypassCacheForContent(%q) = %v, want %v", tc.name, got, tc.bypass)
			}
		})
	}
}
