package httpserver

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"

	"learninghub/internal/ai"
	"learninghub/internal/fsx"
	"learninghub/internal/jobs"
	"learninghub/internal/roadmap"
	"learninghub/internal/services/roadmaps"
	"learninghub/internal/services/topics"
	"learninghub/internal/ui"
)

type app struct {
	sm     *SecurityManager
	drafts *roadmaps.Service
	topics *topics.Service
	runner *jobs.Runner
	root   *fsx.Root
	pages  map[string]*template.Template

	settings  Settings
	probeMu   sync.Mutex
	lastProbe *Doctor
}

// pageData is shared by all pages; each template uses the fields it needs.
type pageData struct {
	Title, CSRF, Error string

	Draft           *roadmaps.Draft
	Expires         string
	RuleValue       string
	Rules           []ruleOption
	Items           []*treeNode
	Included, Total int
	SuggestItems    bool
	Diff            *roadmaps.Diff
	Roadmaps        []roadmaps.RoadmapRef
	RoadmapID       int64 // re-import target preselected in the upload form

	Query, Status       string
	Statuses            []ruleOption
	Topics              []*topics.Node
	Matched, TopicTotal int

	Panel   *topics.Panel
	Viewing *topics.LessonVersion // lesson version shown; nil = none
	Lesson  template.HTML         // sanitized by render.Markdown
	Job     *jobs.Info            // latest job of the topic; nil = none
	Notice  string                // job action feedback inside the job area

	Tab         string // "" (lesson) | "exercises" | "answers" | "reports" | "suggestions"
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

	Doctor              *Doctor
	Adapter, Model      string
	Privacy, PrivacyAck string // notice text; RFC3339 time of acknowledgement or ""
}

type ruleOption struct {
	Value, Label string
	Selected     bool
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
	a := &app{sm: sm, drafts: drafts, topics: topicList, runner: runner, root: root, settings: settings, pages: map[string]*template.Template{}}
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
		"num":         func(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) },
		"sugKind":     func(s string) string { return suggestionKindLabels[s] },
		"sugStatus":   func(s string) string { return suggestionStatusLabels[s] },
	}
	for _, p := range []string{"new", "draft", "topics", "topic", "settings"} {
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
	rule, ok := parseRule(value)
	if !ok {
		a.renderDraft(w, r, http.StatusUnprocessableEntity, d, "قاعده انتخاب نامعتبر است.")
		return
	}
	c := roadmaps.Choice{Rule: rule}
	// Ticks belong to the rule they were shown under; a rule change starts with everything ticked.
	if value == r.FormValue("shown_rule") {
		sel, err := d.Preview(c)
		if err != nil {
			a.renderDraft(w, r, http.StatusUnprocessableEntity, d, "قاعده انتخاب نامعتبر است.")
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
		a.renderDraft(w, r, http.StatusUnprocessableEntity, d, "ذخیره انتخاب ناموفق بود.")
		return
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
	value := ruleValue(d.Choice.Rule)
	data := &pageData{
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
	name := "layout"
	if r.Header.Get("HX-Request") == "true" {
		name = "draft-form"
	}
	a.render(w, status, "draft", name, data)
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

func ruleOptions(selected string) []ruleOption {
	opts := []ruleOption{{Value: "deepest", Label: "عمیق‌ترین سطح سرفصل (پیش‌فرض)"}}
	for n := 1; n <= 6; n++ {
		opts = append(opts, ruleOption{Value: fmt.Sprintf("level-%d", n), Label: fmt.Sprintf("سرفصل‌های سطح %d", n)})
	}
	opts = append(opts, ruleOption{Value: "items", Label: "آیتم‌های لیست"})
	for i := range opts {
		opts[i].Selected = opts[i].Value == selected
	}
	return opts
}
