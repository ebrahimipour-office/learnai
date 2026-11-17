package httpserver

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"learninghub/internal/domain"
	"learninghub/internal/services/topics"
)

var statusLabels = map[string]string{
	string(domain.StatusCreated):        "ایجادشده",
	string(domain.StatusContentReady):   "درس آماده",
	string(domain.StatusExercisesReady): "تمرین آماده",
	string(domain.StatusAnswered):       "پاسخ‌داده‌شده",
	string(domain.StatusEvaluated):      "ارزیابی‌شده",
}

var statusOrder = []domain.TopicStatus{domain.StatusCreated, domain.StatusContentReady,
	domain.StatusExercisesReady, domain.StatusAnswered, domain.StatusEvaluated}

func (a *app) listTopics(w http.ResponseWriter, r *http.Request) {
	f := topics.Filter{Query: r.URL.Query().Get("q"), Status: r.URL.Query().Get("status"),
		Archived: r.URL.Query().Get("archived") == "1"}
	if _, ok := statusLabels[f.Status]; !ok {
		f.Status = "" // unknown status: show everything rather than an empty page
	}
	tree, matched, total, err := a.topics.List(r.Context(), f)
	if err != nil {
		slog.Error("listing topics", "error", err)
		http.Error(w, "خطای داخلی", http.StatusInternalServerError)
		return
	}
	opts := make([]ruleOption, len(statusOrder))
	for i, s := range statusOrder {
		opts[i] = ruleOption{Value: string(s), Label: statusLabels[string(s)], Selected: string(s) == f.Status}
	}
	data := &pageData{Title: "موضوعات", Query: f.Query, Status: f.Status, Archived: f.Archived, Statuses: opts,
		Topics: tree, Matched: matched, TopicTotal: total}
	name := "layout"
	if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-History-Restore-Request") != "true" {
		name = "topic-results"
	}
	a.render(w, http.StatusOK, "topics", name, data)
}

func (a *app) archiveTopic(w http.ResponseWriter, r *http.Request)   { a.setArchived(w, r, true) }
func (a *app) unarchiveTopic(w http.ResponseWriter, r *http.Request) { a.setArchived(w, r, false) }

// setArchived changes only topics.state (T9.12) and goes back to the topic page.
func (a *app) setArchived(w http.ResponseWriter, r *http.Request, archived bool) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	err := a.topics.SetArchived(r.Context(), id, archived)
	switch {
	case err == nil:
		http.Redirect(w, r, "/topics/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
	case errors.Is(err, domain.ErrNotFound):
		http.NotFound(w, r)
	case errors.Is(err, domain.ErrScopeBusy):
		d, err := a.panelData(r, id, "", nil)
		if err != nil {
			slog.Error("loading topic panel", "topic", id, "error", err)
			http.Error(w, "خطای داخلی", http.StatusInternalServerError)
			return
		}
		d.Error = "کاری روی این موضوع در جریان است؛ پس از پایان آن دوباره تلاش کنید."
		a.fillLesson(r, d)
		a.render(w, http.StatusConflict, "topic", "layout", d)
	default:
		slog.Error("archiving topic", "topic", id, "error", err)
		http.Error(w, "خطای داخلی", http.StatusInternalServerError)
	}
}
