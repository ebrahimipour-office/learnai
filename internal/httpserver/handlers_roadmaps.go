package httpserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"path"
	"reflect"
	"strconv"
	"strings"
	"sync"

	"learninghub/internal/ai"
	"learninghub/internal/domain"
	"learninghub/internal/fsx"
	"learninghub/internal/jobs"
	"learninghub/internal/roadmap"
	"learninghub/internal/services/roadmaps"
	"learninghub/internal/services/syncsvc"
	"learninghub/internal/services/topics"
	"learninghub/internal/ui"
)

type app struct {
	sm     *SecurityManager
	drafts *roadmaps.Service
	topics *topics.Service
	runner *jobs.Runner
	root   *fsx.Root
	sync   *syncsvc.Service
	pages  map[string]*template.Template

	settings  Settings
	probeMu   sync.Mutex
	lastProbe *Doctor
}

// pageData is shared by all pages; each template uses the fields it needs.
type pageData struct {
	Title, CSRF, Error string
	CanOpen            bool // editor.command is set: show «باز کردن» (T6.6)

	Draft           *roadmaps.Draft
	Expires         string
	RuleValue       string
	Rules           []ruleOption
	Items           []*treeNode
	Included, Total int
	SuggestItems    bool
	Diff            *roadmaps.Diff
	Roadmaps        []roadmaps.RoadmapRef
	RoadmapID       int64        // re-import target preselected in the upload form
	AIRule, AILabel string       // T6.2 suggested rule as form value and label; "" = no suggestion
	AIApplied       bool         // the draft's rule is exactly the suggested one
	Chapters        []chapterRow // chapters rule only: editable list instead of Items

	Query, Status       string
	Statuses            []ruleOption
	Topics              []*topics.Node
	Matched, TopicTotal int

	Panel   *topics.Panel
	Viewing *topics.LessonVersion // lesson version shown; nil = none
	Lesson  template.HTML         // sanitized by render.Markdown; also the chapter text on the source tab
	Job     *jobs.Info            // latest job of the topic; nil = none
	Notice  string                // job action feedback inside the job area

	Tab         string // "" (lesson) | "source" | "exercises" | "answers" | "reports" | "suggestions"
	Sets        []topics.SetVersion
	Set         *topics.SetVersion // set shown; nil = none
	Exercises   []exerciseRow
	ExercisesMD template.HTML // sanitized by render.Markdown
	CountMin    int
	CountMax    int

	Attempts     []topics.Attempt
	Attempt      *topics.Attempt // attempt shown; nil = none
	AttemptItems []topics.AttemptItem
	ReportItem   *topics.AttemptItem // exercise whose report is shown; nil = none
	ReportMD     template.HTML       // sanitized by render.Markdown

	Suggestions []topics.Suggestion

	Sync *syncsvc.Run // latest scan; nil = none

	Doctor              *Doctor
	Adapter, Model      string
	Privacy, PrivacyAck string // notice text; RFC3339 time of acknowledgement or ""
}

type ruleOption struct {
	Value, Label string
	Selected     bool
}

// chapterRow is one chapter in the preview; its form fields rebuild the chapters rule (§12.4).
type chapterRow struct {
	N, Line, EndLine int // N is 1-based and is the merge value
	Title, Key       string
	Included, Last   bool
}

// treeNode is a group or a topic in the preview tree, in document order.
type treeNode struct {
	IsGroup  bool
	Title    string
	Key      string
	Included bool
	Outline  string
	Items    []*treeNode
}

