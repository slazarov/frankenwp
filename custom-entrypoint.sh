#!/bin/bash
set -e

# Run original entrypoint to unpack WP
/usr/local/bin/docker-entrypoint.sh php-fpm &

# Wait for WP to exist
until [ -f /var/www/html/wp-config.php ] || [ -f /var/www/html/wp-settings.php ]; do
    echo "Waiting for WordPress core..."
    sleep 2
done

# Install plugin
wp plugin install redis-cache --allow-root --activate --path=/var/www/html

wait