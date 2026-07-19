package cache

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"
)

// defaultTrackingParams are query parameters stripped before keying (in
// query_mode=strip they let a tracking URL still hit the clean cached page).
var defaultTrackingParams = []string{
	"gclid", "gclsrc", "dclid", "fbclid", "msclkid", "wbraid", "gbraid",
	"mc_cid", "mc_eid", "_ga", "_gl", "ref", "igshid", "yclid", "utm_id",
}

type Cache struct {
	logger             *zap.Logger
	Loc                string
	PurgePath          string
	PurgeKeyHeader     string
	PurgeKey           string
	CacheHeaderName    string
	BypassPathPrefixes []string
	BypassPathRegex    string
	BypassHome         bool
	BypassDebugQuery   string
	CacheResponseCodes []string
	TTL                int
	QueryMode          string
	TrackingParams     []string
	Store              *Store

	MemoryItemMaxSize   int
	MemoryCacheMaxSize  int
	MemoryCacheMaxCount int

	pathRx         *regexp.Regexp
	trackingParams map[string]struct{}
	preloadSem     chan struct{}
	preloadGroup   *singleflight.Group
}

func init() {
	caddy.RegisterModule(Cache{})
	httpcaddyfile.RegisterHandlerDirective("wp_cache", parseCaddyfileHandler)
}

func parseCaddyfileHandler(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	c := new(Cache)
	if err := c.UnmarshalCaddyfile(h.Dispenser); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Cache) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	for d.Next() {
		var value string
		key := d.Val()
		if !d.Args(&value) {
			continue
		}

		switch key {
		case "loc":
			c.Loc = value

		case "bypass_path_prefixes":
			c.BypassPathPrefixes = strings.Split(strings.TrimSpace(value), ",")

		case "bypass_path_regex":
			value = strings.TrimSpace(value)
			if len(value) != 0 {
				if _, err := regexp.Compile(value); err != nil {
					return d.Errf("invalid bypass_path_regex %q: %v", value, err)
				}
			} else {
				value = ".*(\\.[^.]+)$"
			}
			c.BypassPathRegex = value

		case "bypass_home":
			c.BypassHome = strings.EqualFold(value, "true")

		case "bypass_debug_query":
			c.BypassDebugQuery = strings.TrimSpace(value)

		case "cache_response_codes":
			c.CacheResponseCodes = parseResponseCodes(value)

		case "query_mode":
			c.QueryMode = strings.ToLower(strings.TrimSpace(value))

		case "tracking_params":
			c.TrackingParams = splitCSV(value)

		case "ttl":
			ttl, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return d.Errf("invalid ttl value %q: %v", value, err)
			}
			c.TTL = ttl

		case "purge_path":
			c.PurgePath = value

		case "purge_key":
			c.PurgeKey = strings.TrimSpace(value)

		case "purge_key_header":
			c.PurgeKeyHeader = value

		case "cache_header_name":
			c.CacheHeaderName = value

		case "memory_item_max_size":
			if n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil {
				c.MemoryItemMaxSize = int(n)
			}
		case "memory_max_size":
			if n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil {
				c.MemoryCacheMaxSize = int(n)
			}
		case "memory_max_count":
			if n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil {
				c.MemoryCacheMaxCount = int(n)
			}
		}
	}
	return nil
}

func parseResponseCodes(value string) []string {
	codes := splitCSV(value)
	out := make([]string, 0, len(codes))
	for _, code := range codes {
		if strings.Contains(code, "XX") {
			code = string(code[0])
		}
		out = append(out, code)
	}
	return out
}

