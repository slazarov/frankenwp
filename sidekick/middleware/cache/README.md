# wp_cache — Sidekick full-page cache

A Caddy HTTP handler module (`http.handlers.wp_cache`) that gives WordPress a fast,
WordPress-aware full-page cache. It is compiled into the FrankenPHP binary via
`xcaddy` (see the repo `Containerfile`).

## Design

Two tiers, both written on every store:

- **Memory** — an [Otter v2](https://github.com/maypok86/otter) cache
  (adaptive W-TinyLFU), bounded by total bytes (`MaximumWeight` + a weigher over
  key+body). Concurrent misses for the same entry are coalesced by Otter's loader.
- **Disk** — `<CACHE_LOC>/sidekick-cache/<key>/` with one body file per encoding
  (`.none`, `.gzip`, `.br`, `.zstd`) plus a per-encoding `.meta.<ce>` JSON. Writes
  are atomic (temp file + rename); bulk removal (purge/flush) is serialized against
  writes with an `RWMutex`.

**Cache key** = `<path>::<query-variant>::<encoding>`. The path/variant split makes
prefix purges segment-exact (`/post` never matches `/post-2`).

### Request flow (`ServeHTTP`)

1. Purge/flush API (`PURGE_PATH`) — **fails closed**: disabled unless `PURGE_KEY`
   is set; constant-time key compare; `401`/`403` on failure.
2. Non-`GET` → pass through.
3. Bypass rules: `BYPASS_DEBUG_QUERY`, `BYPASS_PATH_PREFIXES`, static-extension
   regex, `BYPASS_HOME`, or a `wordpress_logged_in*` cookie.
4. `QUERY_MODE`: `strip` (drop tracking params, then bypass if any query remains)
   or `include` (hash the normalized query into the key).
5. Lookup by `Accept-Encoding`; on hit, honor conditional requests (RFC 7232) and
   return `304`, else replay the stored response.
6. On miss, wrap the downstream response in a `CustomWriter` that decides
   cacheability and stores it on `Close`.

### What is never cached

Decided in `CustomWriter.WriteHeader` (`writer.go`): a non-cacheable status, any
`hdrResNotCacheList` header, or `responseForbidsCache` — i.e. `Set-Cookie`,
`Cache-Control: private|no-store|no-cache|max-age=0`, or `Vary: *|Cookie|Authorization`.
`5xx` is never cached via a wildcard (needs an exact 3-digit opt-in). A `populating`
guard elects one writer per key so concurrent misses don't double-store.

## Files

| File | Responsibility |
|------|----------------|
| `cache.go` | Module: `Provision`/`Validate`/`ServeHTTP`, Caddyfile parsing, query normalization, conditional requests, bounded background preload |
| `store.go` | Two-tier store: `Get`/`Set`/`Purge`/`Flush`/`List`, Otter memory tier, atomic disk I/O |
| `writer.go` | `CustomWriter`: cacheability decision, body buffering, leader election |
| `meta.go`   | `CacheMeta`, header allow/deny lists, `responseForbidsCache`, weak ETag + Last-Modified |
| `metrics.go`| Prometheus `wp_cache_events_total{outcome}` |

## Configuration (Caddyfile directive)

```
wp_cache {
    loc                  /var/www/html/wp-content/cache
    cache_response_codes 200,404,405
    ttl                  6000
    query_mode           strip          # strip | include
    purge_path           /__cache/purge
    purge_key            {$PURGE_KEY}
    purge_key_header     X-WPSidekick-Purge-Key
    bypass_home          false
    bypass_path_prefixes /wp-admin,/wp-json
    bypass_path_regex    ".*(\.[^.]+)$"
    bypass_debug_query   WPEverywhere-NOCACHE
    cache_header_name    X-FrankenWP-Cache
    tracking_params      utm_source,fbclid,gclid   # optional; overrides defaults
    memory_item_max_size 4194304
    memory_max_size      134217728
    memory_max_count     32768
}
```

Each option also reads a matching env var (see the repo README).

## Metrics

`wp_cache_events_total{outcome="hit|hit_304|miss|bypass|purge|flush"}` is registered
on the default Prometheus registry (gathered by Caddy's metrics endpoint).

## Development

There is no local Go required in this repo's workflow; run the toolchain in a
container. From this directory:

```sh
docker run --rm -v "$PWD":/src -w /src \
  -v wpcache-gomod:/go/pkg/mod -v wpcache-gobuild:/root/.cache/go-build \
  golang:1.26-alpine sh -c 'apk add --no-cache gcc musl-dev git >/dev/null && \
    go mod tidy && go vet ./... && go test -race ./...'
```

> **Note:** `go.mod`'s module path is `github.com/stephenmiracle/wpcache`, but the
> `Containerfile` builds it as `github.com/stephenmiracle/frankenwp/sidekick/middleware/cache`
> via an `xcaddy --with ...=./cache` local replace. Do **not** change the module
> name or that mapping.
