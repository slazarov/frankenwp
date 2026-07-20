# Install WordPress if not yet installed and the required vars are provided.
# Guard every variable with ${VAR:-} because the entrypoint runs under `set -u`.
if ! wp core is-installed --path=/var/www/html --allow-root >/dev/null 2>&1; then
    if [ -n "${WP_URL:-}" ] && [ -n "${WP_TITLE:-}" ] && [ -n "${WP_ADMIN:-}" ] && [ -n "${WP_ADMIN_EMAIL:-}" ]; then
        echo "Installing WordPress..."
        wp core install --url="$WP_URL" \
            --title="$WP_TITLE" \
            --admin_user="$WP_ADMIN" \
            --admin_email="$WP_ADMIN_EMAIL" \
            --path=/var/www/html --allow-root
    else
        echo "Skipping automatic WordPress install (set WP_URL, WP_TITLE, WP_ADMIN, WP_ADMIN_EMAIL to enable)."
    fi
fi

# Install plugins in the background
if [ -n "${PLUGINS:-}" ]; then
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