func splitCSV(value string) []string {
	parts := strings.Split(strings.TrimSpace(value), ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (c *Cache) Provision(ctx caddy.Context) error {
	c.logger = ctx.Logger(c)

	if c.Loc == "" {
		c.Loc = os.Getenv("CACHE_LOC")
	}

	if c.CacheResponseCodes == nil {
		c.CacheResponseCodes = parseResponseCodes(os.Getenv("CACHE_RESPONSE_CODES"))
	}

	if c.BypassPathPrefixes == nil {
		c.BypassPathPrefixes = splitCSV(os.Getenv("BYPASS_PATH_PREFIXES"))
	}

	if c.BypassPathRegex == "" {
		c.BypassPathRegex = ".*(\\.[^.]+)$"
	}
	rx, err := regexp.Compile(c.BypassPathRegex)
	if err != nil {
		return err
	}
	c.pathRx = rx

	if !c.BypassHome {
		c.BypassHome = strings.EqualFold(os.Getenv("BYPASS_HOME"), "true")
	}

	if c.BypassDebugQuery == "" {
		c.BypassDebugQuery = os.Getenv("BYPASS_DEBUG_QUERY")
		if c.BypassDebugQuery == "" {
			c.BypassDebugQuery = "WPEverywhere-NOCACHE"
		}
	}

	if c.TTL == 0 {
		if v := strings.TrimSpace(os.Getenv("TTL")); v != "" {
			ttl, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("wp_cache: invalid TTL env %q: %w", v, err)
			}
			c.TTL = ttl
		}
	}

	if c.PurgePath == "" {
		c.PurgePath = os.Getenv("PURGE_PATH")
		if c.PurgePath == "" {
			c.PurgePath = "/__cache/purge"
		}
	}

	if c.PurgeKey == "" {
		c.PurgeKey = strings.TrimSpace(os.Getenv("PURGE_KEY"))
	}

	if c.PurgeKeyHeader == "" {
		c.PurgeKeyHeader = os.Getenv("PURGE_KEY_HEADER")
		if c.PurgeKeyHeader == "" {
			c.PurgeKeyHeader = "X-WPSidekick-Purge-Key"
		}
	}

	if c.CacheHeaderName == "" {
		c.CacheHeaderName = os.Getenv("CACHE_HEADER_NAME")
		if c.CacheHeaderName == "" {
			c.CacheHeaderName = "X-FrankenWP-Cache"
		}
	}

	if c.QueryMode == "" {
		c.QueryMode = strings.ToLower(strings.TrimSpace(os.Getenv("QUERY_MODE")))
		if c.QueryMode == "" {
			c.QueryMode = "strip"
		}
	}

	c.trackingParams = make(map[string]struct{})
	params := c.TrackingParams
	if len(params) == 0 {
		params = defaultTrackingParams
	}
	for _, p := range params {
		c.trackingParams[strings.ToLower(strings.TrimSpace(p))] = struct{}{}
	}

	if c.MemoryItemMaxSize == 0 {
		c.MemoryItemMaxSize = 4 * 1024 * 1024 // 4MB
	}
	if c.MemoryItemMaxSize < 0 {
		c.MemoryItemMaxSize = math.MaxInt
	}
	if c.MemoryCacheMaxSize == 0 {
		c.MemoryCacheMaxSize = 128 * 1024 * 1024 // 128MB
	}
	if c.MemoryCacheMaxCount == 0 {
		c.MemoryCacheMaxCount = 32 * 1024 // 32K
	}

	c.preloadSem = make(chan struct{}, max(2, runtime.GOMAXPROCS(0)))
	c.preloadGroup = new(singleflight.Group)

	store, err := NewStore(c.Loc, c.TTL, c.MemoryCacheMaxSize, c.MemoryCacheMaxCount, c.logger)
	if err != nil {
		return err
	}
	c.Store = store

	return nil
}

func (c *Cache) Validate() error {
	if c.Store == nil {
		return fmt.Errorf("wp_cache: store not provisioned")
	}

	probe := path.Join(c.Loc, CACHE_DIR, ".probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {
		return fmt.Errorf("wp_cache: cache dir %q is not writable: %w", c.Loc, err)
	}
	_ = os.Remove(probe)

	if c.PurgeKey == "" {
		c.logger.Warn("wp_cache: purge_key is empty; the purge/flush endpoint is disabled")
	}

	for _, code := range c.CacheResponseCodes {
		if code == "" {
			continue
		}
		if code == "5" {
			c.logger.Warn("wp_cache: 5xx wildcard is ignored; list exact 5xx codes to cache error pages")
			continue
		}
		for _, ch := range code {
			if ch < '0' || ch > '9' {
				return fmt.Errorf("wp_cache: invalid cache_response_code %q", code)
			}
		}
	}

	if c.QueryMode != "strip" && c.QueryMode != "include" {
		return fmt.Errorf("wp_cache: invalid query_mode %q (want strip or include)", c.QueryMode)
	}

	return nil
}

func (Cache) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.wp_cache",
		New: func() caddy.Module { return new(Cache) },
	}
}

