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
)

// Settings is what the settings page shows and checks (TDD §7.2 /settings, R7.3, R9).
type Settings struct {
	DB      *store.DB
	Adapter ai.Adapter
	Model   string // configured adapter.model; "" = the tool's default

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
	d := Doctor{At: time.Now(), Caps: caps}
	switch {
	case err != nil:
		d.Err, d.Hint = err.Error(), ai.Message(ai.Code(err))
	case !caps.Authenticated:
		d.Hint = ai.Message("auth_required")
	case s.Model != "" && len(caps.Models) > 0 && !slices.Contains(caps.Models, s.Model):
		d.ModelWarn = "مدل «" + s.Model + "» در فهرست `agy models` نیست؛ مقدار `adapter.model` را در config.toml اصلاح کنید."
	}
	return d
}

// doctor returns the cached Probe result, running it first when there is none or rerun is set.
// Probe runs agy several times (up to ~90s), so it is not repeated on every page view.
func (a *app) doctor(ctx context.Context, rerun bool) Doctor {
	a.probeMu.Lock()
	defer a.probeMu.Unlock()
	if a.lastProbe == nil || rerun {
		d := ProbeAdapter(ctx, a.settings)
		if ctx.Err() != nil { // client went away mid-probe; don't cache a canceled result
			return d
		}
		a.lastProbe = &d
	}
	return *a.lastProbe
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
	d := a.doctor(r.Context(), false)
	a.render(w, status, "settings", "layout", &pageData{Title: "تنظیمات", Error: msg, Doctor: &d,
		Model: a.settings.Model, Adapter: a.settings.Adapter.Name(), PrivacyAck: ack, Privacy: PrivacyNotice})
}

func (a *app) rerunDoctor(w http.ResponseWriter, r *http.Request) {
	a.doctor(r.Context(), true)
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
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}
