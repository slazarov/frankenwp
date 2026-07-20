# Pinned for reproducible builds. Renovate keeps these current (see renovate.json).
ARG WORDPRESS_VERSION=6.9
ARG PHP_VERSION=8.4
ARG FRANKENPHP_VERSION=1.12
ARG USER=www-data

FROM docker.io/dunglas/frankenphp:${FRANKENPHP_VERSION}-builder-php${PHP_VERSION}-bookworm AS builder

# Copy xcaddy in the builder image
COPY --from=docker.io/library/caddy:builder /usr/bin/xcaddy /usr/bin/xcaddy

# build cache for FrankenPHP
RUN --mount=type=cache,target=/root/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

# build cache for sidekick cache
COPY ./sidekick/middleware/cache/go.mod ./sidekick/middleware/cache/go.sum ./cache/
RUN --mount=type=cache,target=/root/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    cd ./cache/ && go mod download

COPY ./sidekick/middleware/cache ./cache

# CGO must be enabled to build FrankenPHP
RUN --mount=type=cache,target=/root/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 XCADDY_SETCAP=1 XCADDY_GO_BUILD_FLAGS='-ldflags="-w -s" -trimpath' \
    CGO_CFLAGS=$(php-config --includes) \
    CGO_LDFLAGS="$(php-config --ldflags) $(php-config --libs)" \
    xcaddy build \
    --output /usr/local/bin/frankenphp \
    --with github.com/dunglas/frankenphp=./ \
    --with github.com/dunglas/frankenphp/caddy=./caddy/ \
    --with github.com/dunglas/caddy-cbrotli \
    # Add extra Caddy modules here
    --with github.com/stephenmiracle/frankenwp/sidekick/middleware/cache=./cache

FROM docker.io/wordpress:${WORDPRESS_VERSION} AS wp
FROM docker.io/dunglas/frankenphp:${FRANKENPHP_VERSION}-php${PHP_VERSION}-bookworm AS base

LABEL org.opencontainers.image.title=FrankenWP \
      org.opencontainers.image.description="Optimized WordPress containers to run everywhere. Built with FrankenPHP & Caddy." \
      org.opencontainers.image.url=https://wpeverywhere.com \
      org.opencontainers.image.source=https://github.com/StephenMiracle/frankenwp \
      org.opencontainers.image.licenses=MIT \
      org.opencontainers.image.vendor="Stephen Miracle"

# Replace the official binary by the one contained your custom modules
COPY --from=builder /usr/local/bin/frankenphp /usr/local/bin/frankenphp

ARG DEBUG=""
ENV WP_DEBUG=${DEBUG:+1} \
    FORCE_HTTPS=0 \
    PHP_INI_SCAN_DIR=$PHP_INI_DIR/conf.d

# Install dependencies, PHP extensions, set base php.ini, and clean up in one layer
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    ghostscript \
    curl \
    libonig-dev \
    libxml2-dev \
    libcurl4-openssl-dev \
    libssl-dev \
    libzip-dev \
    unzip \
    git \
    libjpeg-dev \
    libwebp-dev \
    libmemcached-dev \
    zlib1g-dev \
    libnss3-tools \
    # install-php-extensions is idempotent; retry to ride out transient
    # pecl.php.net / GitHub 5xx outages instead of failing the whole build.
    # imagick from master: https://github.com/Imagick/imagick/issues/640#issuecomment-2077206945
    && n=0; until install-php-extensions \
    bcmath \
    exif \
    gd \
    intl \
    mysqli \
    zip \
    imagick/imagick@master \
    opcache \
    redis; do \
    n=$((n+1)); \
    if [ "$n" -ge 5 ]; then echo "install-php-extensions failed after $n attempts" >&2; exit 1; fi; \
    echo "install-php-extensions attempt $n failed; retrying in 15s..." >&2; sleep 15; \
    done \
    && cp $PHP_INI_DIR/php.ini-production $PHP_INI_DIR/php.ini \
    && apt-get purge -y --auto-remove -o APT::AutoRemove::RecommendsImportant=false \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/*

COPY php.ini $PHP_INI_DIR/conf.d/wp.ini
COPY --from=wp /usr/src/wordpress /usr/src/wordpress
COPY --from=wp /usr/local/etc/php/conf.d /usr/local/etc/php/conf.d/
COPY --from=wp /usr/local/bin/docker-entrypoint.sh /usr/local/bin/
COPY plugins-install.sh /tmp/plugins-install.sh
COPY wp-content/mu-plugins /var/www/html/wp-content/mu-plugins

WORKDIR /var/www/html

# Modify scripts, create directories, install WP-CLI, and clean up in one layer
RUN mkdir -p /var/www/html/wp-content/cache \
    && sed -i -e 's/\[ "$1" = '\''php-fpm'\'' \]/\[\[ "$1" == frankenphp* \]\]/g' \
           -e 's/php-fpm/frankenphp/g' \
           /usr/local/bin/docker-entrypoint.sh \
    && sed -i '/exec "\$@"/e cat /tmp/plugins-install.sh && echo ""' /usr/local/bin/docker-entrypoint.sh \
    && rm /tmp/plugins-install.sh \
    && sed -i 's/<?php/<?php if (!!getenv("FORCE_HTTPS")) { \$_SERVER["HTTPS"] = "on"; } define( "FS_METHOD", "direct" ); set_time_limit(300); /g' /usr/src/wordpress/wp-config-docker.php \
    && curl -O https://raw.githubusercontent.com/wp-cli/builds/gh-pages/phar/wp-cli.phar \
    && chmod +x wp-cli.phar \
    && mv wp-cli.phar /usr/local/bin/wp

# Declare volume after preparing directories
VOLUME /var/www/html/wp-content

# Force 0644 so the config is always world-readable regardless of the build
# host's umask/source perms — frankenphp runs as www-data and must read it.
COPY --chmod=644 Caddyfile /etc/caddy/Caddyfile

# Re-declare here: a global ARG (before the first FROM) is not expanded inside a
# build stage unless re-declared. Without this, ${USER} is empty and the image
# would run as root.
ARG USER=www-data

# Set user, capabilities, and permissions in one layer. www-data already exists
# in the base image; create it only if a custom USER was supplied.
RUN (id -u ${USER} >/dev/null 2>&1 || useradd -m ${USER}) \
    && setcap CAP_NET_BIND_SERVICE=+eip /usr/local/bin/frankenphp \
    && mkdir -p /data/caddy /config/caddy \
    && chown -R ${USER}:${USER} /data/caddy /config/caddy /var/www/html /usr/src/wordpress /usr/local/bin/docker-entrypoint.sh

USER $USER

# Liveness/readiness probe hits the lightweight /healthz route (served by Caddy
# without invoking PHP). See Caddyfile.
HEALTHCHECK --interval=30s --timeout=5s --start-period=45s --retries=3 \
    CMD curl -fsS http://127.0.0.1/healthz || exit 1

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["frankenphp", "run", "--config", "/etc/caddy/Caddyfile"]