// normalizeQuery derives the query variant for the cache key. In "strip" mode a
// URL with only tracking params keys to the clean page; any other query bypasses
// caching. In "include" mode the canonical remaining query is hashed into the key.
func (c *Cache) normalizeQuery(u *url.URL) (variant string, cacheable bool) {
	if u.RawQuery == "" {
		return "", true
	}
	q := u.Query()
	for k := range q {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "utm_") {
			delete(q, k)
			continue
		}
		if _, ok := c.trackingParams[lk]; ok {
			delete(q, k)
		}
	}
	if len(q) == 0 {
		return "", true
	}
	if c.QueryMode == "include" {
		for _, vs := range q {
			sort.Strings(vs)
		}
		sum := sha256.Sum256([]byte(q.Encode()))
		return fmt.Sprintf("q%x", sum[:8]), true
	}
	return "", false
}

// checkConditionalRequest implements RFC 7232 precedence: If-None-Match wins and
// If-Modified-Since is only consulted in its absence. Returns true when a 304 is
// warranted.
func checkConditionalRequest(r *http.Request, cacheMeta *CacheMeta) bool {
	if inm := r.Header.Get("If-None-Match"); inm != "" {
		cachedETag, ok := cacheMeta.headerValue("Etag")
		if !ok || cachedETag == "" {
			return false
		}
		if strings.TrimSpace(inm) == "*" {
			return true
		}
		for _, tag := range strings.Split(inm, ",") {
			if etagWeakEqual(strings.TrimSpace(tag), cachedETag) {
				return true
			}
		}
		return false
	}

	if ims := r.Header.Get("If-Modified-Since"); ims != "" {
		cachedLM, ok := cacheMeta.headerValue("Last-Modified")
		if !ok {
			return false
		}
		imsTime, err1 := http.ParseTime(ims)
		lmTime, err2 := http.ParseTime(cachedLM)
		if err1 == nil && err2 == nil && !lmTime.After(imsTime) {
			return true
		}
	}
	return false
}

// etagWeakEqual compares ETags using the weak-comparison rules (RFC 7232 §2.3.2):
// the optional W/ prefix is ignored on both sides.
func etagWeakEqual(a, b string) bool {
	return strings.TrimPrefix(a, "W/") == strings.TrimPrefix(b, "W/")
}

// ServeHTTP implements caddyhttp.MiddlewareHandler.
func (c *Cache) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	reqHdr := r.Header
	db := c.Store
	hdr := w.Header()

	// Purge / flush API — disabled entirely when no key is configured (fail closed).
	if c.PurgeKey != "" && strings.HasPrefix(r.URL.Path, c.PurgePath) {
		provided := reqHdr.Get(c.PurgeKeyHeader)
		if provided == "" {
			return caddyhttp.Error(http.StatusUnauthorized, fmt.Errorf("wp_cache: missing purge key"))
		}
		ph := sha256.Sum256([]byte(provided))
		kh := sha256.Sum256([]byte(c.PurgeKey))
		if subtle.ConstantTimeCompare(ph[:], kh[:]) != 1 {
			c.logger.Warn("wp cache - purge - invalid key", zap.String("path", r.URL.Path))
			return caddyhttp.Error(http.StatusForbidden, fmt.Errorf("wp_cache: invalid purge key"))
		}

		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(db.List()); err != nil {
				c.logger.Error("wp cache - list encode", zap.Error(err))
			}
			return nil
		case http.MethodPost:
			pathToPurge := strings.TrimPrefix(r.URL.Path, c.PurgePath)
			if len(pathToPurge) < 2 {
				c.logger.Info("wp cache - flush all")
				recordEvent("flush")
				safeGo(c.logger, func() { _ = db.Flush() })
			} else {
				c.logger.Info("wp cache - purge", zap.String("path", pathToPurge))
				recordEvent("purge")
				safeGo(c.logger, func() { db.Purge(pathToPurge) })
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("OK"))
			return nil
		default:
			return caddyhttp.Error(http.StatusMethodNotAllowed, nil)
		}
	}

	// Only GET is cacheable.
	if r.Method != http.MethodGet {
		return next.ServeHTTP(w, r)
	}

	if c.bypass(r) {
		hdr.Set(c.CacheHeaderName, "BYPASS")
		recordEvent("bypass")
		return next.ServeHTTP(w, r)
	}

	variant, cacheable := c.normalizeQuery(r.URL)
	if !cacheable {
		hdr.Set(c.CacheHeaderName, "BYPASS")
		recordEvent("bypass")
		return next.ServeHTTP(w, r)
	}

	requestEncoding := strings.Split(strings.Join(reqHdr["Accept-Encoding"], ""), ",")
	if len(requestEncoding) == 1 && len(requestEncoding[0]) == 0 {
		requestEncoding = nil
	}
	requestEncoding = append(requestEncoding, "none")

	var cacheData []byte
	var cacheMeta *CacheMeta
	var err error
	ce := ""
	for _, re := range requestEncoding {
		ce = strings.TrimSpace(re)
		cacheData, cacheMeta, err = db.Get(r.Context(), r.URL.Path, variant, ce)
		if err == nil {
			break
		}
	}

	if err == nil {
		// Only an uncompressed variant is cached but the client accepts
		// compression: warm a compressed copy in the background (bounded).
		if ce == "none" && len(requestEncoding) > 0 && requestEncoding[0] != "none" {
			c.triggerPreload(r, next, variant)
		}

		if checkConditionalRequest(r, cacheMeta) {
			hdr.Set(c.CacheHeaderName, "HIT-304")
			recordEvent("hit_304")
			hdr.Set("Vary", "Accept-Encoding")
			for _, kv := range cacheMeta.Header {
				if len(kv) != 2 {
					continue
				}
				switch kv[0] {
				case "Etag", "Last-Modified", "Cache-Control", "Expires":
					hdr.Set(kv[0], kv[1])
				}
			}
			w.WriteHeader(http.StatusNotModified)
			return nil
		}

		hdr.Set(c.CacheHeaderName, "HIT")
		recordEvent("hit")
		hdr.Set("Vary", "Accept-Encoding")
		if ce != "none" {
			hdr.Set("Content-Encoding", ce)
		}
		for _, kv := range cacheMeta.Header {
			if len(kv) != 2 {
				continue
			}
			hdr.Set(kv[0], kv[1])
		}
		w.WriteHeader(cacheMeta.StateCode)
		_, _ = w.Write(cacheData)
		return nil
	}

	recordEvent("miss")
	nw := NewCustomWriter(w, r, db, c.logger, c, variant)
	defer nw.Close()
	return next.ServeHTTP(nw, r)
}

