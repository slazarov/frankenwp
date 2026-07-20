<?php
/**
 * Plugin Name:     FrankenWP Cache Tags
 * Author:          FrankenWP
 * Description:     Emits Cache-Tag response headers so a CDN (e.g. Cloudflare) can
 *                  purge related pages (post, its archives, taxonomy, home) by tag.
 * Version:         0.1.0
 */

if (!defined('ABSPATH')) {
    exit;
}

/**
 * Compute the cache tags for the current request.
 *
 * @return string[]
 */
function frankenwp_cache_tags_for_request() {
    $tags = [];

    if (is_front_page() || is_home()) {
        $tags[] = 'home';
    }

    if (is_singular()) {
        $id = get_queried_object_id();
        if ($id) {
            $tags[] = 'post-' . $id;
            $tags[] = 'post-type-' . get_post_type($id);
        }
    }

    if (is_category() || is_tag() || is_tax()) {
        $term = get_queried_object();
        if ($term && isset($term->term_id)) {
            $tags[] = 'term-' . $term->term_id;
        }
    }

    if (is_author()) {
        $tags[] = 'author-' . get_queried_object_id();
    }

    return apply_filters('frankenwp_cache_tags', $tags);
}

add_action('send_headers', function () {
    // Only tag anonymous, cacheable GET responses.
    if (is_admin() || is_user_logged_in()) {
        return;
    }
    if (isset($_SERVER['REQUEST_METHOD']) && $_SERVER['REQUEST_METHOD'] !== 'GET') {
        return;
    }

    $tags = frankenwp_cache_tags_for_request();
    if (empty($tags)) {
        return;
    }

    // Cache-Tag: printable ASCII, comma-separated, <= 16KB / ~1000 tags.
    $tags = array_slice(array_values(array_unique($tags)), 0, 100);
    header('Cache-Tag: ' . implode(',', $tags));
}, 100);
