package web

import "testing"

func TestNormalizePath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/article/some-slug", "/article/:slug"},
		{"/cards/amex-platinum", "/cards/:slug"},
		{"/cards/", "/cards/"},
		{"/static/css/style.css", "/static"},
		{"/random-bot-path", "/_other"},
		{"/admin/articles/123/approve", "/admin/articles/:id/approve"},
		{"/admin/cards/5/updates/17/approve", "/admin/cards/:id/updates/:uid/approve"},
		{"/admin/sources/3/toggle", "/admin/sources/:id/toggle"},
		{"/", "/"},
		{"/search", "/search"},
		{"/healthz", "/healthz"},
		{"/metrics", "/metrics"},
		{"/subscribe", "/subscribe"},
		{"/subscribe/confirm", "/subscribe/confirm"},
		{"/unsubscribe", "/unsubscribe"},
		{"/api/vote", "/api/vote"},
		{"/feed.xml", "/feed.xml"},
		{"/robots.txt", "/robots.txt"},
		{"/sitemap.xml", "/sitemap.xml"},
		{"/admin/dashboard", "/admin/dashboard"},
		{"/admin/cards/new", "/admin/cards/new"},
		{"/admin/cards/5/edit", "/admin/cards/:id/edit"},
		{"/admin/cards/5/delete", "/admin/cards/:id/delete"},
		{"/admin/cards/5/updates", "/admin/cards/:id/updates"},
		{"/admin/cards/5/updates/17/reject", "/admin/cards/:id/updates/:uid/reject"},
		{"/admin/sources/add", "/admin/sources/add"},
		{"/static/js/app.js", "/static"},
		{"/static", "/static"},
		{"/wp-admin/login.php", "/_other"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizePath(tt.input)
			if got != tt.want {
				t.Errorf("normalizePath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
