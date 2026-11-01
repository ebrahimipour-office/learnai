package httpserver

import (
	"io"
	"io/fs"
	"net/http"
	"time"

	"learninghub/internal/services/roadmaps"
	"learninghub/internal/ui"
)

// NewServer builds the HTTP server with every route behind SecurityMiddleware (Section 8).
func NewServer(addr string, sm *SecurityManager, drafts *roadmaps.Service) *http.Server {
	a := newApp(sm, drafts)
	static, err := fs.Sub(ui.FS, "static")
	if err != nil {
		panic(err) // embedded at build time; cannot fail at runtime
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok")
	})
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	// shortcut: "/" goes to the import form until the topic list exists, switch to /topics in T2.6.
	mux.Handle("GET /{$}", http.RedirectHandler("/roadmaps/new", http.StatusSeeOther))
	mux.HandleFunc("GET /roadmaps/new", a.newRoadmap)
	mux.HandleFunc("POST /roadmaps/drafts", a.createDraft)
	mux.HandleFunc("GET /roadmaps/drafts/{id}", a.showDraft)
	mux.HandleFunc("POST /roadmaps/drafts/{id}/selection", a.updateSelection)
	mux.HandleFunc("POST /roadmaps/drafts/{id}/confirm", a.confirmDraft)

	return &http.Server{
		Addr:              addr,
		Handler:           sm.SecurityMiddleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}
