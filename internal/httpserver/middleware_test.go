package httpserver_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"learninghub/internal/httpserver"
)

func TestSecurityMiddleware(t *testing.T) {
	sm, err := httpserver.NewSecurityManager("127.0.0.1:8787")
	if err != nil {
		t.Fatalf("Failed to create SecurityManager: %v", err)
	}

	okHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	handler := sm.SecurityMiddleware(okHandler)

	t.Run("Rejects invalid Host header (Anti-DNS Rebinding)", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "http://evil.com/topics", nil)
		req.Host = "evil.com"
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("Expected 403 Forbidden for Host evil.com, got %d", rec.Code)
		}
	})

	t.Run("Accepts valid 127.0.0.1 and localhost Host", func(t *testing.T) {
		validHosts := []string{"127.0.0.1:8787", "localhost:8787", "127.0.0.1", "localhost"}
		for _, vh := range validHosts {
			req := httptest.NewRequest(http.MethodGet, "http://"+vh+"/topics", nil)
			req.Host = vh
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Errorf("Expected 200 OK for Host %s, got %d", vh, rec.Code)
			}
		}
	})

	t.Run("Rejects POST without CSRF token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/topics/1/jobs/lesson", strings.NewReader("{}"))
		req.Host = "127.0.0.1:8787"
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("Expected 403 Forbidden for POST without CSRF token, got %d", rec.Code)
		}
	})

	t.Run("Rejects POST with invalid Origin", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/topics/1/jobs/lesson", strings.NewReader("{}"))
		req.Host = "127.0.0.1:8787"
		req.Header.Set("Origin", "https://attacker.site")
		req.Header.Set("X-CSRF-Token", sm.CSRFToken())
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("Expected 403 Forbidden for invalid Origin, got %d", rec.Code)
		}
	})

	t.Run("Rejects POST with null Origin", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/topics/1/jobs/lesson", strings.NewReader("{}"))
		req.Host = "127.0.0.1:8787"
		req.Header.Set("Origin", "null")
		req.Header.Set("X-CSRF-Token", sm.CSRFToken())
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("Expected 403 Forbidden for Origin null, got %d", rec.Code)
		}
	})

	t.Run("Rejects POST with cross-site Sec-Fetch-Site", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/topics/1/jobs/lesson", strings.NewReader("{}"))
		req.Host = "127.0.0.1:8787"
		req.Header.Set("Origin", "http://127.0.0.1:8787")
		req.Header.Set("X-CSRF-Token", sm.CSRFToken())
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("Expected 403 Forbidden for Sec-Fetch-Site cross-site, got %d", rec.Code)
		}
	})

	t.Run("Accepts valid POST with CSRF header and valid Origin", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/topics/1/jobs/lesson", strings.NewReader("{}"))
		req.Host = "127.0.0.1:8787"
		req.Header.Set("Origin", "http://127.0.0.1:8787")
		req.Header.Set("X-CSRF-Token", sm.CSRFToken())
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("Expected 200 OK for valid POST, got %d", rec.Code)
		}

		// Verify security headers
		if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
			t.Errorf("Missing or invalid CSP header: %s", csp)
		}
		if nosniff := rec.Header().Get("X-Content-Type-Options"); nosniff != "nosniff" {
			t.Errorf("Missing X-Content-Type-Options nosniff")
		}
	})
}
