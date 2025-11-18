<?php
/**
 * Plugin Name:     WebP Image Handler
 * Author:          FrankenWP
 * Description:     Automatically serve WebP images to supported browsers with on-the-fly conversion.
 * Version:         1.0.0
 */

// Prevent direct access
if (!defined('ABSPATH')) {
    exit;
}

/**
 * WebP Image Handler Class
 *
 * Handles automatic WebP conversion and serving for WordPress images.
 */
class FrankenWP_WebP_Handler {

    /**
     * WebP quality setting (0-100)
     */
    private $quality = 82;

    /**
     * Supported source formats
     */
    private $supported_formats = ['jpg', 'jpeg', 'png', 'gif'];

    /**
     * Initialize the handler
     */
    public function __construct() {
        // Register the WebP conversion endpoint
        add_action('init', [$this, 'register_endpoint']);

        // Handle WebP conversion requests
        add_action('template_redirect', [$this, 'handle_webp_request']);

        // Clean up WebP files when attachments are deleted
        add_action('delete_attachment', [$this, 'cleanup_webp_on_delete']);

        // Add filter to modify image srcset for WebP
        add_filter('wp_calculate_image_srcset', [$this, 'filter_srcset'], 10, 5);

        // Auto-generate WebP when images are uploaded
        add_filter('wp_generate_attachment_metadata', [$this, 'generate_webp_on_upload'], 10, 2);

        // Regenerate WebP when image is edited
        add_filter('wp_update_attachment_metadata', [$this, 'generate_webp_on_upload'], 10, 2);

        // Register cron handler for background WebP generation
        add_action('frankenwp_generate_webp', [$this, 'pregenerate_webp']);
    }

    /**
     * Generate WebP versions when an image is uploaded
     *
     * @param array $metadata Attachment metadata
     * @param int $attachment_id Attachment ID
     * @return array Unmodified metadata
     */
    public function generate_webp_on_upload($metadata, $attachment_id) {
        // Check if this is an image we can convert
        $mime_type = get_post_mime_type($attachment_id);
        $supported_mimes = ['image/jpeg', 'image/png', 'image/gif'];

        if (!in_array($mime_type, $supported_mimes)) {
            return $metadata;
        }

        // Generate WebP in background to not slow down upload
        if (function_exists('wp_schedule_single_event')) {
            wp_schedule_single_event(time(), 'frankenwp_generate_webp', [$attachment_id]);
        } else {
            // Fallback: generate immediately
            $this->pregenerate_webp($attachment_id);
        }

        return $metadata;
    }

    /**
     * Register the WebP conversion endpoint
     */
    public function register_endpoint() {
        add_rewrite_rule(
            '^webp-convert/(.+)$',
            'index.php?webp_convert=$matches[1]',
            'top'
        );
        add_rewrite_tag('%webp_convert%', '([^&]+)');
    }

    /**
     * Handle WebP conversion requests
     */
    public function handle_webp_request() {
        $image_path = get_query_var('webp_convert');

        if (empty($image_path)) {
            return;
        }

        // Security: Validate the path
        $image_path = $this->sanitize_image_path($image_path);
        if (!$image_path) {
            status_header(400);
            exit('Invalid image path');
        }

        // Convert and serve the WebP image
        $this->convert_and_serve($image_path);
    }

    /**
     * Sanitize and validate image path
     *
     * @param string $path The image path to sanitize
     * @return string|false Sanitized path or false if invalid
     */
    private function sanitize_image_path($path) {
        // Decode URL encoding
        $path = urldecode($path);

        // Remove leading slash if present
        $path = ltrim($path, '/');

        // Remove any directory traversal attempts
        $path = str_replace(['../', '..\\', '..'], '', $path);

        // Ensure it's within wp-content/uploads
        if (strpos($path, 'wp-content/uploads/') !== 0) {
            return false;
        }

        // Validate extension
        $extension = strtolower(pathinfo($path, PATHINFO_EXTENSION));
        if (!in_array($extension, $this->supported_formats)) {
            return false;
        }

        return $path;
    }

    /**
     * Convert image to WebP and serve it
     *
     * @param string $relative_path Relative path to the image
     */
    private function convert_and_serve($relative_path) {
        $upload_dir = wp_upload_dir();
        $base_dir = dirname($upload_dir['basedir']); // wp-content directory
        $base_dir = dirname($base_dir); // WordPress root

        $source_path = $base_dir . '/' . $relative_path;
        $webp_path = $source_path . '.webp';

        // Check if source exists
        if (!file_exists($source_path)) {
            status_header(404);
            exit('Image not found');
        }

        // Generate WebP if it doesn't exist or is older than source
        if (!file_exists($webp_path) || filemtime($webp_path) < filemtime($source_path)) {
            $success = $this->convert_to_webp($source_path, $webp_path);

            if (!$success) {
                // Fallback to original image
                $this->serve_original($source_path);
                return;
            }
        }

        // Serve the WebP image
        $this->serve_webp($webp_path);
    }