func newApp(sm *SecurityManager, drafts *roadmaps.Service, topicList *topics.Service, runner *jobs.Runner, root *fsx.Root, settings Settings) *app {
	a := &app{sm: sm, drafts: drafts, topics: topicList, runner: runner, root: root, settings: settings, pages: map[string]*template.Template{},
		sync: &syncsvc.Service{DB: settings.DB, Root: root}}
	funcs := template.FuncMap{
		"statusLabel": func(s string) string { return statusLabels[s] },
		"jobStatus":   func(s string) string { return jobStatusLabels[s] },
		"jobPhase":    func(s string) string { return jobPhaseLabels[s] },
		"jobKind":     func(s string) string { return jobKindLabels[s] },
		"aiMessage":   ai.Message,
		"base":        path.Base,
		"answerLabel": func(s string) string { return answerStatusLabels[s] },
		"exType":      func(s string) string { return exerciseTypeLabels[s] },
		"difficulty":  func(s string) string { return difficultyLabels[s] },
		"itemStatus":  func(s string) string { return itemStatusLabels[s] },
		"evalMode":    func(s string) string { return evalModeLabels[s] },
		"num":         func(f float64) string { return strconv.FormatFloat(f, 'f', 2, 64) },
		"localTime":   localTime,
		"faDuration":  faDuration,
		"sugKind":     func(s string) string { return suggestionKindLabels[s] },
		"sugStatus":   func(s string) string { return suggestionStatusLabels[s] },
		"fileKind":    func(s string) string { return fileKindLabels[s] },
	}
	for _, p := range []string{"new", "draft", "topics", "topic", "settings", "sync"} {
		a.pages[p] = template.Must(template.New(p).Funcs(funcs).ParseFS(ui.FS, "templates/layout.html", "templates/"+p+".html"))
	}
	return a
}

// render executes into a buffer first so a template error never sends a half page.
func (a *app) render(w http.ResponseWriter, status int, page, name string, d *pageData) {
	d.CSRF = a.sm.CSRFToken()
	var buf bytes.Buffer
	if err := a.pages[page].ExecuteTemplate(&buf, name, d); err != nil {
		slog.Error("rendering template", "page", page, "error", err)
		http.Error(w, "خطای داخلی", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// renderNew shows the upload form with the re-import picker.
func (a *app) renderNew(w http.ResponseWriter, r *http.Request, status int, msg string) {
	list, err := a.drafts.Roadmaps(r.Context())
	if err != nil {
		slog.Error("listing roadmaps", "error", err)
	}
	rid, _ := strconv.ParseInt(r.FormValue("roadmap_id"), 10, 64)
	a.render(w, status, "new", "layout", &pageData{Title: "ورود نقشه راه", Error: msg, Roadmaps: list, RoadmapID: rid})
}

func (a *app) newRoadmap(w http.ResponseWriter, r *http.Request) {
	a.renderNew(w, r, http.StatusOK, "")
}

func (a *app) createDraft(w http.ResponseWriter, r *http.Request) {
	fail := func(status int, msg string) { a.renderNew(w, r, status, msg) }
	file, hdr, err := r.FormFile("file")
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			fail(http.StatusRequestEntityTooLarge, "حجم فایل بیش از ۱ مگابایت است.")
			return
		}
		fail(http.StatusBadRequest, "فایلی انتخاب نشده است.")
		return
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, roadmaps.MaxSourceBytes+1))
	if err != nil {
		fail(http.StatusBadRequest, "خواندن فایل ناموفق بود.")
		return
	}

	var id string
	if v := r.FormValue("roadmap_id"); v != "" {
		rid, perr := strconv.ParseInt(v, 10, 64)
		if perr != nil || rid <= 0 {
			fail(http.StatusUnprocessableEntity, "نقشه راه انتخاب‌شده نامعتبر است.")
			return
		}
		id, err = a.drafts.CreateReimport(r.Context(), rid, hdr.Filename, content)
	} else {
		id, err = a.drafts.CreateDraft(r.Context(), hdr.Filename, content)
	}
	switch {
	case errors.Is(err, roadmaps.ErrTooLarge):
		fail(http.StatusRequestEntityTooLarge, "حجم فایل بیش از ۱ مگابایت است.")
	case errors.Is(err, roadmaps.ErrNoRoadmap):
		fail(http.StatusUnprocessableEntity, "نقشه راه انتخاب‌شده پیدا نشد.")
	case errors.Is(err, roadmaps.ErrInvalidSource):
		fail(http.StatusUnprocessableEntity, "فایل باید Markdown (.md)، غیرخالی و با کدگذاری UTF-8 باشد.")
	case err != nil:
		slog.Error("creating draft", "error", err)
		fail(http.StatusInternalServerError, "ساخت پیش‌نویس ناموفق بود.")
	default:
		http.Redirect(w, r, "/roadmaps/drafts/"+id, http.StatusSeeOther)
	}
}

