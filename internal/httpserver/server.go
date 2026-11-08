package httpserver

import (
	"io"
	"io/fs"
	"mime"
	"net/http"
	"time"

	"learninghub/internal/domain"
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
	_ = mime.AddExtensionType(".woff2", "font/woff2") // not in Go's built-in table; Windows registry may lack it

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok")
	})
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.Handle("GET /{$}", http.RedirectHandler("/topics", http.StatusSeeOther))
	mux.HandleFunc("GET /topics", a.requireAck(a.listTopics))
	mux.HandleFunc("GET /topics/{id}", a.showTopic)
	mux.HandleFunc("GET /topics/{id}/lesson", a.showTopic)
	mux.HandleFunc("GET /topics/{id}/source", a.showSource)
	mux.HandleFunc("POST /topics/{id}/jobs/lesson", a.requireAck(a.generateLesson))
	mux.HandleFunc("GET /topics/{id}/exercises", a.showExercises)
	mux.HandleFunc("GET /topics/{id}/answers", a.showAnswers)
	mux.HandleFunc("POST /topics/{id}/jobs/exercises", a.requireAck(a.generateExercises))
	mux.HandleFunc("GET /topics/{id}/reports", a.showReports)
	mux.HandleFunc("POST /topics/{id}/jobs/evaluate", a.requireAck(a.evaluate))
	mux.HandleFunc("GET /topics/{id}/suggestions", a.showSuggestions)
	mux.HandleFunc("POST /suggestions/{id}/accept", a.acceptSuggestion)
	mux.HandleFunc("POST /suggestions/{id}/reject", a.rejectSuggestion)
	mux.HandleFunc("GET /files/{id}", a.serveFile)
	mux.HandleFunc("POST /files/{id}/open", a.openFile)
	mux.HandleFunc("GET /jobs/{id}/card", a.jobCard)
	mux.HandleFunc("POST /jobs/{id}/cancel", a.cancelJob)
	mux.HandleFunc("POST /jobs/{id}/retry", a.requireAck(a.retryJob))
	mux.HandleFunc("GET /settings", a.showSettings)
	mux.HandleFunc("POST /settings/doctor", a.rerunDoctor)
	mux.HandleFunc("POST /settings/privacy", a.ackPrivacy)
	mux.HandleFunc("POST /settings/adapter", a.selectAdapter)
	mux.HandleFunc("GET /sync", a.showSync)
	mux.HandleFunc("POST /sync/scan", a.scanSync)
	mux.HandleFunc("POST /sync/{id}/apply", a.applySync)
	mux.HandleFunc("GET /roadmaps/new", a.newRoadmap)
	mux.HandleFunc("POST /roadmaps/drafts", a.createDraft)
	mux.HandleFunc("GET /roadmaps/drafts/{id}", a.showDraft)
	mux.HandleFunc("POST /roadmaps/drafts/{id}/selection", a.updateSelection)
	mux.HandleFunc("POST /roadmaps/drafts/{id}/confirm", a.confirmDraft)
	mux.HandleFunc("POST /roadmaps/drafts/{id}/ai-assist", a.requireAck(a.draftJob(domain.JobKindRoadmapAnalyze)))
	mux.HandleFunc("POST /roadmaps/drafts/{id}/chapters", a.requireAck(a.draftJob(domain.JobKindChapterDetect)))
	mux.HandleFunc("GET /roadmaps/drafts/{id}/ai", a.draftAIStatus)

	return &http.Server{
		Addr:              addr,
		Handler:           sm.SecurityMiddleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}
