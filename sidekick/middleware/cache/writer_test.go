package cache

import (
	"testing"
)

func TestShouldBypassCacheForContent(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected bool
	}{
		{
			name:     "Iubenda consent management",
			content:  `<iframe class="_iub_cs_activate" src="about:blank"></iframe>`,
			expected: true,
		},
		{
			name:     "Lazy load class",
			content:  `<iframe class="cmplazyload" src="about:blank"></iframe>`,
			expected: true,
		},
		{
			name:     "Suppressed iframe source",
			content:  `<iframe data-suppressedsrc="https://maps.google.com"></iframe>`,
			expected: true,
		},
		{
			name:     "CMP attributes",
			content:  `<iframe data-cmp-vendor="178"></iframe>`,
			expected: true,
		},
		{
			name:     "Cookiebot",
			content:  `<script src="cookiebot.js"></script>`,
			expected: true,
		},
		{
			name:     "OneTrust",
			content:  `<script src="OneTrust.js"></script>`,
			expected: true,
		},
		{
			name:     "Generic cookie consent",
			content:  `<div class="CookieConsent"></div>`,
			expected: true,
		},
		{
			name:     "Normal content without consent management",
			content:  `<iframe src="https://www.youtube.com/embed/test"></iframe>`,
			expected: false,
		},
		{
			name:     "Regular Google Maps iframe",
			content:  `<iframe src="https://www.google.com/maps/embed?pb=test"></iframe>`,
			expected: false,
		},
		{
			name:     "Plain HTML",
			content:  `<html><body><h1>Hello World</h1></body></html>`,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shouldBypassCacheForContent([]byte(tt.content))
			if result != tt.expected {
				t.Errorf("shouldBypassCacheForContent() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestGoogleMapsConsentManagedIframe(t *testing.T) {
	// Real-world example from user's issue
	content := `<iframe src="about:blank" width="100%" height="450" style="border: 0px;" allowfullscreen=""
loading="lazy" referrerpolicy="no-referrer-when-downgrade" class="_iub_cs_activate cmplazyload"
data-suppressedsrc="https://www.google.com/maps/embed?pb=!1m18!1m12!1m3!1d10230.133581217482"
data-iub-purposes="3" data-cmp-ab="2" data-cmp-src="https://www.google.com/maps/embed"
data-cmp-info="11" data-cmp-vendor="178"></iframe>`

	result := shouldBypassCacheForContent([]byte(content))
	if !result {
		t.Error("Expected to bypass cache for consent-managed Google Maps iframe")
	}
}
