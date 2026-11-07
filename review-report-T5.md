# Review Report: Phase 5 (T5.1–T5.8)

**Date:** 2026-11-07
**Scope:** `d56c290` (T5.1) through `3ca7d6d` (T5.8). That is 31 files, +2573/−38. Each task was checked against its row in `technical-design-document.md` §9 (stage 5) and the commit/recovery rules in §6.4.
**Naming:** findings are **E1–E7**.

**Method:**
- These pass at `ae6c921`: `go test ./...`, `go test -race -count=1` (evaluation, scoring, topics, httpserver) and `CGO_ENABLED=0 GOOS=windows go build ./...`.
- `gofmt -l` still lists the same 6 older files (L9). None of them are T5 files.
- Findings come from reading the code and grepping for callers. I wrote no probe tests.
- A real `agy` run was not done, so T5.8 is still open.

---

## 1. Verdict

Phase 5 is solid. The acceptance criteria for T5.1–T5.7 are met and tested:
- Unanswered answers are never sent.
- A retry runs only the unfinished exercises.
- The raw answer is embedded byte-for-byte (property test).
- `attempt-K` is never overwritten.
- The topic score is never taken from the AI.
- The UI values match `result.json`.

There is **one real gap (E1)**: the crash-recovery half of the commit protocol is missing. T5.8 also still needs its real run before Phase 5 can count as closed.

| Task | Criteria met | Notes |
|---|---|---|
| T5.1 select inputs | ✅ | E2, E5 |
| T5.2 handler + checkpoints | ✅ | E6 |
| T5.3 scoring | ✅ | 16 aggregate cases + 5 mismatch cases (≥ 15 required) |
| T5.4 reports | ✅ | property test R11.2-5 |
| T5.5 commit + history | ✅ (normal path) | **E1** (crash path) |
| T5.6 guards | ✅ | E4 |
| T5.7 reports UI | ✅ | E2 |
| T5.8 calibration | ⏳ harness only | E3 |

---

## 2. Findings

### E1 — High: `commit_journal` is written but never read, so a crash mid-commit blocks every later evaluation
- **Where:** `internal/services/evaluation/commit.go:140-163` writes the journal and then the files. `internal/jobs/runner.go:203` `Recover` only marks jobs `failed/interrupted`.
- **Expected:** TDD §6.4 (line 1143) says that at startup, files listed in the journal that are on disk, match its hash and are not in `files` must be deleted. RK9 relies on this too.
- **What happens:** suppose the process dies after `WriteNew` but before the DB transaction commits. Then `reports/vN/attempt-K/*` stays on disk with no `attempts` row. The next evaluation computes the same K and hits a name collision in `WriteNew`. The commit is refused correctly, so nothing is overwritten. But every later evaluation of that set fails the same way until the user deletes the folder by hand.
- **Scope:** the lesson commit (`lessons.go:280`) and the exercise-set commit (`exercises.go:225`) write journals that are also never read. The same wedge applies to them.
- **Fix:** add a journal sweep in `Runner.Recover`. For each job that has `commit_journal` and is not `succeeded`, delete each listed path whose hash matches and that has no `files` row. One shared function fixes all three commits. Add a test: write the files, skip the transaction, run `Recover`, then evaluate again and expect `attempt-K` to succeed.

### E2 — Medium: answers held for manual review show as "unanswered" whenever anything else is sent
- **Where:** `topics/select.go` puts oversized or binary answers in `sel.Review`. `commit.go:carried` then records them as `unanswered` (score 0). The UI (`itemStatusLabels`) has no "needs review" label.
- **What happens:** the 422 message names the review count only when *nothing* is sent. If one other answer is sent, a 2 MB answer silently counts as 0 and is labeled «بدون پاسخ» (unanswered). That contradicts R3.4/RK17 («نیازمند بازبینی دستی», needs manual review).
- **Fix:** store the review reason on the item: either a `needs_review` flag in `attempt_items.flags`, which already exists, or a distinct status. Show it with the existing «⚠ نیازمند بازبینی» badge. The score can stay 0.

### E3 — Medium: T5.8 acceptance is not met yet
- The harness exists (`calibrate_test.go`, `HUB_CALIBRATE=1`). The criteria are not measured yet: at least 4 of 5 answers inside their expected range, and spread ≤ 15 across 3 repeats. PROGRESS.md marks this `[~]`, which is correct. The current-state row says "Phase 5 complete ✅". Keep the "real T5.8 run deferred" caveat visible there until the run is recorded.

### E4 — Low: the injection guard is a phrase list (already documented)
- `guard.go` carries a `shortcut:` comment that states the limit. Obfuscated or paraphrased injections pass. This is acceptable under RK5, because the answerer is the user themself and the flag never changes the score. No action needed. Listed so the risk is on record.

### E5 — Low: any header edit counts as a "changed" answer
- `SelectForEvaluation` hashes the whole file, not the content after the answer marker. PROGRESS already notes this for T5.1. So in incremental mode, editing only the `@hub` header re-sends the answer and costs one AI call. This is not a correctness issue. Fixing it would mean hashing `fsx.ExtractAnswerContent`, which also changes `EvaluatedSHA` and its comparisons.

### E6 — Low: two timestamp formats in one attempt
- `commit.go:67-72`: `started_at` comes from SQLite (`%Y-%m-%dT%H:%M:%fZ`, milliseconds), while `finished_at` is Go `RFC3339` (seconds). Both are valid RFC 3339, but they sort and display differently. Use one format, for example `time.RFC3339Nano` truncated to milliseconds, or `nowSQL` for both.

### E7 — Low: "no current set" gives a misleading 422
- `handlers_exercises.go evaluate`: if the topic has no current set, `list` is empty and the user sees «پاسخ جدید یا تغییرکرده‌ای برای ارزیابی نیست» ("no new or changed answer"). The handler itself would fail with `ErrInvalidState`. This is cosmetic, because the button only shows on the set tabs.

---

## 3. Checked and fine
- **Unanswered never sent:** the selection is repeated at run time (`evaluation.go:110`). The tests count fake adapter calls.
- **Retry and fingerprint:** the fingerprint covers model, skill, AGENTS, profile, exercises.md, rubric, and every sent answer's id and hash. Inherited steps copy staging into the new job with `WriteNew`. If staging is missing, the step simply runs again.
- **Parallel steps:** the semaphore is acquired in order, the first error is kept, and the step outcome is written with `WithoutCancel`. The race detector is clean.
- **`Check`:** the right exercise, each criterion scored exactly once, and no score above `max_points`. Negative scores are rejected by the schema (`minimum: 0`).
- **What the AI saw is what is reported:** `Pick.Body` is read once, hashed, sent, and embedded. A carried-over item is used only when the hash on disk still equals `EvaluatedSHA`.
- **History:** K = MAX+1. Every file goes through `WriteNew` with `immutable=1`. If the transaction fails, this job's files are removed and earlier attempts are untouched (R11.2-6 test).
- **Reports page:** paths come only from DB rows of the same set (no traversal). The Markdown goes through `render.Markdown`. A carried-over item links to its source attempt.
- **Architecture rules (AGENTS.md):** no CGO, all file I/O through `fsx.Root`, and only the app creates files and rows. The AI returns JSON only.

## 4. Suggested order
1. E1, before T6.5 (cleanup), since both touch `.app/` sweeping. One shared fix covers lessons, exercises and evaluation.
2. E2. This is small: one flag and one label.
3. The real T5.8 run, recorded in PROGRESS.md.
4. E5–E7 when convenient.
