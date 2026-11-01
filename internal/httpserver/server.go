package httpserver

import (
	"io"
	"net/http"
	"time"
)

// NewServer builds the HTTP server with every route behind SecurityMiddleware (Section 8).
func NewServer(addr string, sm *SecurityManager) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok")
	})

	return &http.Server{
		Addr:              addr,
		Handler:           sm.SecurityMiddleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}