// loadDraft writes the error response itself and returns nil when the draft is unavailable.
func (a *app) loadDraft(w http.ResponseWriter, r *http.Request) *roadmaps.Draft {
	d, err := a.drafts.Draft(r.Context(), r.PathValue("id"))
	if errors.Is(err, roadmaps.ErrDraftNotFound) {
		a.renderNew(w, r, http.StatusNotFound, "پیش‌نویس پیدا نشد یا منقضی شده است؛ فایل را دوباره بارگذاری کنید.")
		return nil
	}
	if err != nil {
		slog.Error("loading draft", "error", err)
		http.Error(w, "خطای داخلی", http.StatusInternalServerError)
		return nil
	}
	return d
}

func (a *app) showDraft(w http.ResponseWriter, r *http.Request) {
	if d := a.loadDraft(w, r); d != nil {
		a.renderDraft(w, r, http.StatusOK, d, "")
	}
}

func (a *app) updateSelection(w http.ResponseWriter, r *http.Request) {
	d := a.loadDraft(w, r)
	if d == nil {
		return
	}
	value := r.FormValue("rule")
	var rule roadmap.Rule
	ok := false
	switch value {
	case "ai": // apply the stored AI suggestion as is
		if d.AI != nil {
			rule, ok = d.AI.Rule, true
		}
	case string(roadmap.RuleChapters):
		rule, ok = formChapters(r)
	default:
		rule, ok = parseRule(value)
	}
	if !ok {
		a.renderDraft(w, r, http.StatusUnprocessableEntity, d, "قاعده انتخاب نامعتبر است.")
		return
	}
	c := roadmaps.Choice{Rule: rule}
	// Ticks belong to the rule they were shown under; a rule change starts with everything ticked.
	if value == r.FormValue("shown_rule") {
		sel, err := d.Preview(c)
		if err != nil {
			a.renderDraft(w, r, http.StatusUnprocessableEntity, d, selectionError(err, "قاعده انتخاب نامعتبر است."))
			return
		}
		ticked := map[string]bool{}
		for _, k := range r.Form["include"] {
			ticked[k] = true
		}
		for _, t := range sel.Topics {
			if !ticked[t.StableKey] {
				c.Excluded = append(c.Excluded, t.StableKey)
			}
		}
	}
	if err := a.drafts.SetChoice(r.Context(), d, c); err != nil {
		if errors.Is(err, roadmaps.ErrDraftNotFound) {
			http.Error(w, "پیش‌نویس منقضی شده است.", http.StatusNotFound)
			return
		}
		slog.Error("saving draft selection", "error", err)
		a.renderDraft(w, r, http.StatusUnprocessableEntity, d, selectionError(err, "ذخیره انتخاب ناموفق بود."))
		return
	}
	// A chapter suggestion also carries a title; it fills the title field of a first import only (§12.5).
	if value == "ai" && d.RoadmapID == 0 && d.AI.Title != "" {
		if err := a.drafts.SetTitle(r.Context(), d, d.AI.Title); err != nil {
			slog.Error("saving suggested title", "error", err)
			a.renderDraft(w, r, http.StatusUnprocessableEntity, d, "ذخیره عنوان پیشنهادی ناموفق بود.")
			return
		}
	}
	if r.Header.Get("HX-Request") != "true" {
		http.Redirect(w, r, "/roadmaps/drafts/"+d.ID, http.StatusSeeOther)
		return
	}
	a.renderDraft(w, r, http.StatusOK, d, "")
}

