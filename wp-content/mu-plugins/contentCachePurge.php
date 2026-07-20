<?php
/**
 * Plugin Name:     Content Cache Purge
 * Author:          Stephen Miracle
 * Description:     Purge local FrankenWP + Cloudflare cache when content changes.
 * Version:         0.3.0
 */

if (!defined('ABSPATH')) {
    exit;
}

add_action('save_post', function ($id, $post = null) {
    if (wp_is_post_revision($id) || wp_is_post_autosave($id)) {
        return;
    }
    if ($post === null) {
        $post = get_post($id);
    }
    if (!$post) {
        return;
    }

    $link = get_permalink($id);

    // Local FrankenWP full-page cache keys by URL, so purge the post and the
    // home page (archives fall off via TTL).
    if ($link) {
        frankenwp_local_purge(wp_make_link_relative($link));
    }
    frankenwp_local_purge('/');

    // Cloudflare edge: purge by tag so archives / taxonomy / home invalidate too.
    $tags = ['home', 'post-' . $id, 'post-type-' . get_post_type($id)];
    foreach (get_object_taxonomies($post->post_type) as $taxonomy) {
        $term_ids = wp_get_post_terms($id, $taxonomy, ['fields' => 'ids']);
        if (!is_wp_error($term_ids)) {
            foreach ($term_ids as $tid) {
                $tags[] = 'term-' . $tid;
            }
        }
    }
    frankenwp_purge_cloudflare(array_values(array_unique($tags)), $link);
}, 10, 2);

/**
 * Purge a single relative path from the local FrankenWP cache. Non-blocking so
 * it never slows down the editor save.
 *
 * @param string $relative_path e.g. "/hello-world/"
 */
function frankenwp_local_purge($relative_path) {
    $purge_path = isset($_SERVER['PURGE_PATH']) ? $_SERVER['PURGE_PATH'] : getenv('PURGE_PATH');
    $purge_key  = isset($_SERVER['PURGE_KEY']) ? $_SERVER['PURGE_KEY'] : getenv('PURGE_KEY');

    // The purge endpoint is disabled unless a key is configured.
    if (empty($purge_path) || empty($purge_key) || empty($relative_path)) {
        return;
    }

    $url = get_site_url() . $purge_path . $relative_path;
    wp_remote_post($url, [
        'headers'   => ['X-WPSidekick-Purge-Key' => $purge_key],
        'sslverify' => false, // loopback to the local FrankenPHP instance
        'timeout'   => 5,
        'blocking'  => false,
    ]);
}

/**
 * Purge the Cloudflare edge cache by tag (falling back to a single URL when no
 * tags are available). No-op unless CLOUDFLARE_ZONE_ID + CLOUDFLARE_API_TOKEN
 * are configured.
 *
 * @param string[] $tags
 * @param string   $fallback_url
 * @return bool|WP_Error
 */
function frankenwp_purge_cloudflare($tags, $fallback_url = '') {
    $cf_zone_id   = getenv('CLOUDFLARE_ZONE_ID');
    $cf_api_token = getenv('CLOUDFLARE_API_TOKEN');

    if (empty($cf_zone_id) || empty($cf_api_token)) {
        return true; // Cloudflare not configured — skip silently.
    }

    // Cloudflare allows up to 30 tags per purge_cache call.
    if (!empty($tags)) {
        $body = ['tags' => array_slice($tags, 0, 30)];
    } elseif (!empty($fallback_url)) {
        $body = ['files' => [$fallback_url]];
    } else {
        return true;
    }

    $response = wp_remote_post(
        sprintf('https://api.cloudflare.com/client/v4/zones/%s/purge_cache', $cf_zone_id),
        [
            'headers' => [
                'Authorization' => 'Bearer ' . $cf_api_token,
                'Content-Type'  => 'application/json',
            ],
            'body'    => wp_json_encode($body),
            'timeout' => 15,
        ]
    );

    if (is_wp_error($response)) {
        error_log('Cloudflare cache purge failed: ' . $response->get_error_message());
        return $response;
    }

    $code = wp_remote_retrieve_response_code($response);
    $data = json_decode(wp_remote_retrieve_body($response), true);

    if ($code !== 200 || empty($data['success'])) {
        $error_msg = isset($data['errors']) ? wp_json_encode($data['errors']) : 'Unknown error';
        error_log("Cloudflare cache purge failed: {$error_msg}");
        return new WP_Error('cloudflare_purge_failed', $error_msg);
    }

    return true;
}