func (c *Cache) bypass(r *http.Request) bool {
	if c.BypassDebugQuery != "" && r.URL.Query().Has(c.BypassDebugQuery) {
		return true
	}
	for _, prefix := range c.BypassPathPrefixes {
		if prefix != "" && strings.HasPrefix(r.URL.Path, prefix) {
			return true
		}
	}
	if c.pathRx != nil && c.pathRx.MatchString(r.URL.Path) {
		return true
	}
	if c.BypassHome && r.URL.Path == "/" {
		return true
	}
	for _, cookie := range r.Cookies() {
		if strings.HasPrefix(cookie.Name, "wordpress_logged_in") {
			return true
		}
	}
	return false
}

// triggerPreload runs a bounded, deduplicated background re-render to warm a
// compressed variant. It never blocks the request and recovers from panics.
func (c *Cache) triggerPreload(r *http.Request, next caddyhttp.Handler, variant string) {
	select {
	case c.preloadSem <- struct{}{}:
	default:
		return // at capacity — skip
	}
	key := r.URL.Path + "::" + variant
	safeGo(c.logger, func() {
		defer func() { <-c.preloadSem }()
		_, _, _ = c.preloadGroup.Do(key, func() (interface{}, error) {
			c.doCache(r, next, variant)
			return nil, nil
		})
	})
}

func (c *Cache) doCache(r0 *http.Request, next caddyhttp.Handler, variant string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	r := r0.Clone(ctx)
	repl := caddy.NewReplacer()
	r = caddyhttp.PrepareRequest(r, repl, nil, nil)

	nw := NewCustomWriter(&NopResponseWriter{}, r, c.Store, c.logger, c, variant)
	defer nw.Close()
	_ = next.ServeHTTP(nw, r)
}

// safeGo runs fn in a goroutine, recovering panics so a background failure never
// crashes the server.
func safeGo(logger *zap.Logger, fn func()) {
	go func() {
		defer func() {
			if p := recover(); p != nil {
				logger.Error("wp cache - goroutine panic", zap.Any("panic", p), zap.Stack("stack"))
			}
		}()
		fn()
	}()
}

// Interface guards
var (
	_ caddy.Provisioner           = (*Cache)(nil)
	_ caddy.Validator             = (*Cache)(nil)
	_ caddyhttp.MiddlewareHandler = (*Cache)(nil)
	_ caddyfile.Unmarshaler       = (*Cache)(nil)

	_ http.ResponseWriter = (*NopResponseWriter)(nil)
)

type NopResponseWriter map[string][]string

func (nop *NopResponseWriter) WriteHeader(statusCode int) {}

func (nop *NopResponseWriter) Write(buf []byte) (int, error) { return len(buf), nil }

func (nop *NopResponseWriter) Header() http.Header { return http.Header(*nop) }
