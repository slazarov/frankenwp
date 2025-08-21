<?php
add_action('phpmailer_init', function($phpmailer) {
    $phpmailer->isSMTP();
    $phpmailer->Host = getenv('SMTP_HOST');
    $phpmailer->SMTPAuth = true;
    $phpmailer->Username = getenv('SMTP_USERNAME');
    $phpmailer->Password = getenv('SMTP_PASSWORD');
    $phpmailer->SMTPSecure = getenv('SMTP_ENCRYPTION') ?: 'tls';
    $phpmailer->Port = getenv('SMTP_PORT') ?: 587;
    $phpmailer->From = getenv('SMTP_FROM_EMAIL');
    $phpmailer->FromName = getenv('SMTP_FROM_NAME');
});
?>