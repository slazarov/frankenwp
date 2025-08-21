# Install WordPress if not yet installed
if ! wp core is-installed --path=/var/www/html --allow-root >/dev/null 2>&1; then
    echo "Installing WordPress..."
    wp core install --url="$WP_URL" --title="$WP_TITLE" \
        --admin_user="$WP_ADMIN" --admin_password="$WP_ADMIN_PASS" \
        --admin_email="$WP_ADMIN_EMAIL" --path=/var/www/html --allow-root
fi

# Install plugins in background
if [ -n "$PLUGINS" ]; then
    (
    IFS=',' read -ra PLUGIN_ARRAY <<<"$PLUGINS"
    for plugin in "${PLUGIN_ARRAY[@]}"; do
        plugin=$(echo "$plugin" | xargs)
        if ! wp plugin is-installed "$plugin" --path=/var/www/html --allow-root >/dev/null 2>&1; then
            echo "Installing plugin: $plugin"
            wp plugin install "$plugin" --activate --path=/var/www/html --allow-root
        fi
    done
    ) &
fi

# Start main container process
exec "$@"