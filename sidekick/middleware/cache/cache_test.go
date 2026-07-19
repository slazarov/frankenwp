package cache

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"
)

// nextFunc adapts a function to caddyhttp.Handler (the downstream handler).
type nextFunc func(http.ResponseWriter, *http.Request) error

func (f nextFunc) ServeHTTP(w http.ResponseWriter, r *http.Request) error { return f(w, r) }

func newTestCache(t *testing.T, ttl int) *Cache {
	t.Helper()
	dir := t.TempDir()
	logger := zap.NewNop()
	store, err := NewStore(dir, ttl, 128*1024*1024, 1024, logger)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return &Cache{
		logger:              logger,
		Loc:                 dir,
		PurgePath:           "/__cache/purge",
		PurgeKeyHeader:      "X-Purge-Key",
		CacheHeaderName:     "X-Cache",
		CacheResponseCodes:  []string{"200", "404", "405"},
		BypassDebugQuery:    "NOCACHE",
		QueryMode:           "strip",
		TTL:                 ttl,
		MemoryItemMaxSize:   4 * 1024 * 1024,
		MemoryCacheMaxSize:  128 * 1024 * 1024,
		MemoryCacheMaxCount: 1024,
		Store:               store,
		pathRx:              regexp.MustCompile(`.*(\.[^.]+)$`),
		trackingParams:      map[string]struct{}{"fbclid": {}, "gclid": {}},
		preloadSem:          make(chan struct{}, 2),
		preloadGroup:        new(singleflight.Group),
	}
}

func htmlHandler(calls *int) nextFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		*calls++
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello"))
		return nil
	}
}

func TestServeMissThenHit(t *testing.T) {
	c := newTestCache(t, 0)
	calls := 0
	next := htmlHandler(&calls)

	w1 := httptest.NewRecorder()
	if err := c.ServeHTTP(w1, httptest.NewRequest("GET", "/", nil), next); err != nil {
		t.Fatal(err)
	}
	if got := w1.Header().Get("X-Cache"); got != "MISS" {
		t.Fatalf("first X-Cache = %q, want MISS", got)
	}
	if w1.Body.String() != "hello" {
		t.Fatalf("body = %q", w1.Body.String())
	}

	w2 := httptest.NewRecorder()
	if err := c.ServeHTTP(w2, httptest.NewRequest("GET", "/", nil), next); err != nil {
		t.Fatal(err)
	}
	if got := w2.Header().Get("X-Cache"); got != "HIT" {
		t.Fatalf("second X-Cache = %q, want HIT", got)
	}
	if w2.Body.String() != "hello" {
		t.Fatalf("hit body = %q", w2.Body.String())
	}
	if calls != 1 {
		t.Fatalf("next called %d times, want 1", calls)
	}
}

func TestServeBypassLoggedIn(t *testing.T) {
	c := newTestCache(t, 0)
	calls := 0
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "wordpress_logged_in_abc", Value: "1"})
	w := httptest.NewRecorder()
	if err := c.ServeHTTP(w, r, htmlHandler(&calls)); err != nil {
		t.Fatal(err)
	}
	if got := w.Header().Get("X-Cache"); got != "BYPASS" {
		t.Fatalf("X-Cache = %q, want BYPASS", got)
	}
}

func TestServeBypassDebugQuery(t *testing.T) {
	c := newTestCache(t, 0)
	calls := 0
	w := httptest.NewRecorder()
	if err := c.ServeHTTP(w, httptest.NewRequest("GET", "/?NOCACHE=1", nil), htmlHandler(&calls)); err != nil {
		t.Fatal(err)
	}
	if got := w.Header().Get("X-Cache"); got != "BYPASS" {
		t.Fatalf("X-Cache = %q, want BYPASS", got)
	}
}

