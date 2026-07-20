package cache

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"
)

var (
	// hdrResCacheList: response headers that are stored and replayed on a HIT.
	hdrResCacheList = []string{
		"Accept-Ranges",
		"Content-Length",
		"Content-Type",
		"Location",
		"Etag",
		"Last-Modified",

		"Access-Control-Allow-Origin",
		"Access-Control-Max-Age",
		"Access-Control-Allow-Headers",
		"Access-Control-Allow-Methods",
		"Access-Control-Allow-Credentials",
		"Access-Control-Expose-Headers",

		"Referrer-Policy",
		"Strict-Transport-Security",
		"Content-Security-Policy",
		"X-Content-Type-Options",
		"X-Frame-Options",
		"X-Robots-Tag",

		// wordpress
		"X-Pingback",

		// CDN edge invalidation (Cloudflare Cache-Tag / Fastly Surrogate-Key)
		"Cache-Tag",
		"Surrogate-Key",

		"Server-Timing",

		"Cache-Control",
		"Vary",
		"Link",
		"Expires",
		"Age",

		"Refresh",

		// deprecated but occasionally required
		"Pragma",
		"X-Xss-Protection",
		"X-UA-Compatible",
	}

	// hdrResNotCacheList: hop-by-hop / non-cacheable response headers. If any is
	// present the response is not stored. (Value-based rules — Cache-Control,
	// Set-Cookie, Vary — are handled by responseForbidsCache.)
	hdrResNotCacheList = []string{
		"Content-Range",
		"Www-Authenticate",

		"Connection",
		"Proxy-Connection",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Te",
		"Trailer",
		"Upgrade",

		"Retry-After",
	}
)

type CacheMeta struct {
	StateCode int        `json:"c,omitempty"`
	Header    [][]string `json:"h,omitempty"`
	Timestamp int64      `json:"t,omitempty"`

	contentEncoding string
}

// GenerateETag creates a weak ETag from the response body using SHA-256.
func GenerateETag(data []byte) string {
	hash := sha256.Sum256(data)
	return fmt.Sprintf(`W/"%x"`, hash[:16])
}

// responseForbidsCache reports whether a response's headers make it unsafe to
// store in a shared full-page cache: it carries Set-Cookie, its Cache-Control
// marks it private/uncacheable, or it Varies on Cookie/Authorization/*.
func responseForbidsCache(hdr http.Header) (bool, string) {
	if len(hdr.Values("Set-Cookie")) > 0 {
		return true, "Set-Cookie present"
	}

	cc := strings.Join(hdr.Values("Cache-Control"), ",")
	for _, tok := range splitHeaderTokens(cc) {
		switch ccDirectiveName(tok) {
		case "private", "no-store", "no-cache":
			return true, "Cache-Control: " + ccDirectiveName(tok)
		case "max-age", "s-maxage":
			if ccDirectiveValue(tok) == "0" {
				return true, "Cache-Control: " + tok
			}
		}
	}

	vary := strings.Join(hdr.Values("Vary"), ",")
	for _, tok := range splitHeaderTokens(vary) {
		switch strings.ToLower(tok) {
		case "*", "cookie", "authorization":
			return true, "Vary: " + tok
		}
	}

	return false, ""
}

func splitHeaderTokens(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	raw := strings.Split(s, ",")
	out := raw[:0]
	for _, p := range raw {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func ccDirectiveName(tok string) string {
	if i := strings.IndexByte(tok, '='); i >= 0 {
		return strings.ToLower(strings.TrimSpace(tok[:i]))
	}
	return strings.ToLower(strings.TrimSpace(tok))
}

func ccDirectiveValue(tok string) string {
	if i := strings.IndexByte(tok, '='); i >= 0 {
		return strings.Trim(strings.TrimSpace(tok[i+1:]), `"`)
	}
	return ""
}

// NewCacheMeta captures the cacheable subset of response headers plus a
// body-derived ETag and a stable Last-Modified. When prev is non-nil and the
// body is unchanged (same ETag), the prior Last-Modified is carried forward so
// conditional revalidation keeps working across recaches.
func NewCacheMeta(stateCode int, hdr http.Header, data []byte, prev *CacheMeta) *CacheMeta {
	ce := hdr.Get("Content-Encoding")
	if ce == "" {
		ce = "none"
	}
	if !slices.Contains(CachedContentEncoding, ce) {
		return nil
	}

	now := time.Now().Unix()
	meta := &CacheMeta{
		StateCode:       stateCode,
		Header:          make([][]string, 0, 8),
		Timestamp:       now,
		contentEncoding: ce,
	}

	if hdr.Get("Etag") == "" && len(data) > 0 {
		hdr.Set("Etag", GenerateETag(data))
	}

	if hdr.Get("Last-Modified") == "" {
		lm := time.Unix(now, 0).UTC().Format(http.TimeFormat)
		if prev != nil {
			if prevETag, ok := prev.headerValue("Etag"); ok && prevETag == hdr.Get("Etag") {
				if prevLM, ok := prev.headerValue("Last-Modified"); ok {
					lm = prevLM
				}
			}
		}
		hdr.Set("Last-Modified", lm)
	}

	meta.SetHeader(hdr)
	return meta
}

func (m *CacheMeta) SetHeader(hdr http.Header) {
	for key := range hdr {
		if slices.Contains(hdrResCacheList, key) {
			m.Header = append(m.Header, []string{key, strings.Join(hdr[key], ",")})
		}
	}
}

func (m *CacheMeta) headerValue(name string) (string, bool) {
	for _, kv := range m.Header {
		if len(kv) == 2 && kv[0] == name {
			return kv[1], true
		}
	}
	return "", false
}

func (m *CacheMeta) WriteToFileAtomic(fp string) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return writeFileAtomic(fp, data, 0o644)
}

func (m *CacheMeta) LoadFromFile(fp string) error {
	fd, err := os.Open(fp)
	if err != nil {
		return err
	}
	defer func() { _ = fd.Close() }()
	return json.NewDecoder(fd).Decode(m)
}
