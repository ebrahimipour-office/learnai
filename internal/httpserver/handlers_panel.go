package httpserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"learninghub/internal/domain"
	"learninghub/internal/jobs"
	"learninghub/internal/render"
	"learninghub/internal/services/qa"
	"learninghub/internal/services/topics"
)

var jobStatusLabels = map[string]string{
	"queued": "در صف", "running": "در حال اجرا", "succeeded": "موفق", "failed": "ناموفق", "canceled": "لغوشده",
}

var jobPhaseLabels = map[string]string{
	"prepare": "آماده‌سازی", "ai": "تولید با هوش مصنوعی", "validate": "اعتبارسنجی", "commit": "ثبت",
}

var jobKindLabels = map[string]string{
	"lesson": "تولید درس", "exercises": "تولید تمرین", "evaluate": "ارزیابی", "roadmap_analyze": "تحلیل نقشه راه", "chapter_detect": "تشخیص فصل‌ها", "qa": "پرسش درباره درس", "flashcards": "ساخت کارت‌های مرور",
}

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

// tabJobKind is the one job kind whose card a tab shows (B7); suggestions has none.
var tabJobKind = map[string]domain.JobKind{
	"": domain.JobKindLesson, "exercises": domain.JobKindExercises, "answers": domain.JobKindExercises, "reports": domain.JobKindEvaluate,
}

// tabTitles names each tab in the page <title> so browser tabs and history tell them apart (T10.3).
var tabTitles = map[string]string{
	"": "درس", "source": "متن فصل", "exercises": "تمرین‌ها", "answers": "فایل‌های پاسخ", "reports": "گزارش‌ها", "suggestions": "پیشنهادها",
}

var lessonTabKinds = map[string][]domain.JobKind{"": {domain.JobKindQA, domain.JobKindFlashcards}}

// panelData loads the topic and its tab's latest job; job overrides the latest one when non-nil.
func (a *app) panelData(r *http.Request, id int64, tab string, job *jobs.Info) (*pageData, error) {
	p, err := a.topics.Panel(r.Context(), id)
	if err != nil {
		return nil, err
	}
	if kind, ok := tabJobKind[tab]; ok && job == nil {
		if job, err = a.runner.Latest(r.Context(), topics.Scope(id), kind); err != nil {
			return nil, err
		}
		// T10.21, T11.3: question and card jobs show under the lesson too, whichever ran last
		for _, k := range lessonTabKinds[tab] {
			q, err := a.runner.Latest(r.Context(), topics.Scope(id), k)
			if err != nil {
				return nil, err
			}
			if q != nil && (job == nil || q.ID > job.ID) {
				job = q
			}
		}
	}
	if job == nil && r.Method == http.MethodGet { // a page view, not a job response
		if err := a.topics.Touch(r.Context(), id); err != nil {
			slog.Warn("recording topic open", "topic", id, "error", err) // the card is a convenience; still show the page
		}
	}
	title := tabTitles[tab] + " · " + p.Title
	if job != nil && job.Active() {
		title = "⏳ " + title // T10.13: visible on a background browser tab
	}
	c := a.settings.Exercises
	return &pageData{Title: title, Panel: p, Job: job, Tab: tab, CountMin: c.CountMin, CountMax: c.CountMax, MaxQuestion: qa.MaxQuestion}, nil
}

func (a *app) showTopic(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	d, err := a.panelData(r, id, "", nil)
	if errors.Is(err, domain.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		slog.Error("loading topic panel", "topic", id, "error", err)
		http.Error(w, "خطای داخلی", http.StatusInternalServerError)
		return
	}
	if d.MergeFrom, err = a.topics.MergeCandidates(r.Context(), id); err != nil {
		slog.Error("listing merge candidates", "topic", id, "error", err)
		http.Error(w, "خطای داخلی", http.StatusInternalServerError)
		return
	}
	a.fillLesson(r, d)
	a.render(w, http.StatusOK, "topic", "layout", d)
}

// showSource is the chapter text tab (§12.9): source.md as on disk now; no job card (T7.0.e).
func (a *app) showSource(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	d, err := a.panelData(r, id, "source", nil)
	if errors.Is(err, domain.ErrNotFound) || (err == nil && d.Panel.Source == nil) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		slog.Error("loading topic panel", "topic", id, "error", err)
		http.Error(w, "خطای داخلی", http.StatusInternalServerError)
		return
	}
	if m, err := a.md.get(d.Panel.Source.RelPath); err != nil {
		slog.Warn("reading chapter source", "path", d.Panel.Source.RelPath, "error", err)
		d.Error = fileError(err, "فایل متن فصل روی دیسک پیدا نشد: ", d.Panel.Source.RelPath)
	} else {
		d.Lesson = m.HTML
	}
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
	qs, err := a.topics.Questions(r.Context(), d.Panel.ID)
	if err != nil {
		slog.Error("loading lesson questions", "topic", d.Panel.ID, "error", err) // the lesson still shows
	}
	for _, q := range qs {
		d.Questions = append(d.Questions, qaRow{q.Question, q.CreatedAt, q.Version, render.Markdown([]byte(q.AnswerMD))})
	}
	if d.Deck, err = a.topics.Deck(r.Context(), d.Panel.ID, time.Now()); err != nil {
		slog.Error("loading flashcards", "topic", d.Panel.ID, "error", err)
	}
	if d.Deck != nil {
		for _, c := range d.Deck.Due {
			d.Cards = append(d.Cards, cardRow{c.ID, c.Box, render.Markdown([]byte(c.Front)), render.Markdown([]byte(c.Back))})
		}
	}
	m, err := a.md.get(d.Viewing.RelPath)
	if err != nil {
		slog.Warn("reading lesson file", "path", d.Viewing.RelPath, "error", err)
		d.Error = fileError(err, "فایل درس روی دیسک پیدا نشد: ", d.Viewing.RelPath)
		return
	}
	d.Lesson, d.TOC = m.HTML, m.TOC
}