func TestServeQueryStripVsBypass(t *testing.T) {
	c := newTestCache(t, 0)
	calls := 0
	next := htmlHandler(&calls)

	// Prime the clean page.
	c.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil), next)

	// Tracking param is stripped => same cached page => HIT, no new render.
	wTrack := httptest.NewRecorder()
	c.ServeHTTP(wTrack, httptest.NewRequest("GET", "/?fbclid=xyz", nil), next)
	if got := wTrack.Header().Get("X-Cache"); got != "HIT" {
		t.Fatalf("tracking-param X-Cache = %q, want HIT", got)
	}

	// Real query => bypass in strip mode.
	wQuery := httptest.NewRecorder()
	c.ServeHTTP(wQuery, httptest.NewRequest("GET", "/?s=secret", nil), next)
	if got := wQuery.Header().Get("X-Cache"); got != "BYPASS" {
		t.Fatalf("query X-Cache = %q, want BYPASS", got)
	}
}

func statusOf(t *testing.T, err error) int {
	t.Helper()
	var he caddyhttp.HandlerError
	if !errors.As(err, &he) {
		t.Fatalf("error is not a caddyhttp.HandlerError: %v", err)
	}
	return he.StatusCode
}

func TestPurgeAuthFailClosed(t *testing.T) {
	c := newTestCache(t, 0)
	c.PurgeKey = "s3cret"
	calls := 0
	next := htmlHandler(&calls)

	// Missing header => 401.
	err := c.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/__cache/purge/", nil), next)
	if got := statusOf(t, err); got != http.StatusUnauthorized {
		t.Fatalf("missing key status = %d, want 401", got)
	}

	// Wrong header => 403.
	rWrong := httptest.NewRequest("POST", "/__cache/purge/", nil)
	rWrong.Header.Set("X-Purge-Key", "nope")
	if got := statusOf(t, c.ServeHTTP(httptest.NewRecorder(), rWrong, next)); got != http.StatusForbidden {
		t.Fatalf("wrong key status = %d, want 403", got)
	}

	// Correct header => 200 OK.
	rOK := httptest.NewRequest("POST", "/__cache/purge/", nil)
	rOK.Header.Set("X-Purge-Key", "s3cret")
	wOK := httptest.NewRecorder()
	if err := c.ServeHTTP(wOK, rOK, next); err != nil {
		t.Fatalf("correct key err = %v", err)
	}
	if wOK.Code != http.StatusOK {
		t.Fatalf("correct key code = %d, want 200", wOK.Code)
	}
}

// With no PurgeKey configured the endpoint is inert (fails closed): the request
// is treated as a normal request rather than granting purge access.
func TestPurgeDisabledWhenNoKey(t *testing.T) {
	c := newTestCache(t, 0)
	c.PurgeKey = ""
	calls := 0
	// POST is non-GET so it just passes through to next; the key point is that
	// no purge/flush is triggered and no auth bypass occurs.
	err := c.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/__cache/purge/", nil), htmlHandler(&calls))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if calls != 1 {
		t.Fatalf("next calls = %d, want 1 (passthrough)", calls)
	}
}

func TestConditional304(t *testing.T) {
	c := newTestCache(t, 0)
	calls := 0
	next := htmlHandler(&calls)

	// Prime + read the ETag from a HIT.
	c.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil), next)
	wHit := httptest.NewRecorder()
	c.ServeHTTP(wHit, httptest.NewRequest("GET", "/", nil), next)
	etag := wHit.Header().Get("Etag")
	if etag == "" {
		t.Fatal("no ETag on hit")
	}

	rCond := httptest.NewRequest("GET", "/", nil)
	rCond.Header.Set("If-None-Match", etag)
	wCond := httptest.NewRecorder()
	if err := c.ServeHTTP(wCond, rCond, next); err != nil {
		t.Fatal(err)
	}
	if wCond.Code != http.StatusNotModified {
		t.Fatalf("code = %d, want 304", wCond.Code)
	}
	if wCond.Body.Len() != 0 {
		t.Fatalf("304 body should be empty, got %q", wCond.Body.String())
	}
}

func TestInvalidTTLConfigErrorNotPanic(t *testing.T) {
	c := new(Cache) // logger is nil, as at Caddyfile parse time
	err := c.UnmarshalCaddyfile(caddyfile.NewTestDispenser("ttl not-a-number"))
	if err == nil {
		t.Fatal("expected a config error for invalid ttl")
	}
}
