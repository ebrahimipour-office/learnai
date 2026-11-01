package httpserver

import (
	"log/slog"
	"net/http"

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
	f := topics.Filter{Query: r.URL.Query().Get("q"), Status: r.URL.Query().Get("status")}
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
	data := &pageData{Title: "موضوعات", Query: f.Query, Status: f.Status, Statuses: opts,
		Topics: tree, Matched: matched, TopicTotal: total}
	name := "layout"
	if r.Header.Get("HX-Request") == "true" {
		name = "topic-results"
	}
	a.render(w, http.StatusOK, "topics", name, data)
}
