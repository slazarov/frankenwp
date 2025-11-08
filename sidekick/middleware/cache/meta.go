package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"
)

var (
	hdrResCacheList = []string{
		"Accept-Ranges",
		// "Content-Encoding",
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

		"Server-Timing", // ?

		// should put in remove?
		"Server",
		"X-Powered-By",

		// TODO:
		"Vary",
		"Link",
		"Expires",
		"Age",
		// "Via",

		"Refresh",

		// deprecated
		"Pragma", // TODO: ?
		"X-Xss-Protection",
		"Warning",

		// not sure
		"X-UA-Compatible",
	}

	// bypass if response has any of these header
	hdrResNotCacheList = []string{
		"Content-Range",
		"Www-Authenticate",

		"Connection",
		"Proxy-Connection", // non-standard but still sent by libcurl and rejected by e.g. google
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Te",      // canonicalized version of "TE"
		"Trailer", // not Trailers per URL above; https://www.rfc-editor.org/errata_search.php?eid=4522
		"Upgrade",

		// mostly some rate limit control
		"Retry-After",

		// "Keep-Alive",
		// "Transfer-Encoding",
	}
)

type CacheMeta struct {
	StateCode int        `json:"c,omitempty"`
	Header    [][]string `json:"h,omitempty"`
	Timestamp int64      `json:"t,omitempty"`

	contentEncoding string
}

// GenerateETag creates a strong ETag based on content hash
func GenerateETag(content []byte) string {
	hash := sha256.Sum256(content)
	return `"` + hex.EncodeToString(hash[:16]) + `"`
}

func NewCacheMeta(stateCode int, hdr http.Header, content []byte) *CacheMeta {
	// content encoding
	ce := hdr.Get("Content-Encoding")
	if ce == "" {
		ce = "none"
	}
	// skip if Content-Encoding not in list
	if !slices.Contains(CachedContentEncoding, ce) {
		return nil
	}

	// TODO: pool
	meta := &CacheMeta{
		StateCode: stateCode,
		Header:    make([][]string, 0, 8),
		Timestamp: time.Now().Unix(),

		contentEncoding: ce,
	}
	meta.SetHeader(hdr)

	// Generate ETag if not present
	if hdr.Get("Etag") == "" {
		etag := GenerateETag(content)
		meta.Header = append(meta.Header, []string{"Etag", etag})
	}

	// Set Last-Modified if not present
	if hdr.Get("Last-Modified") == "" {
		lastModified := time.Unix(meta.Timestamp, 0).UTC().Format(http.TimeFormat)
		meta.Header = append(meta.Header, []string{"Last-Modified", lastModified})
	}

	return meta
}

func (m *CacheMeta) SetHeader(hdr http.Header) {
	for key := range hdr {
		ok := slices.Contains(hdrResCacheList, key)
		if ok {
			m.Header = append(m.Header, []string{key, strings.Join(hdr[key], ",")})
		}
	}
}

func (m *CacheMeta) WriteToFile(fp string) error {
	fd, err := os.OpenFile(fp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer fd.Close()
	enc := json.NewEncoder(fd)
	return enc.Encode(m)
}

func (m *CacheMeta) LoadFromFile(fp string) error {
	fd, err := os.Open(fp)
	if err != nil {
		return err
	}
	defer fd.Close()
	dec := json.NewDecoder(fd)
	return dec.Decode(m)
}