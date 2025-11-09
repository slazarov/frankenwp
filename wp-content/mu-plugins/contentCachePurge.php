<?php
/**
 * Plugin Name:     Content Cache Purge
 * Author:          Stephen Miracle
 * Description:     Purge the content on publish.
 * Version:         0.2.0
 *
 */


add_action("save_post", function ($id) {
    $link = get_permalink($id);

    // Purge local FrankenWP cache
    $url = get_site_url() . $_SERVER["PURGE_PATH"] . wp_make_link_relative($link) . "/";
    wp_remote_post($url, [
        "headers" => [
            "X-WPSidekick-Purge-Key" => $_SERVER["PURGE_KEY"],
        ],
        "sslverify" => false,
    ]);

    // Purge Cloudflare cache if configured
    purge_cloudflare_cache($link);
});

/**
 * Purge Cloudflare cache for a specific URL
 *
 * @param string $url The URL to purge from Cloudflare cache
 * @return bool|WP_Error True on success, WP_Error on failure
 */
function purge_cloudflare_cache($url) {
    // Check if Cloudflare integration is enabled
    $cf_zone_id = getenv('CLOUDFLARE_ZONE_ID');
    $cf_api_token = getenv('CLOUDFLARE_API_TOKEN');

    if (empty($cf_zone_id) || empty($cf_api_token)) {
        // Cloudflare not configured, skip silently
        return true;
    }

    // Cloudflare API endpoint
    $api_endpoint = sprintf(
        'https://api.cloudflare.com/client/v4/zones/%s/purge_cache',
        $cf_zone_id
    );

    // Prepare the request
    $response = wp_remote_post($api_endpoint, [
        'headers' => [
            'Authorization' => 'Bearer ' . $cf_api_token,
            'Content-Type' => 'application/json',
        ],
        'body' => json_encode([
            'files' => [$url]
        ]),
        'timeout' => 15,
    ]);

    // Check for errors
    if (is_wp_error($response)) {
        error_log('Cloudflare cache purge failed: ' . $response->get_error_message());
        return $response;
    }

    $response_code = wp_remote_retrieve_response_code($response);
    $response_body = json_decode(wp_remote_retrieve_body($response), true);

    if ($response_code !== 200 || !$response_body['success']) {
        $error_msg = isset($response_body['errors'])
            ? json_encode($response_body['errors'])
            : 'Unknown error';
        error_log("Cloudflare cache purge failed for {$url}: {$error_msg}");
        return new WP_Error('cloudflare_purge_failed', $error_msg);
    }

    error_log("Successfully purged Cloudflare cache for: {$url}");
    return true;
}