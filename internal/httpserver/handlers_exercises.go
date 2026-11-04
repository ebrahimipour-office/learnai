package httpserver

import (
	"cmp"
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"learninghub/internal/domain"
	"learninghub/internal/jobs"
	"learninghub/internal/render"
	"learninghub/internal/services/topics"
)

var answerStatusLabels = map[string]string{
	topics.AnswerMissing: "گم‌شده", topics.AnswerEmpty: "بدون پاسخ",
	topics.AnswerDone: "پاسخ‌داده‌شده", topics.AnswerChanged: "تغییرکرده پس از ارزیابی",
}

var exerciseTypeLabels = map[string]string{
	"coding": "کدنویسی", "conceptual": "مفهومی", "code_analysis": "تحلیل کد", "problem_solving": "حل مسئله",
}

var difficultyLabels = map[string]string{"easy": "آسان", "medium": "متوسط", "hard": "دشوار"}

// exerciseRow is one exercise with the state of its answer file on disk.
type exerciseRow struct {
	topics.Exercise
	Status   string
	FullPath string // absolute path for the copy button
}

// tabOf keeps the job area on the tab it was rendered for (forms and polls carry ?tab=).
func tabOf(r *http.Request) string {
	if t := r.FormValue("tab"); t == "exercises" || t == "answers" {
		return t
	}
	return ""
}

func (a *app) showExercises(w http.ResponseWriter, r *http.Request) {
	a.showSetTab(w, r, "exercises")
}

func (a *app) showAnswers(w http.ResponseWriter, r *http.Request) {
	a.showSetTab(w, r, "answers")
}

func (a *app) showSetTab(w http.ResponseWriter, r *http.Request, tab string) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	d, err := a.panelData(r, id, nil)
	if err == nil {
		d.Tab = tab
		err = a.fillSet(r, d)
	}
	if errors.Is(err, domain.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		slog.Error("loading exercise set", "topic", id, "error", err)
		http.Error(w, "خطای داخلی", http.StatusInternalServerError)
		return
	}
	a.render(w, http.StatusOK, "topic", "layout", d)
}

// fillSet loads the set in ?v= (default: current) and the state of each answer file.
func (a *app) fillSet(r *http.Request, d *pageData) error {
	sets, err := a.topics.Sets(r.Context(), d.Panel.ID)
	if err != nil {
		return err
	}
	d.Sets = sets
	want, _ := strconv.ParseInt(r.URL.Query().Get("v"), 10, 64)
	for i := range sets {
		if (want == 0 && sets[i].Current) || sets[i].Version == want {
			d.Set = &sets[i]
			break
		}
	}
	if d.Set == nil {
		return nil
	}
	list, err := a.topics.Exercises(r.Context(), d.Set.ID)
	if err != nil {
		return err
	}
	for _, x := range list {
		row := exerciseRow{Exercise: x, FullPath: filepath.Join(a.root.BaseDir(), filepath.FromSlash(x.AnswerRel))}
		if d.Tab == "answers" {
			sha, _, err := a.root.HashFile(x.AnswerRel)
			if err != nil {
				slog.Warn("hashing answer file", "path", x.AnswerRel, "error", err)
				sha = ""
			}
			row.Status = x.AnswerStatus(sha)
		}
		d.Exercises = append(d.Exercises, row)
	}
	if d.Tab == "exercises" {
		b, err := a.root.ReadFile(d.Set.ExercisesRel)
		if err != nil {
			slog.Warn("reading exercises file", "path", d.Set.ExercisesRel, "error", err)
			d.Error = "فایل تمرین‌ها روی دیسک پیدا نشد: " + d.Set.ExercisesRel
			return nil
		}
		d.ExercisesMD = render.Markdown(b)
	}
	return nil
}

