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

	t.Run("Rejects Host with wrong port (CR-I1)", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:9999/topics", nil)
		req.Host = "127.0.0.1:9999"
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("Expected 403 Forbidden for wrong Host port, got %d", rec.Code)
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
		if rec.Header().Get("X-CSRF-Stale") != "1" {
			t.Error("CSRF 403 must carry X-CSRF-Stale so app.js can reload (CR-20)")
		}
		// T13.8(c): a plain form gets a Persian page with a way back, not a bare «Forbidden».
		if !strings.Contains(rec.Body.String(), "نشست منقضی شده") || !strings.Contains(rec.Body.String(), `href="/topics"`) ||
			!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
			t.Errorf("stale CSRF page: %q %q", rec.Header().Get("Content-Type"), rec.Body.String())
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

	t.Run("Rejects POST with port-less Origin (CR-I1)", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/topics/1/jobs/lesson", strings.NewReader("{}"))
		req.Host = "127.0.0.1:8787"
		req.Header.Set("Origin", "http://127.0.0.1")
		req.Header.Set("X-CSRF-Token", sm.CSRFToken())
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("Expected 403 Forbidden for port-less Origin, got %d", rec.Code)
		}
	})

	t.Run("Accepts POST with IPv6 loopback Origin with port (CR-I1)", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/topics/1/jobs/lesson", strings.NewReader("{}"))
		req.Host = "127.0.0.1:8787"
		req.Header.Set("Origin", "http://[::1]:8787")
		req.Header.Set("X-CSRF-Token", sm.CSRFToken())
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("Expected 200 OK for [::1]:8787 Origin, got %d", rec.Code)
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

	t.Run("CORP header and cross-site subresource GET rejected (CR-48)", func(t *testing.T) {
		for dest, want := range map[string]int{"image": http.StatusForbidden, "script": http.StatusForbidden, "document": http.StatusOK} {
			req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/files/1", nil)
			req.Host = "127.0.0.1:8787"
			req.Header.Set("Sec-Fetch-Site", "cross-site")
			req.Header.Set("Sec-Fetch-Dest", dest)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != want || rec.Header().Get("Cross-Origin-Resource-Policy") != "same-origin" {
				t.Errorf("dest %s: code %d CORP %q", dest, rec.Code, rec.Header().Get("Cross-Origin-Resource-Policy"))
			}
		}
	})

	t.Run("Body limit applies before CSRF form parsing (CR-I2)", func(t *testing.T) {
		// Valid token in a form body larger than the 64KB default must return 413.
		body := "csrf_token=" + sm.CSRFToken() + "&pad=" + strings.Repeat("a", 128*1024)
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/topics/1/jobs/lesson", strings.NewReader(body))
		req.Host = "127.0.0.1:8787"
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("Expected 413 for oversized form POST, got %d", rec.Code)
		}
	})

	t.Run("Body limit applies on header-CSRF path (CR-I2)", func(t *testing.T) {
		// Oversized body with X-CSRF-Token must also return 413
		body := "pad=" + strings.Repeat("a", 128*1024)
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/topics/1/jobs/lesson", strings.NewReader(body))
		req.Host = "127.0.0.1:8787"
		req.Header.Set("Origin", "http://127.0.0.1:8787")
		req.Header.Set("X-CSRF-Token", sm.CSRFToken())
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("Expected 413 for oversized htmx POST, got %d", rec.Code)
		}
	})

	t.Run("Fast rejection when Content-Length exceeds limit (CR-I2)", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/topics/1/jobs/lesson", strings.NewReader("{}"))
		req.Host = "127.0.0.1:8787"
		req.ContentLength = 200 * 1024
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("Expected 413 for oversized Content-Length, got %d", rec.Code)
		}
	})

	t.Run("Cache-Control header for static vs dynamic routes (CR-I4)", func(t *testing.T) {
		reqStatic := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/static/app.css", nil)
		recStatic := httptest.NewRecorder()
		handler.ServeHTTP(recStatic, reqStatic)
		if cc := recStatic.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("Expected no-cache (revalidate) for /static/, got %q", cc)
		}

		reqDynamic := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/topics", nil)
		recDynamic := httptest.NewRecorder()
		handler.ServeHTTP(recDynamic, reqDynamic)
		if cc := recDynamic.Header().Get("Cache-Control"); cc != "no-store" {
			t.Errorf("Expected no-store Cache-Control for /topics, got %q", cc)
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
