package httpserver

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"strings"
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
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.Handle("GET /static/", http.StripPrefix("/static/", staticHandler(static)))
	mux.Handle("GET /{$}", http.RedirectHandler("/topics", http.StatusSeeOther))
	mux.HandleFunc("GET /topics", a.requireAck(a.listTopics))
	mux.HandleFunc("GET /topics/{id}", a.showTopic)
	mux.HandleFunc("GET /topics/{id}/lesson", a.showTopic)
	mux.HandleFunc("GET /topics/{id}/source", a.showSource)
	mux.HandleFunc("POST /topics/{id}/jobs/lesson", a.requireAck(a.generateLesson))
	mux.HandleFunc("POST /topics/{id}/jobs/qa", a.requireAck(a.askQuestion))
	mux.HandleFunc("POST /topics/{id}/jobs/flashcards", a.requireAck(a.buildCards))
	mux.HandleFunc("POST /topics/{id}/cards/{card}", a.gradeCard)
	mux.HandleFunc("GET /topics/{id}/exercises", a.showExercises)
	mux.HandleFunc("GET /topics/{id}/answers", a.showAnswers)
	mux.HandleFunc("POST /topics/{id}/jobs/exercises", a.requireAck(a.generateExercises))
	mux.HandleFunc("GET /topics/{id}/reports", a.showReports)
	mux.HandleFunc("POST /topics/{id}/jobs/evaluate", a.requireAck(a.evaluate))
	mux.HandleFunc("POST /topics/{id}/archive", a.archiveTopic)
	mux.HandleFunc("POST /topics/{id}/unarchive", a.unarchiveTopic)
	mux.HandleFunc("POST /topics/{id}/merge", a.mergeTopic)
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
	mux.HandleFunc("POST /settings/calendar", a.selectCalendar)
	mux.HandleFunc("POST /settings/skills/{name}/upgrade", a.upgradeSkill)
	mux.HandleFunc("POST /settings/backup", a.backupNow)
	mux.HandleFunc("GET /settings/backups/{name}", a.downloadBackup)
	mux.HandleFunc("GET /topics/{id}/export", a.exportTopic)
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

	// ReadTimeout and WriteTimeout are intentionally unset on this loopback server:
	// WriteTimeout would prematurely cut off synchronous operations like adapter probing (~90s),
	// while ReadHeaderTimeout (10s) and IdleTimeout (120s) protect against slowloris attacks on headers.
	return &http.Server{
		Addr:              addr,
		Handler:           sm.SecurityMiddleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

// staticHandler serves the embedded assets with a content-hash ETag (embed.FS has no ModTime, so
// without it every navigation re-downloads them; ServeContent answers If-None-Match with 304).
// Fonts never change in place, so they are cached for a year (T13.12).
func staticHandler(static fs.FS) http.Handler {
	etags := map[string]string{}
	err := fs.WalkDir(static, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(static, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		etags[p] = `"` + hex.EncodeToString(sum[:8]) + `"`
		return nil
	})
	if err != nil {
		panic(err) // embedded at build time; cannot fail at runtime
	}
	files := http.FileServerFS(static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tag, ok := etags[r.URL.Path]; ok {
			w.Header().Set("ETag", tag)
			if strings.HasPrefix(r.URL.Path, "fonts/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
		}
		files.ServeHTTP(w, r)
	})
}
