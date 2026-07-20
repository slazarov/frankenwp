<?php
/**
 * Plugin Name:     Consent-Suppressed No-Cache
 * Author:          Stephen Miracle
 * Description:     Automatically skip the full-page cache on pages whose embeds
 *                  (e.g. a Google Maps iframe) are held blank until the
 *                  visitor's consent JS activates them. Such pages render
 *                  per-visitor, so caching one variant in the shared cache
 *                  serves a permanently-blank embed (or leaks a consented one)
 *                  to everyone.
 * Version:         0.3.0
 *
 * How it works — WORKS BY DEFAULT, no configuration required:
 *   The suppression markers (data-suppressedsrc, _iub_cs_activate, cmplazyload)
 *   only exist in the *rendered* HTML, which is produced after WordPress would
 *   normally send its headers. So we open an output buffer before any output
 *   (template_redirect) and inspect the full page in the buffer's flush
 *   callback. Because nothing is sent to the client until the buffer flushes,
 *   we can still set Cache-Control there. When a marker is present we emit
 *   `Cache-Control: private, no-store`, which the FrankenWP cache honors as a
 *   hard bypass (responseForbidsCache) — and Cloudflare honors too when Origin
 *   Cache Control is respected.
 *
 *   This runs at the WordPress layer on the uncompressed body, which is the
 *   correct place: the Caddy cache buffers the response *after* compression and
 *   cannot inspect content itself.
 *
 * Overrides:
 *   - `frankenwp_no_cache_slugs`     — force-bypass specific slugs/IDs (useful
 *                                      when suppression is done purely in JS and
 *                                      leaves no server-side marker).
 *   - `frankenwp_suppressed_markers` — customize the detected marker list.
 *   - `frankenwp_should_skip_cache`  — final boolean override for any condition.
 */

if (!defined('ABSPATH')) {
    exit;
}

/**
 * The byte markers that indicate a consent-suppressed / lazy-activated embed.
 * Deliberately narrow: these appear only on the suppressed element itself, not
 * on every page (unlike site-wide consent-banner scripts), so matching them
 * does not disable caching site-wide.
 */
function frankenwp_suppressed_markers(): array
{
    return (array) apply_filters('frankenwp_suppressed_markers', [
        'data-suppressedsrc', // CMP-suppressed iframe source (Iubenda, etc.)
        '_iub_cs_activate',   // Iubenda consent activation hook
        'cmplazyload',        // CMP lazy-load activation class
    ]);
}

function frankenwp_html_has_suppressed_embed(string $html): bool
{
    foreach (frankenwp_suppressed_markers() as $marker) {
        if ($marker !== '' && str_contains($html, $marker)) {
            return true;
        }
    }
    return false;
}

/**
 * Emit the cache-bypass headers. Safe to call more than once (header() replaces).
 */
function frankenwp_send_no_cache_headers(): void
{
    if (headers_sent()) {
        return;
    }
    // private + no-store: FrankenWP treats either as a hard bypass; Cloudflare
    // skips its shared cache when Origin Cache Control is respected (default on
    // Free/Pro/Business). With a "Cache Everything" rule + explicit Edge Cache
    // TTL, add a Cloudflare Cache Rule to bypass the path too.
    header('Cache-Control: private, no-store, no-cache, max-age=0');
    header('X-FrankenWP-Cache: BYPASS');
}

/**
 * Output-buffer callback: inspect the fully rendered page and bypass the cache
 * if it carries a suppressed embed. Runs before the buffer is sent, so setting
 * headers here is valid.
 */
function frankenwp_consent_nocache_filter(string $html): string
{
    if (frankenwp_html_has_suppressed_embed($html)) {
        frankenwp_send_no_cache_headers();
    }
    return $html;
}

add_action('template_redirect', function () {
    // Only buffer ordinary front-end HTML page views.
    if (is_admin() || is_feed() || is_robots() || is_trackback()) {
        return;
    }
    if (($_SERVER['REQUEST_METHOD'] ?? 'GET') !== 'GET') {
        return;
    }
    if ((defined('DOING_AJAX') && DOING_AJAX)
        || (defined('REST_REQUEST') && REST_REQUEST)
        || (defined('WP_CLI') && WP_CLI)
        || (defined('XMLRPC_REQUEST') && XMLRPC_REQUEST)) {
        return;
    }

    // Optional explicit allowlist — handles JS-only suppression that leaves no
    // server-side marker for the content scan to find.
    $slugs = (array) apply_filters('frankenwp_no_cache_slugs', []);
    if ($slugs && is_singular()) {
        $post = get_queried_object();
        if ($post instanceof WP_Post
            && (in_array($post->post_name, $slugs, true) || in_array($post->ID, $slugs, true))) {
            frankenwp_send_no_cache_headers();
        }
    }

    if (apply_filters('frankenwp_should_skip_cache', false)) {
        frankenwp_send_no_cache_headers();
    }

    // Automatic content-based detection for everything else.
    ob_start('frankenwp_consent_nocache_filter');
}, 0);
