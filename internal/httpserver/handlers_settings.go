package httpserver

import (
	"context"
	"database/sql"
	"errors"
	"learninghub/internal/config"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"learninghub/internal/ai"
	"learninghub/internal/store"
	"learninghub/skills"
)

// Settings is what the settings page shows and checks (TDD §7.2 /settings, R7.3, R9).
type Settings struct {
	DB              *store.DB
	Adapter         ai.Adapter
	Switch          *ai.Switch // the same adapter when the tool is selectable on this page; nil hides the selector
	Model           string     // configured adapter.model; "" = the tool's default
	SkipPermissions bool       // config adapter.skip_permissions (agy only)

	Exercises      config.ExercisesConfig // count bounds for the exercises form
	MaxAnswerBytes int64                  // config limits.max_answer_bytes (evaluation selection)
	Editor         string                 // config editor.command: program run as Editor <abs path>; "" hides «باز کردن»
}

// Doctor is one Probe result as shown on the settings page.
type Doctor struct {
	At        time.Time
	Caps      ai.Capabilities
	Err       string // raw error, for "technical details"
	Hint      string // actionable Persian message; "" = ready
	ModelWarn string
}

// Ready reports whether lesson jobs can run; a model warning alone does not block.
func (d Doctor) Ready() bool { return d.Hint == "" }

// PrivacyNotice is the R9 text the user acknowledges (meta.privacy_ack).
const PrivacyNotice = "پاسخ‌ها و محتوای آموزشی برای پردازش به سرویس هوش مصنوعی ارسال می‌شود."

// ProbeAdapter runs Adapter.Probe and turns the result into actionable messages (also used by `hub doctor`).
func ProbeAdapter(ctx context.Context, s Settings) Doctor {
	caps, err := s.Adapter.Probe(ctx)
	model := ai.ModelFor(s.Adapter, s.Model)
	d := Doctor{At: time.Now(), Caps: caps}
	switch {
	case err != nil:
		d.Err, d.Hint = err.Error(), ai.Message(ai.Code(err))
	case !caps.Authenticated:
		d.Hint = ai.Message("auth_required")
	case model != "" && len(caps.Models) > 0 && !slices.Contains(caps.Models, model):
		d.ModelWarn = "مدل «" + model + "» در فهرست `agy models` نیست؛ مقدار `adapter.model` را در config.toml اصلاح کنید."
	}
	return d
}

// doctor returns the cached Probe result; nil until "run doctor" was pressed. GET /settings
// never probes (CR-48): a cross-site navigation must not spawn the AI tool.
func (a *app) doctor() *Doctor {
	a.probeMu.Lock()
	defer a.probeMu.Unlock()
	return a.lastProbe
}

// probe runs Probe (agy several times, up to ~90s) without holding probeMu, so page views never
// wait behind it, then caches the result.
// shortcut: two concurrent reruns both probe (last write wins); add singleflight if that ever matters.
func (a *app) probe(ctx context.Context) {
	d := ProbeAdapter(ctx, a.settings)
	if ctx.Err() != nil { // client went away mid-probe; don't cache a canceled result
		return
	}
	a.probeMu.Lock()
	a.lastProbe = &d
	a.probeMu.Unlock()
}

func (a *app) privacyAck(ctx context.Context) (string, error) {
	var v string
	err := a.settings.DB.Writer().QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'privacy_ack'`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (a *app) showSettings(w http.ResponseWriter, r *http.Request) {
	a.renderSettings(w, r, http.StatusOK, "")
}

func (a *app) renderSettings(w http.ResponseWriter, r *http.Request, status int, msg string) {
	ack, err := a.privacyAck(r.Context())
	if err != nil {
		slog.Error("reading privacy_ack", "error", err)
		http.Error(w, "خطای داخلی", http.StatusInternalServerError)
		return
	}
	if ack != "" {
		if t, err := time.Parse(time.RFC3339Nano, ack); err == nil {
			ack = t.Local().Format("2006-01-02 15:04:05")
		} else if t, err := time.Parse(time.RFC3339, ack); err == nil {
			ack = t.Local().Format("2006-01-02 15:04:05")
		}
	}
	ups, err := skills.Outdated(a.root)
	if err != nil {
		slog.Error("checking skill versions", "error", err)
		http.Error(w, "خطای داخلی", http.StatusInternalServerError)
		return
	}
	a.render(w, status, "settings", "layout", &pageData{Title: "تنظیمات", Error: msg, Doctor: a.doctor(),
		Model: ai.ModelFor(a.settings.Adapter, a.settings.Model), Adapter: a.settings.Adapter.Name(), SkipPermissions: a.settings.SkipPermissions,
		Adapters: a.adapterNames(), PrivacyAck: ack, Privacy: PrivacyNotice, SkillUpdates: ups})
}

func (a *app) adapterNames() []string {
	if a.settings.Switch == nil {
		return nil
	}
	return a.settings.Switch.Names()
}

// SavedAdapter is the tool picked on the settings page (meta.adapter_kind), else fallback (adapter.kind).
func SavedAdapter(ctx context.Context, db *store.DB, fallback string) string {
	var v string
	if err := db.Writer().QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'adapter_kind'`).Scan(&v); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Error("reading adapter_kind", "error", err)
		}
		return fallback
	}
	return v
}

