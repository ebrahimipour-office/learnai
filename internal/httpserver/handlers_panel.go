package httpserver

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"learninghub/internal/domain"
	"learninghub/internal/jobs"
	"learninghub/internal/render"
	"learninghub/internal/services/topics"
)

var jobStatusLabels = map[string]string{
	"queued": "در صف", "running": "در حال اجرا", "succeeded": "موفق", "failed": "ناموفق", "canceled": "لغوشده",
}

var jobPhaseLabels = map[string]string{
	"prepare": "آماده‌سازی", "ai": "تولید با هوش مصنوعی", "validate": "اعتبارسنجی", "commit": "ثبت",
}

var jobKindLabels = map[string]string{
	"lesson": "تولید درس", "exercises": "تولید تمرین", "evaluate": "ارزیابی", "roadmap_analyze": "تحلیل نقشه راه",
}

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

// panelData loads the topic and its latest job; job overrides the latest one when non-nil.
func (a *app) panelData(r *http.Request, id int64, job *jobs.Info) (*pageData, error) {
	p, err := a.topics.Panel(r.Context(), id)
	if err != nil {
		return nil, err
	}
	if job == nil {
		if job, err = a.runner.Latest(r.Context(), topics.Scope(id)); err != nil {
			return nil, err
		}
	}
	return &pageData{Title: p.Title, Panel: p, Job: job}, nil
}

func (a *app) showTopic(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	d, err := a.panelData(r, id, nil)
	if errors.Is(err, domain.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		slog.Error("loading topic panel", "topic", id, "error", err)
		http.Error(w, "خطای داخلی", http.StatusInternalServerError)
		return
	}
	a.fillLesson(r, d)
	a.render(w, http.StatusOK, "topic", "layout", d)
}

// fillLesson renders the version in ?v= (default: current). The file is the user's, so it may be gone or edited.
func (a *app) fillLesson(r *http.Request, d *pageData) {
	want, _ := strconv.ParseInt(r.URL.Query().Get("v"), 10, 64)
	for i := range d.Panel.Lessons {
		v := &d.Panel.Lessons[i]
		if (want == 0 && v.Current) || v.Version == want {
			d.Viewing = v
			break
		}
	}
	if d.Viewing == nil {
		return
	}
	b, err := a.root.ReadFile(d.Viewing.RelPath)
	if err != nil {
		slog.Warn("reading lesson file", "path", d.Viewing.RelPath, "error", err)
		d.Error = "فایل درس روی دیسک پیدا نشد: " + d.Viewing.RelPath
		return
	}
	d.Lesson = render.Markdown(b)
}

// respond answers a job action: htmx gets the job-area fragment with status; a plain form
// is redirected back to the panel on success or shown the panel with the notice.
func (a *app) respond(w http.ResponseWriter, r *http.Request, status int, topicID int64, job *jobs.Info, notice string) {
	if r.Header.Get("HX-Request") != "true" && status < 300 {
		http.Redirect(w, r, "/topics/"+strconv.FormatInt(topicID, 10), http.StatusSeeOther)
		return
	}
	d, err := a.panelData(r, topicID, job)
	if err != nil {
		slog.Error("loading topic panel", "topic", topicID, "error", err)
		http.Error(w, "خطای داخلی", http.StatusInternalServerError)
		return
	}
	d.Notice = notice
	if r.Header.Get("HX-Request") == "true" {
		a.render(w, status, "topic", "job-area", d)
		return
	}
	d.Error = notice
	a.fillLesson(r, d)
	a.render(w, status, "topic", "layout", d)
}

func (a *app) generateLesson(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	p, err := a.topics.Panel(r.Context(), id)
	if errors.Is(err, domain.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		slog.Error("loading topic", "topic", id, "error", err)
		http.Error(w, "خطای داخلی", http.StatusInternalServerError)
		return
	}
	confirm := r.FormValue("confirm_regenerate") == "1"
	if len(p.Lessons) > 0 && !confirm {
		a.respond(w, r, http.StatusConflict, id, nil, "بازتولید درس نیاز به تأیید دارد؛ نسخه فعلی در تاریخچه نگه داشته می‌شود.")
		return
	}
	params, _ := json.Marshal(map[string]bool{"confirm_regenerate": confirm})
	_, err = a.runner.Enqueue(r.Context(), jobs.Job{Kind: domain.JobKindLesson, Scope: topics.Scope(id), TopicID: id, Params: params})
	a.afterEnqueue(w, r, id, err)
}

func (a *app) afterEnqueue(w http.ResponseWriter, r *http.Request, topicID int64, err error) {
	switch {
	case err == nil:
		a.respond(w, r, http.StatusAccepted, topicID, nil, "")
	case errors.Is(err, domain.ErrScopeBusy):
		a.respond(w, r, http.StatusConflict, topicID, nil, "کار دیگری روی این موضوع در جریان است.")
	case errors.Is(err, domain.ErrInvalidState):
		a.respond(w, r, http.StatusConflict, topicID, nil, "فقط کار ناموفق قابل تکرار است.")
	default:
		slog.Error("enqueueing job", "topic", topicID, "error", err)
		http.Error(w, "خطای داخلی", http.StatusInternalServerError)
	}
}

// loadJob returns the job in {id}; only topic jobs have a card so far.
// shortcut: roadmap_analyze jobs (topic_id NULL) get no card; T6.2 adds one on the draft page.
func (a *app) loadJob(w http.ResponseWriter, r *http.Request) *jobs.Info {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return nil
	}
	j, err := a.runner.Get(r.Context(), id)
	if errors.Is(err, domain.ErrNotFound) || (err == nil && j.TopicID == 0) {
		http.NotFound(w, r)
		return nil
	}
	if err != nil {
		slog.Error("loading job", "job", id, "error", err)
		http.Error(w, "خطای داخلی", http.StatusInternalServerError)
		return nil
	}
	return j
}

// jobCard is polled every 2s while the job is active; once it ends the page reloads to show the result.
func (a *app) jobCard(w http.ResponseWriter, r *http.Request) {
	j := a.loadJob(w, r)
	if j == nil {
		return
	}
	if !j.Active() && r.URL.Query().Get("poll") == "1" {
		w.Header().Set("HX-Refresh", "true")
	}
	a.respond(w, r, http.StatusOK, j.TopicID, j, "")
}

func (a *app) cancelJob(w http.ResponseWriter, r *http.Request) {
	j := a.loadJob(w, r)
	if j == nil {
		return
	}
	late, err := a.runner.Cancel(r.Context(), j.ID)
	switch {
	case errors.Is(err, domain.ErrInvalidState):
		a.respond(w, r, http.StatusConflict, j.TopicID, nil, "این کار پایان یافته و قابل لغو نیست.")
	case err != nil:
		slog.Error("canceling job", "job", j.ID, "error", err)
		http.Error(w, "خطای داخلی", http.StatusInternalServerError)
	case late:
		a.respond(w, r, http.StatusOK, j.TopicID, nil, jobs.LateCancelMessage)
	default:
		a.respond(w, r, http.StatusOK, j.TopicID, nil, "")
	}
}

func (a *app) retryJob(w http.ResponseWriter, r *http.Request) {
	j := a.loadJob(w, r)
	if j == nil {
		return
	}
	_, err := a.runner.Retry(r.Context(), j.ID)
	a.afterEnqueue(w, r, j.TopicID, err)
}
