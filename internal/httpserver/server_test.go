package httpserver_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"learninghub/internal/httpserver"
)

func TestNewServerWiresSecurityMiddleware(t *testing.T) {
	sm, err := httpserver.NewSecurityManager("127.0.0.1:8787")
	if err != nil {
		t.Fatal(err)
	}
	srv := httpserver.NewServer("127.0.0.1:8787", sm)

	get := func(host string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		srv.Handler.ServeHTTP(rec, req)
		return rec
	}

	if rec := get("evil.com"); rec.Code != http.StatusForbidden {
		t.Errorf("foreign Host: want 403, got %d", rec.Code)
	}

	rec := get("127.0.0.1:8787")
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Errorf("healthz: want 200 ok, got %d %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Security-Policy") == "" {
		t.Error("healthz: CSP header missing, middleware not wired")
	}
	if srv.ReadHeaderTimeout == 0 || srv.IdleTimeout == 0 {
		t.Error("server timeouts must be set (Section 8)")
	}
}
