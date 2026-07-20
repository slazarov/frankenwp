# WordPress + FrankenPHP Docker Image

An enterprise-grade WordPress image built for scale. It uses the new FrankenPHP server bundled with Caddy. Lightning-fast server side caching Caddy module.

## Getting Started

- [Docker Images](https://hub.docker.com/r/wpeverywhere/frankenwp "Docker Hub")
- [Slack](https://join.slack.com/t/wpeverywhere/shared_invite/zt-2k88x3jtv-dpJHRYJ2IDT9PNQpO96zxQ "Slack")
- [Website](https://wpeverywhere.com)

### Examples

- [Standard environment with MariaDB & Docker Compose](./examples/basic/compose.yaml)
- [Debug with XDebug & Docker Compose](./examples/debug/compose.yaml)
- [SQLite with Docker Compose](./examples/sqlite/compose.yaml)

## Whats Included

### Services

- [WordPress](https://hub.docker.com/_/wordpress "WordPress Docker Image")
- [FrankenPHP](https://hub.docker.com/r/dunglas/frankenphp "FrankenPHP Docker Image")
- [Caddy](https://caddyserver.com/ "Caddy Server")

### Caching

- **OPcache + JIT** for PHP.
- **Sidekick full-page cache** — a custom Caddy handler (`wp_cache`) that stores rendered pages in a two-tier memory ([Otter](https://github.com/maypok86/otter) W-TinyLFU) + disk cache, shareable across containers. See [`sidekick/middleware/cache/README.md`](./sidekick/middleware/cache/README.md) for the full design.
- Optional **Cloudflare** edge purging via `Cache-Tag`.

Every response carries a status header (`X-FrankenWP-Cache` by default) with one of: `HIT`, `HIT-304`, `MISS`, `BYPASS`.

### Cache behavior (summary)

- Only anonymous `GET` responses with a cacheable status are stored.
- **Never cached:** logged-in requests (`wordpress_logged_in*` cookie), or responses with `Set-Cookie`, `Cache-Control: private/no-store/no-cache`, or `Vary: Cookie` — so personalized pages are never served to other users.
- **Query strings** (`QUERY_MODE`): `strip` (default) drops tracking params (`utm_*`, `fbclid`, `gclid`, …) so those URLs hit the clean cached page, then bypasses any request that still has a query; `include` keys on the normalized query instead.
- Conditional requests (`If-None-Match` / `If-Modified-Since`) return `304`.
- The purge API **fails closed**: it is disabled unless `PURGE_KEY` is set, and the key is compared in constant time.

### Purge API

- `POST {PURGE_PATH}/<relative-path>` with header `{PURGE_KEY_HEADER}: {PURGE_KEY}` purges that page (all query variants + encodings, segment-exact).
- `POST {PURGE_PATH}/` (empty path) flushes the entire cache.
- `GET {PURGE_PATH}` returns the current cache listing (also key-gated).

WordPress purges automatically on `save_post` (see `wp-content/mu-plugins/contentCachePurge.php`), and — if `CLOUDFLARE_ZONE_ID`/`CLOUDFLARE_API_TOKEN` are set — purges Cloudflare by `Cache-Tag`.

### Environment Variables

#### FrankenPHP

- `SERVER_NAME`: addresses to listen on; also used for the generated TLS cert.
- `CADDY_GLOBAL_OPTIONS`: inject global Caddy options.
- `FRANKENPHP_CONFIG`: inject config under the `frankenphp` directive.

#### Sidekick Cache

- `CACHE_LOC`: where to store cache. Default `/var/www/html/wp-content/cache`.
- `CACHE_RESPONSE_CODES`: status codes to cache (`200,404,405`). `000` disables caching. A bare `5XX` wildcard never caches; list exact 5xx codes to opt in.
- `TTL`: seconds to keep objects. Default `6000` (`0` = no expiry).
- `QUERY_MODE`: `strip` (default) or `include` — see above.
- `BYPASS_PATH_PREFIXES`: comma-separated path prefixes to never cache. Default `/wp-admin,/wp-json,/wp-login.php`.
- `BYPASS_HOME`: skip caching the home page. Default `false`.
- `BYPASS_DEBUG_QUERY`: query param that forces a bypass. Default `WPEverywhere-NOCACHE`.
- `PURGE_KEY`: **required to enable** the purge/flush endpoint (fails closed when empty). No default.
- `PURGE_KEY_HEADER`: header carrying the key. Default `X-WPSidekick-Purge-Key`.
- `PURGE_PATH`: purge API route. Default `/__cache/purge`.
- `CACHE_HEADER_NAME`: cache-status response header. Default `X-FrankenWP-Cache`.
- `CACHE_MEM_ITEM_SIZE` / `CACHE_MEM_ALL_SIZE` / `CACHE_MEM_ALL_COUNT`: per-item byte cap, total memory byte cap, and initial capacity hint.

#### Cloudflare (optional)

- `CLOUDFLARE_ZONE_ID` and `CLOUDFLARE_API_TOKEN` (token needs `Zone → Cache Purge`). When set, `save_post` purges by `Cache-Tag`.

#### WordPress

- `DB_NAME` / `DB_USER` / `DB_PASSWORD` / `DB_HOST` / `DB_TABLE_PREFIX`: database settings.
- `WP_DEBUG`: turns on WordPress debug.
- `FORCE_HTTPS`: tell WordPress to treat requests as HTTPS (useful behind a load balancer).
- `WORDPRESS_CONFIG_EXTRA`: extra `wp-config` (e.g. `WP_HOME`, `WP_SITEURL`).
- First-run auto-install (optional): `WP_URL`, `WP_TITLE`, `WP_ADMIN`, `WP_ADMIN_EMAIL`, `PLUGINS`.

### Versions

Pinned in `Containerfile` (Renovate keeps them current): FrankenPHP **1.12** · PHP **8.4** · WordPress **6.9** · Caddy **2.11.4**. The image runs as non-root `www-data` and builds for `linux/amd64,linux/arm64`.

### Upgrading (breaking changes)

- The cache-status header is now `X-FrankenWP-Cache` (was `X-Custom-Cache` / `X-WPEverywhere-Cache`). Override with `CACHE_HEADER_NAME`.
- Env var is `BYPASS_PATH_PREFIXES` (plural).
- The purge endpoint is **disabled unless `PURGE_KEY` is set** — set a strong key (`openssl rand -hex 32`) to keep purge-on-publish working.
- Example `CACHE_RESPONSE_CODES` is now `200,404,405` (was `000`, which disabled caching).

## Questions

### Why Not Just Use Standard WordPress Images?

The standard WordPress images are a good starting point and can handle many use cases, but require significant modification to scale. You also don't get FrankenPHP app server. Instead, you need to choose Apache or PHP-FPM. We use the WordPress base image but extend it with FrankenPHP & Caddy.

### Why FrankenPHP?

FrankenPHP is built on Caddy, a modern web server built in Go. It is secure & performs well when scaling becomes important. It also allows us to take advantage of built-in mature concurrency through goroutines into a single Docker image. high performance in a single lean image.

**[Check out FrankenPHP Here](https://frankenphp.dev/ "FrankenPHP")**

### Why is Non-Root User Important?

It is good practice to avoid using root users in your Docker images for security purposes. If a questionable individual gets access into your running Docker container with root account then they could have access to the cluster and all the resources it manages. This could be problematic. On the other hand, by creating a user specific to the Docker image, narrows the threat to only the image itself. It is also important to note that the base WordPress images also create non-root users by default.

### What are the Changes from Base FrankenPHP?

This custom Caddy build also includes an internal project named sidekick. It provides lightning fast cache that can be distributed among many containers. The default cache uses the local wp-content/cache directory but can use many cache services.

### How to use when behind load balancer or proxy?

_tldr: Use a port (ie :80, :8095, etc) for SERVER_NAME env variable._

Working in cloud environments like AWS can be tricky because your traffic is going through a load balancer or some proxy. This means your server name is not what you think your server name is. Your domain hits a proxy dns entry that then hits your application. The application doesn't know your domain. It knows the proxied name. This may seem strange, but it's actually a well established strong architecture pattern.

What about SSL cert? Use `SERVER_NAME=mydomain.com, :80`
Caddy, the underlying application server is flexible enough for multiple entries. Separate multiple values with a comma. It will still request certificate.

## Using in Real Projects? Join the Chat

You can join our Slack chat to ask questions or connect directly. [Connect on Slack](https://join.slack.com/t/wpeverywhere/shared_invite/zt-2k88x3jtv-dpJHRYJ2IDT9PNQpO96zxQ)