    /**
     * Convert an image to WebP format
     *
     * @param string $source_path Path to source image
     * @param string $webp_path Path for output WebP
     * @return bool Success status
     */
    public function convert_to_webp($source_path, $webp_path) {
        // Try ImageMagick first (better quality)
        if (extension_loaded('imagick')) {
            return $this->convert_with_imagick($source_path, $webp_path);
        }

        // Fall back to GD
        if (extension_loaded('gd')) {
            return $this->convert_with_gd($source_path, $webp_path);
        }

        error_log('WebP Handler: No image processing library available');
        return false;
    }

    /**
     * Convert using ImageMagick
     */
    private function convert_with_imagick($source_path, $webp_path) {
        try {
            $imagick = new Imagick($source_path);

            // Handle animated GIFs
            if ($imagick->getNumberImages() > 1) {
                $imagick = $imagick->coalesceImages();
                foreach ($imagick as $frame) {
                    $frame->setImageFormat('webp');
                    $frame->setImageCompressionQuality($this->quality);
                }
                $imagick->writeImages($webp_path, true);
            } else {
                // Preserve transparency for PNG
                if ($imagick->getImageAlphaChannel()) {
                    $imagick->setImageAlphaChannel(Imagick::ALPHACHANNEL_ACTIVATE);
                    $imagick->setBackgroundColor(new ImagickPixel('transparent'));
                }

                $imagick->setImageFormat('webp');
                $imagick->setImageCompressionQuality($this->quality);
                $imagick->stripImage(); // Remove metadata for smaller file
                $imagick->writeImage($webp_path);
            }

            $imagick->destroy();
            return true;
        } catch (Exception $e) {
            error_log('WebP Handler ImageMagick error: ' . $e->getMessage());
            return false;
        }
    }

    /**
     * Convert using GD library
     */
    private function convert_with_gd($source_path, $webp_path) {
        $extension = strtolower(pathinfo($source_path, PATHINFO_EXTENSION));

        // Load the source image
        switch ($extension) {
            case 'jpg':
            case 'jpeg':
                $image = @imagecreatefromjpeg($source_path);
                break;
            case 'png':
                $image = @imagecreatefrompng($source_path);
                break;
            case 'gif':
                $image = @imagecreatefromgif($source_path);
                break;
            default:
                return false;
        }

        if (!$image) {
            error_log('WebP Handler GD error: Could not load image ' . $source_path);
            return false;
        }

        // Handle transparency for PNG and GIF
        if ($extension === 'png' || $extension === 'gif') {
            imagepalettetotruecolor($image);
            imagealphablending($image, true);
            imagesavealpha($image, true);
        }

        // Convert to WebP
        $success = imagewebp($image, $webp_path, $this->quality);
        imagedestroy($image);

        if (!$success) {
            error_log('WebP Handler GD error: Could not save WebP ' . $webp_path);
        }

        return $success;
    }

    /**
     * Serve a WebP image
     */
    private function serve_webp($webp_path) {
        $this->send_cache_headers($webp_path);
        header('Content-Type: image/webp');
        header('Content-Length: ' . filesize($webp_path));
        readfile($webp_path);
        exit;
    }

    /**
     * Serve the original image as fallback
     */
    private function serve_original($source_path) {
        $extension = strtolower(pathinfo($source_path, PATHINFO_EXTENSION));
        $mime_types = [
            'jpg' => 'image/jpeg',
            'jpeg' => 'image/jpeg',
            'png' => 'image/png',
            'gif' => 'image/gif',
        ];

        $this->send_cache_headers($source_path);
        header('Content-Type: ' . ($mime_types[$extension] ?? 'application/octet-stream'));
        header('Content-Length: ' . filesize($source_path));
        readfile($source_path);
        exit;
    }

    /**
     * Send cache control headers
     */
    private function send_cache_headers($file_path) {
        $etag = '"' . md5_file($file_path) . '"';
        $last_modified = gmdate('D, d M Y H:i:s', filemtime($file_path)) . ' GMT';

        // Check for conditional requests
        if (isset($_SERVER['HTTP_IF_NONE_MATCH']) && trim($_SERVER['HTTP_IF_NONE_MATCH']) === $etag) {
            status_header(304);
            exit;
        }

        if (isset($_SERVER['HTTP_IF_MODIFIED_SINCE']) && strtotime($_SERVER['HTTP_IF_MODIFIED_SINCE']) >= filemtime($file_path)) {
            status_header(304);
            exit;
        }

        header('Cache-Control: public, max-age=31536000, immutable');
        header('ETag: ' . $etag);
        header('Last-Modified: ' . $last_modified);
        header('Vary: Accept');
    }

