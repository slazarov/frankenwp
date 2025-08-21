<?php
add_action('phpmailer_init', function($phpmailer) {
    error_log('SMTP Config: Setting up SMTP configuration');

    $phpmailer->isSMTP();
    $phpmailer->Host = getenv('SMTP_HOST');
    $phpmailer->SMTPAuth = true;
    $phpmailer->Username = getenv('SMTP_USERNAME');
    $phpmailer->Password = getenv('SMTP_PASSWORD');

    $port = (int)(getenv('SMTP_PORT') ?: 587);
    $phpmailer->Port = $port;

    // Port 465 uses SSL, port 587 uses TLS
    if ($port == 465) {
        $phpmailer->SMTPSecure = 'ssl';
    } else {
        $phpmailer->SMTPSecure = getenv('SMTP_ENCRYPTION') ?: 'tls';
    }

    $phpmailer->From = getenv('SMTP_FROM_EMAIL');
    $phpmailer->FromName = getenv('SMTP_FROM_NAME');

    // Force SMTP and prevent fallback
    $phpmailer->Mailer = 'smtp';

    error_log('SMTP Config complete - Host: ' . $phpmailer->Host . ', Port: ' . $phpmailer->Port . ', Security: ' . $phpmailer->SMTPSecure);
}, 999); // Higher priority to run later
?>