func (a *app) confirmDraft(w http.ResponseWriter, r *http.Request) {
	d := a.loadDraft(w, r)
	if d == nil {
		return
	}
	// Section 7: sensitive actions need the explicit confirm parameter on the server too.
	if r.FormValue("confirm") != "1" {
		a.renderDraft(w, r, http.StatusConflict, d, "برای ثبت، تأیید لازم است.")
		return
	}
	// T7.2: the title edited in the preview; on re-import it is used only when "replace" is ticked.
	if _, ok := r.Form["title"]; ok {
		title := r.FormValue("title")
		if d.RoadmapID != 0 && r.FormValue("replace_title") != "1" {
			title = ""
		}
		if err := a.drafts.SetTitle(r.Context(), d, title); err != nil {
			if errors.Is(err, roadmaps.ErrDraftNotFound) {
				a.renderNew(w, r, http.StatusNotFound, "پیش‌نویس پیدا نشد، منقضی شده یا قبلاً ثبت شده است.")
				return
			}
			a.renderDraft(w, r, http.StatusUnprocessableEntity, d, fmt.Sprintf("عنوان نقشه راه حداکثر %d نویسه است.", roadmaps.MaxTitleRunes))
			return
		}
	}
	_, _, err := a.drafts.Confirm(r.Context(), d.ID)
	switch {
	case errors.Is(err, roadmaps.ErrNothingSelected):
		a.renderDraft(w, r, http.StatusUnprocessableEntity, d, "هیچ موضوعی انتخاب نشده است.")
	case errors.Is(err, roadmaps.ErrDraftNotFound):
		a.renderNew(w, r, http.StatusNotFound, "پیش‌نویس پیدا نشد، منقضی شده یا قبلاً ثبت شده است.")
	case err != nil:
		slog.Error("confirming draft", "error", err)
		a.renderDraft(w, r, http.StatusInternalServerError, d, "ثبت ناموفق بود؛ هیچ تغییری ذخیره نشد.")
	default:
		http.Redirect(w, r, "/topics", http.StatusSeeOther)
	}
}

// renderDraft serves the full page, or only the form fragment for htmx requests.
func (a *app) renderDraft(w http.ResponseWriter, r *http.Request, status int, d *roadmaps.Draft, errMsg string) {
	sel, err := d.Preview(d.Choice)
	if err != nil {
		slog.Error("previewing draft", "error", err)
		http.Error(w, "خطای داخلی", http.StatusInternalServerError)
		return
	}
	excluded := map[string]bool{}
	for _, k := range d.Choice.Excluded {
		excluded[k] = true
	}
	items, included := buildTree(sel, excluded)
	diff, err := a.drafts.Diff(r.Context(), d)
	if err != nil {
		slog.Error("diffing draft", "error", err)
		http.Error(w, "خطای داخلی", http.StatusInternalServerError)
		return
	}
	var chapters []chapterRow
	if d.Choice.Rule.Kind == roadmap.RuleChapters {
		key := map[int]string{}
		for _, t := range sel.Topics {
			key[t.Line] = t.StableKey
		}
		for i, c := range d.Choice.Rule.Chapters {
			k := key[c.Line]
			chapters = append(chapters, chapterRow{N: i + 1, Line: c.Line, EndLine: c.EndLine, Title: c.Title, Key: k,
				Included: !excluded[k], Last: i == len(d.Choice.Rule.Chapters)-1})
		}
	}
	value := ruleValue(d.Choice.Rule)
	data := &pageData{
		Chapters:     chapters,
		Title:        "پیش‌نمایش ورود",
		Error:        errMsg,
		Draft:        d,
		Expires:      d.ExpiresAt.Local().Format("2006-01-02 15:04"),
		RuleValue:    value,
		Rules:        ruleOptions(value),
		Items:        items,
		Included:     included,
		Total:        len(sel.Topics),
		SuggestItems: len(sel.Topics) == 0 && d.Choice.Rule.Kind != roadmap.RuleItems,
		Diff:         diff,
	}
	if err := a.draftAI(r, d, data); err != nil {
		slog.Error("loading draft analysis", "error", err)
		http.Error(w, "خطای داخلی", http.StatusInternalServerError)
		return
	}
	name := "layout"
	if r.Header.Get("HX-Request") == "true" {
		name = "draft-form"
	}
	a.render(w, status, "draft", name, data)
}

