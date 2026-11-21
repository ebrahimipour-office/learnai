package httpserver_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"learninghub/internal/ai/fake"
)

func (e *env) count(q string) (n int) {
	e.t.Helper()
	if err := e.db.Writer().QueryRow(q).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// T11.3: build review cards from the lesson's keywords, review them with Leitner, see due cards on /topics.
func TestFlashcards(t *testing.T) {
	e := newEnv(t)
	e.seedTopics()
	build := func() int { return e.post("/topics/1/jobs/flashcards", nil, true).Code }
	run := func() string {
		id, _ := e.lastJob()
		e.pollUntilDone(id)
		_, st := e.lastJob()
		return st
	}

	// A lesson without keywords: disabled button with the reason, and the server refuses.
	noKW, _ := json.Marshal(map[string]any{"lesson_markdown": "درس بدون کلیدواژه.", "keywords": []any{}, "suggestions": []any{}})
	e.ad.Output = noKW
	e.post("/topics/1/jobs/lesson", nil, true)
	run()
	if page := e.get("/topics/1").Body.String(); !strings.Contains(page, `disabled>ساخت کارت‌ها`) || !strings.Contains(page, "کلیدواژه‌ای ندارد") {
		t.Error("no keywords: want a disabled build button with the reason")
	}
	if c := build(); c != http.StatusConflict {
		t.Errorf("no keywords: want 409, got %d", c)
	}

	e.ad.Output = lessonOutput("درس")
	e.post("/topics/1/jobs/lesson", url.Values{"confirm_regenerate": {"1"}}, true)
	run()

	// Invalid output fails the job and stores nothing.
	e.ad.Scenario = fake.InvalidJSON
	if c := build(); c != http.StatusAccepted {
		t.Fatalf("want 202, got %d", c)
	}
	if st := run(); st != "failed" {
		t.Errorf("invalid output: job %s, want failed", st)
	}
	if n := e.count(`SELECT COUNT(*) FROM flashcards`); n != 0 {
		t.Errorf("failed job stored %d cards", n)
	}

	// Hostile Markdown on both sides is sanitized; a card with a blank side is dropped.
	e.ad.Scenario = fake.OK
	e.ad.Output, _ = json.Marshal(map[string]any{"cards": []map[string]string{
		{"front": "روی <script>alert(1)</script> **کارت**", "back": "پشت [x](javascript:alert(2)) <img src=x onerror=alert(3)>"},
		{"front": "دوم", "back": "پاسخ دوم"},
		{"front": "  ", "back": "بی‌روی"},
	}})
	if c := build(); c != http.StatusAccepted {
		t.Fatalf("want 202, got %d", c)
	}
	if st := run(); st != "succeeded" {
		t.Fatalf("flashcards job %s", st)
	}
	if n := e.count(`SELECT COUNT(*) FROM flashcards`); n != 2 {
		t.Fatalf("cards = %d, want 2", n)
	}
	in := e.ad.Inputs()
	if last := in[len(in)-1]; !strings.Contains(last["keywords.json"], "Request") || !strings.Contains(last["lesson.md"], "ادامه متن درس") {
		t.Errorf("AI inputs: want keywords.json and lesson.md, got %v", last)
	}
	page := e.get("/topics/1").Body.String()
	if !strings.Contains(page, "<strong>کارت</strong>") || !strings.Contains(page, "۲ کارت از ۲") {
		t.Error("want both due cards rendered")
	}
	for _, bad := range []string{"<script>alert", "javascript:alert", "onerror="} {
		if strings.Contains(page, bad) {
			t.Errorf("page contains %q", bad)
		}
	}
	if c := build(); c != http.StatusConflict {
		t.Errorf("cards already built for this version: want 409, got %d", c)
	}
	if topics := e.do(httptest.NewRequest(http.MethodGet, "/topics", nil)).Body.String(); !strings.Contains(topics, `href="/topics/1#cards"`) {
		t.Error("/topics: want the due cards link in «برای مرور»")
	}

	var card int64
	if err := e.db.Writer().QueryRow(`SELECT id FROM flashcards ORDER BY id LIMIT 1`).Scan(&card); err != nil {
		t.Fatal(err)
	}
	grade := func(id int64, knew string) int {
		return e.post("/topics/1/cards/"+itoa(id), url.Values{"knew": {knew}}, false).Code
	}
	if c := grade(card, "x"); c != http.StatusBadRequest {
		t.Errorf("bad grade: want 400, got %d", c)
	}
	if c := grade(card+100, "1"); c != http.StatusNotFound {
		t.Errorf("unknown card: want 404, got %d", c)
	}
	if c := grade(card, "1"); c != http.StatusSeeOther {
		t.Fatalf("grade: want 303, got %d", c)
	}
	if n := e.count(`SELECT box FROM flashcards WHERE id = ` + itoa(card)); n != 2 {
		t.Errorf("known card: box %d, want 2", n)
	}
	if !strings.Contains(e.get("/topics/1").Body.String(), "۱ کارت از ۲") {
		t.Error("a graded card is no longer due")
	}
	grade(card, "0")
	if n := e.count(`SELECT box FROM flashcards WHERE id = ` + itoa(card)); n != 1 {
		t.Errorf("unknown answer: box %d, want 1", n)
	}

	// A new lesson version hides the old cards but keeps them.
	e.ad.Output = lessonOutput("درس تازه")
	e.post("/topics/1/jobs/lesson", url.Values{"confirm_regenerate": {"1"}}, true)
	if st := run(); st != "succeeded" {
		t.Fatalf("regenerating the lesson: job %s", st)
	}
	if page := e.get("/topics/1").Body.String(); !strings.Contains(page, ">ساخت کارت‌ها") || strings.Contains(page, "<strong>کارت</strong>") {
		t.Error("new lesson version: want the build button and no old cards")
	}
	if n := e.count(`SELECT COUNT(*) FROM flashcards`); n != 2 {
		t.Errorf("old cards must stay: %d", n)
	}
}
