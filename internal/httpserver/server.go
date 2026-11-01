package httpserver

import (
	"io"
	"io/fs"
	"net/http"
	"time"

	"learninghub/internal/fsx"
	"learninghub/internal/jobs"
	"learninghub/internal/services/roadmaps"
	"learninghub/internal/services/topics"
	"learninghub/internal/ui"
)

// NewServer builds the HTTP server with every route behind SecurityMiddleware (Section 8).
func NewServer(addr string, sm *SecurityManager, drafts *roadmaps.Service, topicList *topics.Service, runner *jobs.Runner, root *fsx.Root, settings Settings) *http.Server {
	a := newApp(sm, drafts, topicList, runner, root, settings)
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
	mux.Handle("GET /{$}", http.RedirectHandler("/topics", http.StatusSeeOther))
	mux.HandleFunc("GET /topics", a.listTopics)
	mux.HandleFunc("GET /topics/{id}", a.showTopic)
	mux.HandleFunc("GET /topics/{id}/lesson", a.showTopic)
	mux.HandleFunc("POST /topics/{id}/jobs/lesson", a.generateLesson)
	mux.HandleFunc("GET /jobs/{id}/card", a.jobCard)
	mux.HandleFunc("POST /jobs/{id}/cancel", a.cancelJob)
	mux.HandleFunc("POST /jobs/{id}/retry", a.retryJob)
	mux.HandleFunc("GET /settings", a.showSettings)
	mux.HandleFunc("POST /settings/doctor", a.rerunDoctor)
	mux.HandleFunc("POST /settings/privacy", a.ackPrivacy)
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
