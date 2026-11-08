package httpserver

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"learninghub/internal/domain"
)

var suggestionKindLabels = map[string]string{"prerequisite": "پیش‌نیاز", "supplementary": "تکمیلی"}

var suggestionStatusLabels = map[string]string{"pending": "در انتظار تصمیم", "accepted": "تأییدشده", "rejected": "ردشده (بایگانی)"}

func (a *app) showSuggestions(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	a.renderSuggestions(w, r, id, http.StatusOK, "")
}

func (a *app) renderSuggestions(w http.ResponseWriter, r *http.Request, topicID int64, status int, msg string) {
	d, err := a.panelData(r, topicID, "suggestions", nil)
	if err == nil {
		d.Error = msg
		d.Suggestions, err = a.topics.Suggestions(r.Context(), topicID)
	}
	if errors.Is(err, domain.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		slog.Error("loading suggestions", "topic", topicID, "error", err)
		http.Error(w, "خطای داخلی", http.StatusInternalServerError)
		return
	}
	a.render(w, status, "topic", "layout", d)
}

func (a *app) acceptSuggestion(w http.ResponseWriter, r *http.Request) {
	a.decideSuggestion(w, r, func(id int64) error {
		_, err := a.topics.AcceptSuggestion(r.Context(), id)
		return err
	})
}

func (a *app) rejectSuggestion(w http.ResponseWriter, r *http.Request) {
	a.decideSuggestion(w, r, func(id int64) error { return a.topics.RejectSuggestion(r.Context(), id) })
}

// decideSuggestion runs decide on suggestion {id} and goes back to its topic's suggestions tab.
func (a *app) decideSuggestion(w http.ResponseWriter, r *http.Request, decide func(id int64) error) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	topicID, err := a.topics.SuggestionTopic(r.Context(), id)
	if err == nil {
		err = decide(id)
	}
	switch {
	case err == nil:
		http.Redirect(w, r, "/topics/"+strconv.FormatInt(topicID, 10)+"/suggestions", http.StatusSeeOther)
	case errors.Is(err, domain.ErrNotFound):
		http.NotFound(w, r)
	case errors.Is(err, domain.ErrInvalidState):
		a.renderSuggestions(w, r, topicID, http.StatusConflict, "درباره این پیشنهاد قبلاً تصمیم گرفته شده است.")
	default:
		slog.Error("deciding suggestion", "suggestion", id, "error", err)
		http.Error(w, "خطای داخلی", http.StatusInternalServerError)
	}
}
