<?php
/**
 * Plugin Name:     Consent-Suppressed No-Cache
 * Author:          Stephen Miracle
 * Description:     Skip the full-page cache on pages whose embeds (e.g. a Google
 *                  Maps iframe) are held blank until the visitor's consent JS
 *                  activates them. Such pages render per-visitor, so caching one
 *                  variant in the shared cache serves a permanently-blank embed
 *                  (or leaks a consented embed) to everyone.
 * Version:         0.2.0
 *
 * The decision is made at the HTTP-header level (Cache-Control), which the
 * FrankenWP cache honors via responseForbidsCache. This is deliberate: the
 * cache buffers the response *after* compression, so it cannot inspect body
 * content itself — the page must declare that it is uncacheable.
 *
 * Which pages? Provide an explicit allowlist via the `frankenwp_no_cache_slugs`
 * filter (matches post slug or ID), or full custom logic via the
 * `frankenwp_should_skip_cache` filter. Auto-detecting "a suppressed embed" is
 * intentionally NOT attempted: the suppression attributes (data-suppressedsrc,
 * _iub_cs_activate, …) are injected by the consent plugin during content
 * rendering, which happens *after* headers are sent — so they are not reliably
 * visible at header time. An explicit allowlist is the dependable signal.
 */

if (!defined('ABSPATH')) {
    exit;
}

/**
 * Decide whether the current request must skip the full-page cache.
 */
function frankenwp_request_skips_cache(): bool
{
    $skip = false;

    if (is_singular()) {
        $post = get_queried_object();

        if ($post instanceof WP_Post) {
            // Explicit allowlist of slugs and/or IDs. Empty by default — add
            // your page(s), e.g.
            //   add_filter('frankenwp_no_cache_slugs', fn($s) => [...$s, 'lokatsiya']);
            $slugs = (array) apply_filters('frankenwp_no_cache_slugs', []);
            if (in_array($post->post_name, $slugs, true) || in_array($post->ID, $slugs, true)) {
                $skip = true;
            }
        }
    }

    // Escape hatch for arbitrary conditions (templates, taxonomies, query vars…).
    return (bool) apply_filters('frankenwp_should_skip_cache', $skip);
}

// send_headers is WordPress's purpose-built hook for response headers; it fires
// before any template output, so setting Cache-Control here is safe and final
// (WordPress does not emit Cache-Control on anonymous front-end pages).
add_action('send_headers', function () {
    if (headers_sent() || !frankenwp_request_skips_cache()) {
        return;
    }

    // private + no-store: FrankenWP's cache treats either as a hard bypass.
    // Cloudflare honors these to skip its shared cache when Origin Cache Control
    // is respected (default on Free/Pro/Business). If you run a "Cache
    // Everything" rule with an explicit Edge Cache TTL, add a Cache Rule to
    // bypass this path too — the TTL can otherwise override origin headers.
    header('Cache-Control: private, no-store, no-cache, max-age=0');
    header('X-FrankenWP-Cache: BYPASS');
});