    /**
     * Clean up WebP files when an attachment is deleted
     */
    public function cleanup_webp_on_delete($attachment_id) {
        $metadata = wp_get_attachment_metadata($attachment_id);
        $upload_dir = wp_upload_dir();

        if (!$metadata || !isset($metadata['file'])) {
            return;
        }

        $base_dir = $upload_dir['basedir'];
        $file_dir = dirname($metadata['file']);

        // Delete WebP for main file
        $main_file = $base_dir . '/' . $metadata['file'];
        $this->delete_webp_variant($main_file);

        // Delete WebP for all sizes
        if (isset($metadata['sizes']) && is_array($metadata['sizes'])) {
            foreach ($metadata['sizes'] as $size) {
                $size_file = $base_dir . '/' . $file_dir . '/' . $size['file'];
                $this->delete_webp_variant($size_file);
            }
        }
    }

    /**
     * Delete a WebP variant file
     */
    private function delete_webp_variant($file_path) {
        $webp_path = $file_path . '.webp';
        if (file_exists($webp_path)) {
            unlink($webp_path);
        }
    }

    /**
     * Filter srcset to add WebP URLs (for JavaScript-based detection)
     * Note: Primary WebP serving is handled by Caddy rewrites
     */
    public function filter_srcset($sources, $size_array, $image_src, $image_meta, $attachment_id) {
        // This filter is available for future enhancements
        // Primary WebP serving is handled at the server level via Caddyfile
        return $sources;
    }

    /**
     * Check if a WebP file exists for a given image
     *
     * @param string $image_url URL of the original image
     * @return bool Whether WebP version exists
     */
    public function webp_exists($image_url) {
        $upload_dir = wp_upload_dir();
        $image_path = str_replace($upload_dir['baseurl'], $upload_dir['basedir'], $image_url);
        return file_exists($image_path . '.webp');
    }

    /**
     * Pre-generate WebP for an attachment
     *
     * @param int $attachment_id WordPress attachment ID
     * @return array Results of conversion for each size
     */
    public function pregenerate_webp($attachment_id) {
        $metadata = wp_get_attachment_metadata($attachment_id);
        $upload_dir = wp_upload_dir();
        $results = [];

        if (!$metadata || !isset($metadata['file'])) {
            return $results;
        }

        $base_dir = $upload_dir['basedir'];
        $file_dir = dirname($metadata['file']);

        // Convert main file
        $main_file = $base_dir . '/' . $metadata['file'];
        $results['full'] = $this->convert_to_webp($main_file, $main_file . '.webp');

        // Convert all sizes
        if (isset($metadata['sizes']) && is_array($metadata['sizes'])) {
            foreach ($metadata['sizes'] as $size_name => $size) {
                $size_file = $base_dir . '/' . $file_dir . '/' . $size['file'];
                $results[$size_name] = $this->convert_to_webp($size_file, $size_file . '.webp');
            }
        }

        return $results;
    }
}

// Initialize the WebP handler
new FrankenWP_WebP_Handler();

/**
 * Helper function to pre-generate WebP for an attachment
 *
 * @param int $attachment_id WordPress attachment ID
 * @return array Results of conversion
 */
function frankenwp_pregenerate_webp($attachment_id) {
    global $frankenwp_webp_handler;
    if (!isset($frankenwp_webp_handler)) {
        $frankenwp_webp_handler = new FrankenWP_WebP_Handler();
    }
    return $frankenwp_webp_handler->pregenerate_webp($attachment_id);
}

/**
 * WP-CLI command for bulk WebP generation
 */
if (defined('WP_CLI') && WP_CLI) {
    WP_CLI::add_command('webp generate', function($args, $assoc_args) {
        $handler = new FrankenWP_WebP_Handler();

        $query_args = [
            'post_type' => 'attachment',
            'post_mime_type' => ['image/jpeg', 'image/png', 'image/gif'],
            'posts_per_page' => -1,
            'fields' => 'ids',
        ];

        $attachments = get_posts($query_args);
        $total = count($attachments);

        WP_CLI::log("Found {$total} images to convert...");

        $progress = WP_CLI\Utils\make_progress_bar('Converting images', $total);

        $success = 0;
        $failed = 0;

        foreach ($attachments as $attachment_id) {
            $results = $handler->pregenerate_webp($attachment_id);

            if (in_array(true, $results)) {
                $success++;
            } else {
                $failed++;
            }

            $progress->tick();
        }

        $progress->finish();

        WP_CLI::success("Converted {$success} images, {$failed} failed.");
    });
}
