package httpserver

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"learninghub/internal/domain"
	"learninghub/internal/services/syncsvc"
)

var fileKindLabels = map[string]string{
	"lesson": "درس", "exercises_md": "تمرین‌ها", "manifest": "manifest", "rubric": "روبریک",
	"answer": "پاسخ", "report": "گزارش", "summary": "خلاصه", "result": "نتیجه",
}

// showSync shows the latest scan with a confirm option for each move (R5.3).
func (a *app) showSync(w http.ResponseWriter, r *http.Request) {
	a.renderSync(w, r, http.StatusOK, "")
}

func (a *app) renderSync(w http.ResponseWriter, r *http.Request, status int, errMsg string) {
	run, err := a.sync.Latest(r.Context())
	if err != nil {
		slog.Error("loading sync run", "error", err)
		http.Error(w, "خطای داخلی", http.StatusInternalServerError)
		return
	}
	a.render(w, status, "sync", "layout", &pageData{Title: "همگام‌سازی", Error: errMsg, Sync: run})
}

func (a *app) scanSync(w http.ResponseWriter, r *http.Request) {
	if _, err := a.sync.Scan(r.Context()); err != nil {
		slog.Error("sync scan", "error", err)
		a.renderSync(w, r, http.StatusInternalServerError, "اسکن ناموفق بود؛ هیچ تغییری ذخیره نشد.")
		return
	}
	http.Redirect(w, r, "/sync", http.StatusSeeOther)
}

func (a *app) applySync(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if r.FormValue("confirm") != "1" {
		a.renderSync(w, r, http.StatusConflict, "برای ثبت جابه‌جایی‌ها، تأیید لازم است.")
		return
	}
	var ids []int64
	for _, v := range r.Form["move"] {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			a.renderSync(w, r, http.StatusUnprocessableEntity, "انتخاب نامعتبر است.")
			return
		}
		ids = append(ids, n)
	}
	_, err := a.sync.ApplyMoves(r.Context(), id, ids)
	switch {
	case err == nil:
		http.Redirect(w, r, "/sync", http.StatusSeeOther)
	case errors.Is(err, domain.ErrNotFound):
		http.NotFound(w, r)
	case errors.Is(err, domain.ErrInvalidInput):
		a.renderSync(w, r, http.StatusUnprocessableEntity, "هیچ جابه‌جایی معتبری انتخاب نشده است.")
	case errors.Is(err, domain.ErrInvalidState):
		a.renderSync(w, r, http.StatusConflict, "تصمیم‌های این اسکن قبلاً ثبت شده است؛ دوباره اسکن کنید.")
	case errors.Is(err, syncsvc.ErrStale):
		a.renderSync(w, r, http.StatusConflict, "فایل‌ها از زمان اسکن تغییر کرده‌اند؛ دوباره اسکن کنید. هیچ تغییری ثبت نشد.")
	default:
		slog.Error("applying sync moves", "run", id, "error", err)
		a.renderSync(w, r, http.StatusInternalServerError, "ثبت ناموفق بود؛ هیچ تغییری ذخیره نشد.")
	}
}