func (a *app) generateExercises(w http.ResponseWriter, r *http.Request) {
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
	var sets []topics.SetVersion
	if err == nil {
		sets, err = a.topics.Sets(r.Context(), id)
	}
	if err != nil {
		slog.Error("loading topic", "topic", id, "error", err)
		http.Error(w, "خطای داخلی", http.StatusInternalServerError)
		return
	}
	if len(p.Lessons) == 0 {
		a.respond(w, r, http.StatusConflict, id, nil, "برای ساخت تمرین ابتدا درس این موضوع را تولید کنید.")
		return
	}
	confirm := r.FormValue("confirm_new_version") == "1"
	if len(sets) > 0 && !confirm {
		a.respond(w, r, http.StatusConflict, id, nil, "مجموعه جدید نیاز به تأیید دارد؛ فایل‌های مجموعه فعلی و پاسخ‌هایتان دست‌نخورده می‌مانند.")
		return
	}
	count := 0 // 0 = config count_default
	if s := strings.TrimSpace(r.FormValue("count")); s != "" {
		c := a.settings.Exercises
		if count, err = strconv.Atoi(s); err != nil || count < c.CountMin || count > c.CountMax {
			a.respond(w, r, http.StatusUnprocessableEntity, id, nil,
				"تعداد تمرین باید بین "+strconv.Itoa(c.CountMin)+" و "+strconv.Itoa(c.CountMax)+" باشد.")
			return
		}
	}
	params, _ := json.Marshal(map[string]any{"confirm_new_version": confirm, "count": count})
	_, err = a.runner.Enqueue(r.Context(), jobs.Job{Kind: domain.JobKindExercises, Scope: topics.Scope(id), TopicID: id, Params: params})
	a.afterEnqueue(w, r, id, err)
}

// serveFile serves a registered file by id; the path comes only from the DB (Section 8).
func (a *app) serveFile(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	f, err := a.topics.File(r.Context(), id)
	if errors.Is(err, domain.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		slog.Error("loading file", "file", id, "error", err)
		http.Error(w, "خطای داخلی", http.StatusInternalServerError)
		return
	}
	if f.Locked {
		http.Error(w, "معیار ارزیابی پیش از ارزیابی همه تمرین‌های این مجموعه نمایش داده نمی‌شود.", http.StatusForbidden)
		return
	}
	b, err := a.root.ReadFile(f.RelPath)
	if err != nil {
		slog.Warn("reading file", "path", f.RelPath, "error", err)
		http.Error(w, "فایل روی دیسک پیدا نشد.", http.StatusNotFound)
		return
	}
	name := path.Base(f.RelPath)
	ctype, disp := "application/octet-stream", "attachment"
	switch strings.ToLower(path.Ext(name)) {
	case ".md", ".txt":
		ctype, disp = "text/plain; charset=utf-8", "inline"
	case ".json":
		ctype, disp = "application/json", "inline"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disp, map[string]string{"filename": name}))
	_, _ = w.Write(b)
}

// evaluate enqueues an evaluation of the current set (R3.4); 422 when no answer would go to the AI.
// The job handler (T5.2) selects again at run time, since answers can change while it is queued.
func (a *app) evaluate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	mode := cmp.Or(r.FormValue("mode"), topics.ModeIncremental)
	if mode != topics.ModeIncremental && mode != topics.ModeFull {
		a.respond(w, r, http.StatusUnprocessableEntity, id, nil, "حالت ارزیابی باید «افزایشی» یا «کامل» باشد.")
		return
	}
	if _, err := a.topics.Panel(r.Context(), id); errors.Is(err, domain.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	var list []topics.Exercise
	sets, err := a.topics.Sets(r.Context(), id)
	for _, s := range sets {
		if err == nil && s.Current {
			list, err = a.topics.Exercises(r.Context(), s.ID)
		}
	}
	var sel *topics.Selection
	if err == nil {
		sel, err = topics.SelectForEvaluation(a.root, list, mode, a.settings.MaxAnswerBytes)
	}
	if errors.Is(err, domain.ErrNothingToEvaluate) {
		msg := "پاسخ جدید یا تغییرکرده‌ای برای ارزیابی نیست."
		if n := len(sel.Review); n > 0 {
			msg += " " + strconv.Itoa(n) + " پاسخ بزرگ‌تر از سقف یا غیرمتنی است و نیازمند بازبینی دستی است."
		}
		a.respond(w, r, http.StatusUnprocessableEntity, id, nil, msg)
		return
	}
	if err != nil {
		slog.Error("selecting answers", "topic", id, "error", err)
		http.Error(w, "خطای داخلی", http.StatusInternalServerError)
		return
	}
	params, _ := json.Marshal(map[string]string{"mode": mode})
	_, err = a.runner.Enqueue(r.Context(), jobs.Job{Kind: domain.JobKindEvaluate, Scope: topics.Scope(id), TopicID: id, Params: params})
	a.afterEnqueue(w, r, id, err)
}