// draftAI fills the AI-suggestion section: the suggestion and the draft's latest analysis job.
func (a *app) draftAI(r *http.Request, d *roadmaps.Draft, data *pageData) error {
	data.Draft, data.RuleValue = d, ruleValue(d.Choice.Rule)
	if d.AI != nil {
		data.AIRule = ruleValue(d.AI.Rule)
		data.AIApplied = reflect.DeepEqual(d.AI.Rule, d.Choice.Rule)
		for _, o := range ruleOptions(data.AIRule) {
			if o.Selected {
				data.AILabel = o.Label
			}
		}
		if d.AI.Rule.Kind == roadmap.RuleChapters {
			data.AILabel = fmt.Sprintf("%d فصل", len(d.AI.Rule.Chapters))
		}
	}
	// Both draft jobs share the scope, so the latest of either kind is the one to show.
	j, err := a.runner.Latest(r.Context(), roadmaps.DraftScope(d.ID), "")
	data.Job = j
	return err
}

// renderDraftAI serves only the AI-suggestion section (htmx swap and polling).
func (a *app) renderDraftAI(w http.ResponseWriter, r *http.Request, status int, d *roadmaps.Draft, notice string) {
	data := &pageData{Notice: notice}
	if err := a.draftAI(r, d, data); err != nil {
		slog.Error("loading draft analysis", "error", err)
		http.Error(w, "خطای داخلی", http.StatusInternalServerError)
		return
	}
	a.render(w, status, "draft", "draft-ai", data)
}

// draftAIStatus is polled every 2s while the analysis runs; once it ends the page reloads to show the result.
func (a *app) draftAIStatus(w http.ResponseWriter, r *http.Request) {
	d := a.loadDraft(w, r)
	if d == nil {
		return
	}
	if r.URL.Query().Get("poll") == "1" {
		if j, err := a.runner.Latest(r.Context(), roadmaps.DraftScope(d.ID), ""); err == nil && (j == nil || !j.Active()) {
			w.Header().Set("HX-Refresh", "true")
		}
	}
	a.renderDraftAI(w, r, http.StatusOK, d, "")
}

// draftJob queues a roadmap_analyze (T6.2) or chapter_detect (T7.4) job. Its result is only a
// suggestion the user may apply.
func (a *app) draftJob(kind domain.JobKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { a.enqueueDraftJob(w, r, kind) }
}

func (a *app) enqueueDraftJob(w http.ResponseWriter, r *http.Request, kind domain.JobKind) {
	d := a.loadDraft(w, r)
	if d == nil {
		return
	}
	params, _ := json.Marshal(roadmaps.AnalyzeParams{DraftID: d.ID})
	_, err := a.runner.Enqueue(r.Context(), jobs.Job{Kind: kind, Scope: roadmaps.DraftScope(d.ID), Params: params})
	status, notice := http.StatusAccepted, ""
	switch {
	case errors.Is(err, domain.ErrScopeBusy):
		status, notice = http.StatusConflict, "تحلیل دیگری روی این پیش‌نویس در جریان است."
	case err != nil:
		slog.Error("enqueueing draft job", "kind", kind, "error", err)
		http.Error(w, "خطای داخلی", http.StatusInternalServerError)
		return
	}
	if r.Header.Get("HX-Request") != "true" {
		if err == nil {
			http.Redirect(w, r, "/roadmaps/drafts/"+d.ID, http.StatusSeeOther)
			return
		}
		a.renderDraft(w, r, status, d, notice)
		return
	}
	a.renderDraftAI(w, r, status, d, notice)
}

