ARG WORDPRESS_VERSION=latest
ARG PHP_VERSION=8.4.11
ARG USER=www-data

FROM docker.io/dunglas/frankenphp:builder-php${PHP_VERSION} as builder

# Copy xcaddy in the builder image
COPY --from=caddy:builder /usr/bin/xcaddy /usr/bin/xcaddy

COPY ./sidekick/middleware/cache ./cache

RUN CGO_ENABLED=1 \
    XCADDY_SETCAP=1 \
    XCADDY_GO_BUILD_FLAGS="-ldflags='-w -s' -tags=nobadger,nomysql,nopgx" \
    CGO_CFLAGS=$(php-config --includes) \
    CGO_LDFLAGS="$(php-config --ldflags) $(php-config --libs)" \
    xcaddy build \
    --output /usr/local/bin/frankenphp \
    --with github.com/dunglas/frankenphp=./ \
    --with github.com/dunglas/frankenphp/caddy=./caddy/ \
    --with github.com/dunglas/caddy-cbrotli \
    # Add extra Caddy modules here
    --with github.com/stephenmiracle/frankenwp/sidekick/middleware/cache=./cache

FROM docker.io/wordpress:$WORDPRESS_VERSION as wp
FROM docker.io/dunglas/frankenphp:php${PHP_VERSION} AS base

LABEL org.opencontainers.image.title=FrankenWP \
      org.opencontainers.image.description="Optimized WordPress containers to run everywhere. Built with FrankenPHP & Caddy." \
      org.opencontainers.image.url=https://wpeverywhere.com \
      org.opencontainers.image.source=https://github.com/StephenMiracle/frankenwp \
      org.opencontainers.image.licenses=MIT \
      org.opencontainers.image.vendor="Stephen Miracle"

# Replace the official binary by the one contained your custom modules
COPY --from=builder /usr/local/bin/frankenphp /usr/local/bin/frankenphp

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
    && install-php-extensions \
    bcmath \
    exif \
    gd \
    intl \
    mysqli \
    zip \
    # See https://github.com/Imagick/imagick/issues/640#issuecomment-2077206945
    imagick/imagick@master \
    opcache \
    redis \
    && cp $PHP_INI_DIR/php.ini-production $PHP_INI_DIR/php.ini \
    && apt-get purge -y --auto-remove -o APT::AutoRemove::RecommendsImportant=false \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/*

COPY php.ini $PHP_INI_DIR/conf.d/wp.ini
COPY custom-entrypoint.sh /usr/local/bin/
COPY --from=wp /usr/src/wordpress /usr/src/wordpress
COPY --from=wp /usr/local/etc/php/conf.d /usr/local/etc/php/conf.d/
COPY --from=wp /usr/local/bin/docker-entrypoint.sh /usr/local/bin/
COPY plugins-install.sh /tmp/plugins-install.sh
COPY wp-content/mu-plugins /var/www/html/wp-content/mu-plugins

WORKDIR /var/www/html

# Modify scripts, create directories, install WP-CLI, and clean up in one layer
RUN mkdir -p /var/www/html/wp-content/cache /var/www/html/wp-content/uploads /var/www/html/wp-content/plugins \
    && sed -i \
    -e 's/\[ "$1" = '\''php-fpm'\'' \]/\[\[ "$1" == frankenphp* \]\]/g' \
    -e 's/php-fpm/frankenphp/g' \
    /usr/local/bin/docker-entrypoint.sh \
    && sed -i '/exec "$@"/e cat /tmp/plugins-install.sh' /usr/local/bin/docker-entrypoint.sh \
    && rm /tmp/plugins-install.sh \
    && sed -i 's/<?php/<?php if (!!getenv("FORCE_HTTPS")) { \$_SERVER["HTTPS"] = "on"; } define( "FS_METHOD", "direct" ); set_time_limit(300); /g' /usr/src/wordpress/wp-config-docker.php \
    && curl -O https://raw.githubusercontent.com/wp-cli/builds/gh-pages/phar/wp-cli.phar \
    && chmod +x wp-cli.phar \
    && mv wp-cli.phar /usr/local/bin/wp

# Declare volume after preparing directories
VOLUME /var/www/html/wp-content

COPY Caddyfile /etc/caddy/Caddyfile

# Set user, capabilities, and permissions in one layer
RUN useradd -D ${USER} \
    && setcap CAP_NET_BIND_SERVICE=+eip /usr/local/bin/frankenphp \
    && chown -R ${USER}:${USER} /data/caddy /config/caddy /var/www/html /usr/src/wordpress /usr/local/bin/docker-entrypoint.sh

USER $USER

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["frankenphp", "run", "--config", "/etc/caddy/Caddyfile"]