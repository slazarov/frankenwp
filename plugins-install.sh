if [ -n "$PLUGINS" ]; then
	IFS=',' read -ra PLUGIN_ARRAY <<<"$PLUGINS"
	for plugin in "${PLUGIN_ARRAY[@]}"; do
		plugin=$(echo "$plugin" | xargs)
		if ! wp plugin is-installed "$plugin" --path=/var/www/html --allow-root >/dev/null 2>&1; then
			echo "Installing plugin: $plugin"
			wp plugin install "$plugin" --activate --path=/var/www/html --allow-root
		fi
	done
fi