// respond answers a job action: htmx gets the job-area fragment with status; a plain form
// is redirected back to the panel on success or shown the panel with the notice.
func (a *app) respond(w http.ResponseWriter, r *http.Request, status int, topicID int64, job *jobs.Info, notice string) {
	if r.Header.Get("HX-Request") != "true" && status < 300 {
		http.Redirect(w, r, "/topics/"+strconv.FormatInt(topicID, 10), http.StatusSeeOther)
		return
	}
	d, err := a.panelData(r, topicID, tabOf(r), job)
	if err != nil {
		slog.Error("loading topic panel", "topic", topicID, "error", err)
		http.Error(w, "خطای داخلی", http.StatusInternalServerError)
		return
	}
	d.Notice = notice
	if r.Header.Get("HX-Request") == "true" {
		a.render(w, status, "topic", "job-response", d)
		return
	}
	d.Error = notice
	if d.Tab == "suggestions" {
		if d.Suggestions, err = a.topics.Suggestions(r.Context(), topicID); err != nil {
			slog.Error("loading suggestions", "topic", topicID, "error", err)
		}
	} else if d.Tab != "" {
		if err := a.fillSet(r, d); err != nil {
			slog.Error("loading exercise set", "topic", topicID, "error", err)
		}
	} else {
		a.fillLesson(r, d)
	}
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

// qaRow is one answered question under the lesson; Answer is sanitized by render.Markdown.
type qaRow struct {
	Question, CreatedAt string
	Version             int64
	Answer              template.HTML
}

// askQuestion queues a qa job about the current lesson (T10.21).
func (a *app) askQuestion(w http.ResponseWriter, r *http.Request) {
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
	q, err := qa.CheckQuestion(r.FormValue("question"))
	if err != nil {
		a.respond(w, r, http.StatusUnprocessableEntity, id, nil, "پرسش باید بین ۱ تا "+faDigits.Replace(fmt.Sprint(qa.MaxQuestion))+" نویسه باشد.")
		return
	}
	if len(p.Lessons) == 0 {
		a.respond(w, r, http.StatusConflict, id, nil, "برای پرسیدن ابتدا درس این موضوع را تولید کنید.")
		return
	}
	params, _ := json.Marshal(qa.Params{Question: q})
	_, err = a.runner.Enqueue(r.Context(), jobs.Job{Kind: domain.JobKindQA, Scope: topics.Scope(id), TopicID: id, Params: params})
	a.afterEnqueue(w, r, id, err)
}

// cardRow is one due review card; Front and Back are sanitized by render.Markdown.
type cardRow struct {
	ID          int64
	Box         int
	Front, Back template.HTML
}

// buildCards queues a flashcards job from the current lesson's keywords (T11.3).
func (a *app) buildCards(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	d, err := a.topics.Deck(r.Context(), id, time.Now())
	if err != nil {
		slog.Error("loading flashcards", "topic", id, "error", err)
		http.Error(w, "خطای داخلی", http.StatusInternalServerError)
		return
	}
	switch {
	case d.Keywords == 0:
		a.respond(w, r, http.StatusConflict, id, nil, "درس جاری کلیدواژه‌ای ندارد؛ کارت‌ها از کلیدواژه‌ها ساخته می‌شوند.")
		return
	case d.Total > 0:
		a.respond(w, r, http.StatusConflict, id, nil, "کارت‌های این نسخه درس پیش‌تر ساخته شده‌اند.")
		return
	}
	_, err = a.runner.Enqueue(r.Context(), jobs.Job{Kind: domain.JobKindFlashcards, Scope: topics.Scope(id), TopicID: id, Params: []byte("{}")})
	a.afterEnqueue(w, r, id, err)
}

// gradeCard records «بلد بودم» (knew=1) or «بلد نبودم» (knew=0) for a card and returns to the cards (T11.3).
func (a *app) gradeCard(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	card, err := strconv.ParseInt(r.PathValue("card"), 10, 64)
	knew := r.FormValue("knew")
	if !ok || err != nil {
		http.NotFound(w, r)
		return
	}
	box, boxErr := strconv.Atoi(r.FormValue("box"))
	if (knew != "0" && knew != "1") || boxErr != nil {
		http.Error(w, "پاسخ نامعتبر است", http.StatusBadRequest)
		return
	}
	err = a.topics.GradeCard(r.Context(), id, card, box, knew == "1", time.Now())
	if errors.Is(err, domain.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		slog.Error("grading flashcard", "topic", id, "card", card, "error", err)
		http.Error(w, "خطای داخلی", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/topics/"+strconv.FormatInt(id, 10)+"#cards", http.StatusSeeOther)
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

// loadJob returns the job in {id}; only topic jobs have a card (roadmap_analyze shows on its draft page).
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