// buildTree nests topics under their groups. Topics arrive in document order and a group is
// created at its first topic, so sibling order matches the document.
func buildTree(sel *roadmap.Selection, excluded map[string]bool) (root []*treeNode, included int) {
	groups := make([]*treeNode, len(sel.Groups))
	var itemsOf func(g int) *[]*treeNode
	itemsOf = func(g int) *[]*treeNode {
		if g < 0 {
			return &root
		}
		if groups[g] == nil {
			groups[g] = &treeNode{IsGroup: true, Title: sel.Groups[g].Title}
			parent := itemsOf(sel.Groups[g].Parent)
			*parent = append(*parent, groups[g])
		}
		return &groups[g].Items
	}
	for _, t := range sel.Topics {
		inc := !excluded[t.StableKey]
		if inc {
			included++
		}
		items := itemsOf(t.Group)
		*items = append(*items, &treeNode{Title: t.Title, Key: t.StableKey, Included: inc, Outline: t.Outline})
	}
	return root, included
}

// Rules travel in forms as "deepest", "items" or "level-N".
func ruleValue(r roadmap.Rule) string {
	if r.Kind == roadmap.RuleLevel {
		return fmt.Sprintf("level-%d", r.Level)
	}
	if r.Kind == "" {
		return string(roadmap.RuleDeepest)
	}
	return string(r.Kind)
}

func parseRule(v string) (roadmap.Rule, bool) {
	switch v {
	case string(roadmap.RuleDeepest), string(roadmap.RuleItems):
		return roadmap.Rule{Kind: roadmap.RuleKind(v)}, true
	}
	if n, err := strconv.Atoi(strings.TrimPrefix(v, "level-")); err == nil && strings.HasPrefix(v, "level-") && n >= 1 && n <= 6 {
		return roadmap.Rule{Kind: roadmap.RuleLevel, Level: n}, true
	}
	return roadmap.Rule{}, false
}

// formChapters rebuilds a chapters rule from the preview's ch_line/ch_end/ch_title fields and applies
// merge=N (chapter N absorbs chapter N+1). Validity is checked later by Draft.Preview.
func formChapters(r *http.Request) (roadmap.Rule, bool) {
	lines, ends, titles := r.Form["ch_line"], r.Form["ch_end"], r.Form["ch_title"]
	if len(lines) != len(ends) || len(lines) != len(titles) {
		return roadmap.Rule{}, false
	}
	chs := make([]roadmap.Chapter, len(lines))
	for i := range lines {
		l, err1 := strconv.Atoi(lines[i])
		e, err2 := strconv.Atoi(ends[i])
		if err1 != nil || err2 != nil {
			return roadmap.Rule{}, false
		}
		chs[i] = roadmap.Chapter{Line: l, EndLine: e, Title: strings.TrimSpace(titles[i])}
	}
	if v := r.FormValue("merge"); v != "" {
		m, err := strconv.Atoi(v)
		if err != nil || m < 1 || m >= len(chs) {
			return roadmap.Rule{}, false
		}
		chs[m-1].EndLine = chs[m].EndLine
		chs = append(chs[:m], chs[m+1:]...)
	}
	return roadmap.Rule{Kind: roadmap.RuleChapters, Chapters: chs}, true
}

// selectionError shows a ValidateChapters message as is, otherwise fallback.
func selectionError(err error, fallback string) string {
	var ce *roadmap.ChapterError
	if errors.As(err, &ce) {
		return ce.Msg
	}
	return fallback
}

func ruleOptions(selected string) []ruleOption {
	opts := []ruleOption{{Value: "deepest", Label: "سرفصل‌های بدون زیرسرفصل (پیش‌فرض)"}}
	for n := 1; n <= 6; n++ {
		opts = append(opts, ruleOption{Value: fmt.Sprintf("level-%d", n), Label: fmt.Sprintf("سرفصل‌های سطح %d", n)})
	}
	opts = append(opts, ruleOption{Value: "items", Label: "آیتم‌های لیست"})
	if selected == string(roadmap.RuleChapters) { // only reachable through an applied chapter suggestion
		opts = append(opts, ruleOption{Value: selected, Label: "فصل‌ها"})
	}
	for i := range opts {
		opts[i].Selected = opts[i].Value == selected
	}
	return opts
}
