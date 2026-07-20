package cache

import (
	"bytes"
	"net/http"
	"slices"
	"strconv"
	"sync/atomic"

	"go.uber.org/zap"
)

func NewCustomWriter(rw http.ResponseWriter, r *http.Request, db *Store, logger *zap.Logger, c *Cache, variant string) *CustomWriter {
	return &CustomWriter{
		ResponseWriter: rw,
		Request:        r,
		Store:          db,
		Logger:         logger,

		reqPath: r.URL.Path,
		variant: variant,

		cacheMaxSize:       c.MemoryItemMaxSize,
		cacheResponseCodes: c.CacheResponseCodes,
		cacheHeaderName:    c.CacheHeaderName,
		status:             -1,
	}
}

var _ http.ResponseWriter = (*CustomWriter)(nil)

// CustomWriter wraps the downstream response, decides whether it may be cached,
// buffers the body, and (as the population leader) stores it on Close.
type CustomWriter struct {
	http.ResponseWriter
	*http.Request
	*Store
	*zap.Logger

	cacheResponseCodes []string
	cacheHeaderName    string
	cacheMaxSize       int

	reqPath string
	variant string

	// leader is set when this writer won the populating race and is responsible
	// for storing the response (and releasing the populating slot on Close).
	leader bool

	// -1 means header not sent yet.
	status int32

	// needCache flags that response data should be buffered/stored.
	needCache int32

	buf []byte
}

func (r *CustomWriter) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

func (r *CustomWriter) Header() http.Header {
	return r.ResponseWriter.Header()
}

func (r *CustomWriter) cacheableStatus(status int) bool {
	statusStr := strconv.Itoa(status)
	matched := false
	for _, code := range r.cacheResponseCodes {
		if code == statusStr || (len(code) == 1 && code == statusStr[:1]) {
			matched = true
			break
		}
	}
	if !matched {
		return false
	}
	// Never cache server errors via a wildcard; require an exact 3-digit opt-in.
	if status >= 500 {
		return slices.Contains(r.cacheResponseCodes, statusStr)
	}
	return true
}

func (r *CustomWriter) WriteHeader(status int) {
	atomic.StoreInt32(&r.status, int32(status))
	hdr := r.ResponseWriter.Header()

	cacheable := r.cacheableStatus(status)

	if cacheable {
		for h := range hdr {
			if slices.Contains(hdrResNotCacheList, h) {
				cacheable = false
				break
			}
		}
	}

	if cacheable {
		if forbid, reason := responseForbidsCache(hdr); forbid {
			cacheable = false
			r.Debug("wp cache - bypass response semantics", zap.String("reason", reason))
		}
	}

	if !cacheable {
		hdr.Set(r.cacheHeaderName, "BYPASS")
		r.ResponseWriter.WriteHeader(status)
		return
	}

	hdr.Set(r.cacheHeaderName, "MISS")

	// Elect a single leader to populate this page variant; followers still
	// serve the origin response but do not write to the cache.
	dirKey := r.dirKey(r.reqPath, r.variant)
	if r.tryLeadPopulation(dirKey) {
		r.leader = true
		atomic.StoreInt32(&r.needCache, 1)
	}

	r.ResponseWriter.WriteHeader(status)
}

func (r *CustomWriter) Write(b []byte) (int, error) {
	if atomic.CompareAndSwapInt32(&r.status, -1, 200) {
		r.WriteHeader(200)
	}

	if atomic.LoadInt32(&r.needCache) == 1 {
		if len(r.buf)+len(b) <= r.cacheMaxSize {
			// Write is not called concurrently for a single response.
			r.buf = append(r.buf, b...)
		} else {
			atomic.StoreInt32(&r.needCache, 0)
			r.buf = nil
			r.Debug("wp cache - bypass size", zap.Int("limit", r.cacheMaxSize))
		}
	}

	return r.ResponseWriter.Write(b)
}

// consentSuppressedMarkers are byte patterns that indicate a page carries a
// consent-suppressed / lazy-activated embed (e.g. a Google Maps iframe held at
// src="about:blank" until the visitor's consent JS swaps in data-suppressedsrc).
// Such pages depend on per-visitor client-side activation and must not be
// stored in the shared full-page cache.
//
// These markers are deliberately narrow: they appear only on pages that
// actually contain a suppressed embed, not on every page. Broad consent-banner
// signals (Cookiebot, OneTrust, generic "CookieConsent", etc.) are intentionally
// excluded because those scripts load site-wide and matching them would disable
// caching for the entire site.
var consentSuppressedMarkers = [][]byte{
	[]byte("data-suppressedsrc"), // CMP-suppressed iframe source (Iubenda, etc.)
	[]byte("_iub_cs_activate"),   // Iubenda consent activation hook
	[]byte("cmplazyload"),        // CMP lazy-load activation class
}

// shouldBypassCacheForContent reports whether the buffered response body carries
// a consent-suppressed embed that requires per-visitor client-side activation
// and therefore must not be cached.
func shouldBypassCacheForContent(content []byte) bool {
	for _, marker := range consentSuppressedMarkers {
		if bytes.Contains(content, marker) {
			return true
		}
	}
	return false
}

// Close stores the buffered response (leader only) and releases the population
// slot. Safe to call when nothing was cached.
func (r *CustomWriter) Close() error {
	if !r.leader {
		return nil
	}
	defer r.donePopulation(r.dirKey(r.reqPath, r.variant))

	if atomic.LoadInt32(&r.needCache) != 1 {
		return nil
	}

	if shouldBypassCacheForContent(r.buf) {
		r.Debug("wp cache - bypass consent-suppressed embed", zap.String("path", r.reqPath))
		return nil
	}

	hdr := r.ResponseWriter.Header()
	ce := hdr.Get("Content-Encoding")
	if ce == "" {
		ce = "none"
	}
	prev, _ := r.Peek(r.reqPath, r.variant, ce)

	meta := NewCacheMeta(int(atomic.LoadInt32(&r.status)), hdr, r.buf, prev)
	if meta == nil {
		return nil
	}
	if err := r.Set(r.reqPath, r.variant, meta, r.buf); err != nil {
		r.Error("wp cache - set failed", zap.String("path", r.reqPath), zap.Error(err))
	}
	return nil
}
