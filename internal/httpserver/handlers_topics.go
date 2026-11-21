package httpserver

import (
	"cmp"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

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

var errorTypeLabels = map[string]string{
	"conceptual": "مفهومی", "logical": "منطقی", "technical": "فنی", "style": "سبک کد", "omission": "جاافتادگی",
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
	resume, err := a.topics.Resume(r.Context())
	if err != nil {
		slog.Error("loading resume card", "error", err)
		http.Error(w, "خطای داخلی", http.StatusInternalServerError)
		return
	}
	rc := a.settings.Review
	review, err := a.topics.Review(r.Context(), cmp.Or(rc.ScoreBelow, 70), cmp.Or(rc.StaleDays, 14), time.Now())
	if err != nil {
		slog.Error("loading review list", "error", err)
		http.Error(w, "خطای داخلی", http.StatusInternalServerError)
		return
	}
	due, err := a.topics.DueCards(r.Context(), time.Now())
	if err != nil {
		slog.Error("loading due flashcards", "error", err) // the list still shows
	}
	opts := make([]ruleOption, len(statusOrder))
	for i, s := range statusOrder {
		opts[i] = ruleOption{Value: string(s), Label: statusLabels[string(s)], Selected: string(s) == f.Status}
	}
	data := &pageData{Title: "موضوعات", Query: f.Query, Status: f.Status, Archived: f.Archived, Statuses: opts,
		Topics: tree, Matched: matched, TopicTotal: total, Resume: resume, Review: review, DueCards: due}
	name := "layout"
	if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-History-Restore-Request") != "true" {
		name = "topic-results"
	}
	a.render(w, http.StatusOK, "topics", name, data)
}

func (a *app) archiveTopic(w http.ResponseWriter, r *http.Request)   { a.setArchived(w, r, true) }
func (a *app) unarchiveTopic(w http.ResponseWriter, r *http.Request) { a.setArchived(w, r, false) }

// mergeTopic hands this empty topic's place to an archived topic of the same roadmap (RK8, T11.2).
func (a *app) mergeTopic(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	from, err := strconv.ParseInt(r.FormValue("from"), 10, 64)
	if !ok || err != nil {
		http.Error(w, "موضوع نامعتبر است", http.StatusBadRequest)
		return
	}
	switch err := a.topics.Merge(r.Context(), id, from); {
	case err == nil:
		http.Redirect(w, r, "/topics/"+strconv.FormatInt(from, 10), http.StatusSeeOther)
	case errors.Is(err, domain.ErrInvalidState):
		http.Error(w, "ادغام فقط برای موضوع خالی و یک موضوع بایگانی‌شده از همان نقشه راه ممکن است.", http.StatusConflict)
	default:
		slog.Error("merging topics", "topic", id, "from", from, "error", err)
		http.Error(w, "خطای داخلی", http.StatusInternalServerError)
	}
}

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
