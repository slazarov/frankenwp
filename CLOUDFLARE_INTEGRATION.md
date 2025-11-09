# Cloudflare Cache Integration

This document explains how to integrate Cloudflare cache purging with FrankenWP.

## Overview

When you publish or update content in WordPress, FrankenWP now automatically purges both:
1. **Local FrankenWP cache** (in-memory + disk cache)
2. **Cloudflare edge cache** (if configured)

This ensures your content is fresh across all caching layers.

## Setup Instructions

### Step 1: Get Your Cloudflare Credentials

1. **Zone ID:**
   - Log in to [Cloudflare Dashboard](https://dash.cloudflare.com/)
   - Select your domain
   - Find the Zone ID in the right sidebar under "API" section
   - Copy the Zone ID (format: `abc123def456...`)

2. **API Token:**
   - Go to [API Tokens](https://dash.cloudflare.com/profile/api-tokens)
   - Click "Create Token"
   - Use the "Edit zone DNS" template or create a custom token with:
     - **Permissions:** `Zone → Cache Purge → Purge`
     - **Zone Resources:** Include → Specific zone → Your domain
   - Click "Continue to summary" → "Create Token"
   - **IMPORTANT:** Copy the token immediately (you won't see it again)

### Step 2: Configure Environment Variables

#### Option A: Using Docker Compose with .env file

1. Create a `.env` file in your project root:
   ```bash
   cp .env.example .env
   ```

2. Edit `.env` and add your Cloudflare credentials:
   ```bash
   CLOUDFLARE_ZONE_ID=your_cloudflare_zone_id_here
   CLOUDFLARE_API_TOKEN=your_cloudflare_api_token_here
   ```

3. Make sure your `compose.yaml` includes these environment variables (already added in `examples/basic/compose.yaml`):
   ```yaml
   services:
     wordpress:
       environment:
         CLOUDFLARE_ZONE_ID: ${CLOUDFLARE_ZONE_ID:-}
         CLOUDFLARE_API_TOKEN: ${CLOUDFLARE_API_TOKEN:-}
   ```

4. Restart your containers:
   ```bash
   docker compose down
   docker compose up -d
   ```

#### Option B: Using Systemd Quadlet

1. Create or edit your `.container` file (e.g., `frankenwp.container`):
   ```ini
   [Container]
   Image=wpeverywhere/frankenwp:latest-php8.3
   Environment=CLOUDFLARE_ZONE_ID=your_zone_id_here
   Environment=CLOUDFLARE_API_TOKEN=your_api_token_here
   # ... other environment variables
   ```

2. Reload systemd and restart:
   ```bash
   systemctl --user daemon-reload
   systemctl --user restart frankenwp
   ```

#### Option C: Direct Environment Variables

You can also set these directly in your shell or container runtime:
```bash
export CLOUDFLARE_ZONE_ID="your_zone_id_here"
export CLOUDFLARE_API_TOKEN="your_api_token_here"
```

### Step 3: Verify Integration

1. **Check logs** after publishing a post:
   ```bash
   docker compose logs -f wordpress
   ```

2. Look for log messages like:
   ```
   Successfully purged Cloudflare cache for: https://yoursite.com/your-post/
   ```

3. If there are errors, check:
   - Zone ID is correct
   - API token has Cache Purge permissions
   - Token is for the correct zone

## How It Works

The integration is implemented in `/wp-content/mu-plugins/contentCachePurge.php`:

1. **On content save** (`save_post` hook):
   - Purges local FrankenWP cache via internal API
   - Calls `purge_cloudflare_cache()` function

2. **Cloudflare purge function:**
   - Checks if `CLOUDFLARE_ZONE_ID` and `CLOUDFLARE_API_TOKEN` are set
   - If not configured, silently skips (no errors)
   - Makes API request to Cloudflare: `POST /zones/{zone_id}/purge_cache`
   - Purges specific URL (not entire cache)
   - Logs success/failure to PHP error log

## Optional: Disable Cloudflare Integration

To disable Cloudflare cache purging:
- Simply remove or comment out the `CLOUDFLARE_*` environment variables
- The plugin will detect they're missing and skip Cloudflare purging
- Local FrankenWP cache purging continues to work normally

## Troubleshooting

### Error: "Cloudflare cache purge failed"

Check your logs for specific error messages:

```bash
docker compose logs wordpress | grep -i cloudflare
```

Common issues:

1. **Invalid API token:**
   - Error: `Authentication error`
   - Solution: Regenerate API token with correct permissions

2. **Wrong Zone ID:**
   - Error: `Zone not found`
   - Solution: Double-check Zone ID from Cloudflare dashboard

3. **Insufficient permissions:**
   - Error: `Permission denied`
   - Solution: Ensure API token has `Zone → Cache Purge → Purge` permission

4. **Rate limiting:**
   - Error: `Rate limit exceeded`
   - Solution: Cloudflare has rate limits; wait a few minutes

### No Cloudflare logs appearing

If you don't see any Cloudflare-related logs:
- Check that environment variables are set: `docker compose exec wordpress printenv | grep CLOUDFLARE`
- Verify the mu-plugin is loaded: Check WordPress admin → Plugins (mu-plugins section)

## Performance Notes

- **Async operation:** Cache purging happens synchronously during save (minor delay)
- **Timeout:** Cloudflare API calls timeout after 15 seconds
- **Failure handling:** If Cloudflare purge fails, local cache still purges successfully
- **Cost:** Cloudflare cache purging is included in all plans (Free, Pro, Business, Enterprise)

## Security Best Practices

1. **Never commit `.env` files** to version control
2. **Use API tokens (not API keys):** Tokens are more secure and can be scoped
3. **Minimum permissions:** Only grant "Cache Purge" permission to the token
4. **Rotate tokens regularly:** Generate new tokens every 90 days
5. **Monitor usage:** Check Cloudflare audit logs for suspicious activity

## Advanced: Purge Multiple URLs

The current implementation purges only the specific post URL. To purge related URLs (e.g., homepage, category archives), modify `contentCachePurge.php`:

```php
add_action("save_post", function ($id) {
    $urls_to_purge = [
        get_permalink($id),           // Post URL
        home_url(),                    // Homepage
        get_category_link(get_the_category($id)[0]->term_id), // Category
        // Add more URLs as needed
    ];

    foreach ($urls_to_purge as $url) {
        // Purge local cache
        $purge_url = get_site_url() . $_SERVER["PURGE_PATH"] . wp_make_link_relative($url) . "/";
        wp_remote_post($purge_url, [...]);

        // Purge Cloudflare cache
        purge_cloudflare_cache($url);
    }
});
```

## Further Reading

- [Cloudflare API Documentation](https://developers.cloudflare.com/api/operations/zone-purge)
- [FrankenWP Cache Documentation](README.md)
- [WordPress Caching Best Practices](https://wordpress.org/support/article/optimization/)
