# AGENTS.md

Guidance for coding agents working in this repository. Humans: see `README.md`.

## What this is

**FrankenWP** — a production WordPress Docker image built on **FrankenPHP + Caddy**,
plus a custom Caddy full-page cache module written in Go. It is *both* a Docker
image and a Go/Caddy module (the module is compiled into the FrankenPHP binary via
`xcaddy`).

## Layout

| Path | What |
|------|------|
| `Containerfile` | Multi-stage build (the primary build file). Builder compiles FrankenPHP+modules with `xcaddy`; final stage assembles the WordPress image. |
| `Caddyfile` | Runtime server config (TLS, static headers, `/healthz`, the `wp_cache` directive, `php_server`). |
| `php.ini` | PHP/OPcache/JIT tuning (copied to `conf.d/wp.ini`). |
| `plugins-install.sh` | First-run WP install + plugin install, injected into the WP entrypoint. |
| `sidekick/middleware/cache/` | **The Go module** (`wp_cache`). See its `README.md`. |
| `wp-content/mu-plugins/` | Must-use PHP plugins: cache purge (+Cloudflare), cache-tag emitter, URL rewrite, SMTP. |
| `examples/` | `basic` (MariaDB), `debug` (Xdebug), `sqlite` deployment recipes. |
| `.github/workflows/` | `ci.yml` (lint/test/scan) and `build.yml` (multi-arch build+publish+Trivy). |

## Build & run

```sh
# Build the image (defaults: FrankenPHP 1.12, PHP 8.4, WordPress 6.9)
docker build -f Containerfile -t frankenwp:test .

# Run an example stack
docker compose -f examples/basic/compose.yaml up
```

## Testing the Go module (no local Go — use a container)

This environment has Docker but **not** a local Go toolchain. Run Go tooling in a
`golang` container from `sidekick/middleware/cache/`:

```sh
docker run --rm -v "$PWD":/src -w /src \
  -v wpcache-gomod:/go/pkg/mod -v wpcache-gobuild:/root/.cache/go-build \
  golang:1.26-alpine sh -c 'apk add --no-cache gcc musl-dev git >/dev/null && \
    go mod tidy && go vet ./... && go test -race ./...'
```

Lint PHP mu-plugins: `docker run --rm -v "$PWD/wp-content/mu-plugins":/p -w /p php:8.4-cli-alpine sh -c 'for f in *.php; do php -l "$f"; done'`.

Full end-to-end verification (real WP): bring up `examples/basic` (or the compose in
`README`), install WordPress, then assert the `X-FrankenWP-Cache` header for
MISS→HIT / BYPASS / 304 / purge. The DB must be healthy *before* WordPress starts
(gate with `depends_on: { db: { condition: service_healthy } }`), otherwise the
WP entrypoint's `wp core install` fails under `set -e`.

## Constraints & gotchas (read before editing)

- **Do not change** the `go.mod` module name (`github.com/stephenmiracle/wpcache`)
  or the `Containerfile` `--with …=./cache` mapping — the build relies on that
  local replace. See the module README.
- After changing Go imports, regenerate `go.mod`/`go.sum` with `go mod tidy` in the
  container and commit both. The builder stage has network for `go mod download`.
- The image must run as **non-root `www-data`**. A global `ARG` (declared before the
  first `FROM`) is **not** visible inside a build stage unless re-declared — that is
  why `ARG USER`/`ARG DEBUG` are re-declared in the final stage.
- `plugins-install.sh` runs under the WP entrypoint's `set -euo pipefail`; guard
  every optional env var with `${VAR:-}`.
- **Caddyfile directive order** matters: `wp_cache` runs `before rewrite`, `respond
  before wp_cache`. Keep the `order` block intact.
- Cacheability decisions live in `writer.go:WriteHeader` (headers are guaranteed
  present there); validator/meta persistence in `Close`.
- Config defaults were reconciled (breaking): status header `X-FrankenWP-Cache`,
  env `BYPASS_PATH_PREFIXES` (plural), purge fails closed without `PURGE_KEY`.

## Conventions

- Go: standard `gofmt`; keep changes surgical and match surrounding style. Every
  detached goroutine must be wrapped with `safeGo` (panic recovery).
- New Go behavior needs a test in the same package (`*_test.go`, run with `-race`).
- PHP mu-plugins must pass `php -l` and guard against `set -u` / missing env.
- Don't commit or push unless asked.