// selectAdapter switches the AI tool for new jobs. It is refused while any job is queued or
// running, because a job reads the adapter's name, model and Run at different moments.
func (a *app) selectAdapter(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("adapter")
	if a.settings.Switch == nil || !slices.Contains(a.settings.Switch.Names(), name) {
		a.renderSettings(w, r, http.StatusUnprocessableEntity, "ابزار هوش مصنوعی نامعتبر است.")
		return
	}
	var busy int
	if err := a.settings.DB.Writer().QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM jobs WHERE status IN ('queued','running')`).Scan(&busy); err != nil {
		slog.Error("counting active jobs", "error", err)
		http.Error(w, "خطای داخلی", http.StatusInternalServerError)
		return
	}
	if busy > 0 {
		a.renderSettings(w, r, http.StatusConflict, "تا پایان کارهای در حال اجرا نمی‌توان ابزار هوش مصنوعی را عوض کرد.")
		return
	}
	if _, err := a.settings.DB.Writer().ExecContext(r.Context(),
		`INSERT INTO meta (key, value) VALUES ('adapter_kind', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, name); err != nil {
		slog.Error("saving adapter_kind", "error", err)
		http.Error(w, "خطای داخلی", http.StatusInternalServerError)
		return
	}
	_ = a.settings.Switch.Set(name) // validated above
	a.probeMu.Lock()
	a.lastProbe = nil // the doctor result belonged to the previous tool
	a.probeMu.Unlock()
	slog.Info("AI tool selected", "adapter", name)
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// upgradeSkill replaces an outdated installed skill with the embedded one after a backup copy (§13.2).
func (a *app) upgradeSkill(w http.ResponseWriter, r *http.Request) {
	switch err := skills.Upgrade(a.root, r.PathValue("name")); {
	case errors.Is(err, skills.ErrNotOutdated):
		a.renderSettings(w, r, http.StatusNotFound, "نسخه جدیدتری برای این مهارت وجود ندارد.")
	case err != nil:
		slog.Error("upgrading skill", "skill", r.PathValue("name"), "error", err)
		a.renderSettings(w, r, http.StatusInternalServerError, "به‌روزرسانی مهارت ناموفق بود: "+err.Error())
	default:
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
	}
}

func (a *app) rerunDoctor(w http.ResponseWriter, r *http.Request) {
	a.probe(r.Context())
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (a *app) ackPrivacy(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("ack") != "1" {
		a.renderSettings(w, r, http.StatusUnprocessableEntity, "برای ادامه، متن حریم خصوصی را تأیید کنید.")
		return
	}
	_, err := a.settings.DB.Writer().ExecContext(r.Context(),
		`INSERT INTO meta (key, value) VALUES ('privacy_ack', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		slog.Error("saving privacy_ack", "error", err)
		http.Error(w, "خطای داخلی", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/topics", http.StatusSeeOther)
}

// requireAck is the first-run gate (T6.7): until the privacy notice is acknowledged, the topic
// list and every action that sends content to the AI go to the settings page (doctor + notice).
func (a *app) requireAck(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ack, err := a.privacyAck(r.Context())
		switch {
		case err != nil:
			slog.Error("reading privacy_ack", "error", err)
			http.Error(w, "خطای داخلی", http.StatusInternalServerError)
		case ack != "":
			h(w, r)
		case r.Header.Get("HX-Request") == "true":
			w.Header().Set("HX-Redirect", "/settings")
			http.Error(w, "ابتدا متن حریم خصوصی را در تنظیمات تأیید کنید.", http.StatusConflict)
		default:
			http.Redirect(w, r, "/settings", http.StatusSeeOther)
		}
	}
}
