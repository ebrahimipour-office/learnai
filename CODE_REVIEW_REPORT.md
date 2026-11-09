# Comprehensive Go Project Code Review

| Item | Value |
|---|---|
| Repository | `learninghub` (Learning Hub) |
| Reviewed commit | `f638844` — `feat(ai): T7.11 selectable AI tool (agy, Claude Code, Codex)` (branch `main`, working tree clean) |
| Review date | 2026-11-08 |
| Go toolchain | `go1.26.4 linux/amd64` (`go.mod`: `go 1.26.4`) |
| Scope | All 176 tracked files: ~20.5k lines of Go (about 11k production, 9.5k test), 3 SQL migrations, 8 JSON Schemas, 7 templates, `app.js`/`app.css`, 5 skills, docs, Makefile, `dist/` |
| Method | Static reading of every package, tracing callers and callees (HTTP → service → store/fsx), and running the toolchain checks. Probes for specific findings ran as throwaway tests injected with `go test -overlay`; the probe files stayed in a scratch directory outside the repo. No repository file other than this report and `CODE_REVIEW_TODO.md` was created or changed. |

Finding IDs are `CR-NN`, sorted by priority. Informational items are `CR-INN`. The same IDs are used in `CODE_REVIEW_TODO.md`.
Each finding is listed **once**: in section 3 (P1) or section 4 (P2/P3). Sections 5–13 analyse each area and refer to those IDs.

---

## 1. Executive Summary

**Overall assessment.** The code base is well above average for its size. The architecture is a clean layered design: thin HTTP handlers, services, and `store`/`fsx` underneath. The security model for a local-only app is carefully implemented. AI output never reaches the disk or the database without schema validation plus app-side cross-checks, and the test suite is large and mostly meaningful. **No P0 issue was found.**

What remains are mostly **reliability defects on unhappy paths**: crashes, kills, odd file names, external processes misbehaving, very large inputs. There is also **one functional defect on a main user flow (CR-01)** that no test covers.

| Dimension | Rating | Summary |
|---|---|---|
| Architecture | Good | Clear package boundaries and an acyclic dependency graph. A few service-to-service dependencies and SQL in two handlers (§2.4). |
| Security posture | Good | Host/Origin/Sec-Fetch-Site/CSRF/CSP enforced; all filesystem access goes through `os.Root`; no shell anywhere; parameterised SQL everywhere; two-layer Markdown sanitisation. Residual risks: AI-tool read scope (CR-11), in-root symlinks (CR-10), Windows `.cmd` shims (CR-13), outdated `x/net` (CR-21). |
| Reliability | Fair | The happy path is solid and the job runner is race-free. But crash recovery for commits is unimplemented (CR-03, CR-05, CR-07); a misbehaving AI child process can discard results or hang shutdown (CR-02); one bad file name disables sync (CR-04). |
| Test quality | Good with gaps | `go test`, `go vet` and `go test -race` all pass, with 45–100 % coverage per package. Missing: tests for crash or partial-write recovery, Windows runtime tests, the suggestion → exercises path (would have caught CR-01), and `cmd/hub`. |
| Performance | Adequate for normal input | Several quadratic paths can be triggered by a 1 MB upload (CR-15, CR-16, CR-17). |

**Main risks, in order:**
1. **CR-01:** accepted AI suggestions can never get exercises.
2. **CR-02:** a valid AI result can be thrown away, or a run can hang, if the CLI leaves a background child holding stdout.
3. **CR-03 / CR-05 / CR-07:** a crash during a commit permanently wedges that topic, or that roadmap re-import, until the user deletes files by hand.
4. **CR-04:** one file with a Windows-reserved or overlong name anywhere under `topics/` makes every sync scan fail.

---

## 2. Repository and Architecture Overview

### 2.1 Actual architecture

A single static binary (`cmd/hub`) with the subcommands `serve`, `doctor`, `migrate`, `rebuild` and `version`. `serve` does the following:

1. Takes the single-instance lock on `.app/hub.lock` with `gofrs/flock`.
2. Opens SQLite (`modernc.org/sqlite`, WAL, one writer connection) and applies the embedded migrations.
3. Opens the base directory as an `os.Root` (`internal/fsx`).
4. Installs the default skills.
5. Builds an `ai.Switch` over three CLI adapters: `agy`, `claude` and `codex`.
6. Starts a job runner (`internal/jobs`) with one handler per job kind: lesson, exercises, evaluate, roadmap_analyze and chapter_detect.
7. Starts an hourly maintenance goroutine.
8. Serves server-rendered `html/template` pages with htmx 2.0.4 on `127.0.0.1:8787`.

**Data flow of an AI job.** HTTP handler → `jobs.Runner.Enqueue` (an `IMMEDIATE` transaction with a per-scope busy check) → dispatcher → service `Handle`. The handler then:

1. **prepare:** reads the DB and files.
2. **compose:** `prompt.Composer` writes a whitelisted sandbox under `.app/work/<job>[/<step>]`.
3. **AI call:** `ai.WatchValid` → `ai.Watch` (stall watchdog) → adapter `Run` (process group, NDJSON stream).
4. **validate:** JSON Schema plus Go cross-checks.
5. **commit:** `jobs.Job.Commit`, an atomic cancel gate, then `WithoutCancel`. It writes files with `fsx.WriteNew`/`WriteAtomic` and then runs one DB transaction, with compensating file deletes if the transaction fails.

### 2.2 Package responsibilities

| Package | Responsibility | Assessment |
|---|---|---|
| `cmd/hub` | CLI, wiring, lifecycle | Clear; holds wiring only. Exit-code and doctor issues: CR-43, CR-44. |
| `internal/domain` | Enums, sentinel errors, job kinds | Appropriate and dependency-free. |
| `internal/config` | TOML config, defaults, validation | Validation gaps (CR-40); stale default path (CR-41). |
| `internal/store` | DB open, pragmas, migrations, `WithTx` | Solid. DSN escaping issue (CR-14). |
| `internal/fsx` | `os.Root` wrapper, safe names, `WriteNew`/`WriteAtomic`, hashing, answer templates | Core safety layer. CR-07, CR-10, CR-34, CR-55. |
| `internal/jobs` | Queue, dispatcher, cancel, commit gate, recover | Concurrency is correct; recovery is incomplete (CR-03, CR-37, CR-38). |
| `internal/ai` (+ `antigravity`, `procgroup`, `prompt`, `fake`) | Adapter interface, retry/stall, 3 CLI dialects, process trees, sandbox composition | Good structure. Process-lifecycle defects: CR-02, CR-12, CR-22–CR-29. |
| `internal/schema` | Go models, JSON Schemas, cross-checks | Strong; a few drift points (CR-30). |
| `internal/roadmap` | Deterministic Markdown parse, selection rules, stable keys, chapters | Correct and well tested (golden tests). Performance: CR-15, CR-16; normalisation: CR-51. |
| `internal/render` | goldmark + bluemonday + BiDi stripping | Excellent; 96.7 % coverage with an XSS corpus. |
| `internal/scoring` | Weighted aggregation | Correct; 100 % coverage. |
| `internal/services/*` | Use cases: roadmaps, topics (read model), lessons, exercises, evaluation, syncsvc, rebuild, maintenance, status | Appropriate granularity. Coupling notes in §2.4. |
| `internal/httpserver` | Routes, middleware, handlers | Thin handlers; security middleware is solid. |
| `internal/ui` | Embedded templates and static assets | No inline scripts; CSP-compatible. |
| `skills` | Embedded default skill files plus installer | Fine. Doc drift: CR-57. |

### 2.3 Dependency relationships (internal imports, verified with `go list`)

```
cmd/hub ──> httpserver, jobs, services/{lessons,exercises,evaluation,roadmaps,topics,maintenance,rebuild}, ai, ai/antigravity, store, fsx, config, skills
httpserver ──> services/{roadmaps,topics,syncsvc}, jobs, store, render, roadmap, ai, config, fsx, ui
services/evaluation ──> services/{exercises,topics}, jobs, ai, ai/prompt, schema, scoring, render, store, fsx, skills
services/exercises ──> jobs, ai, ai/prompt, schema, roadmap, render, config, store, fsx, skills
services/lessons ──> jobs, ai, ai/prompt, schema, roadmap, store, fsx, skills
services/roadmaps ──> jobs, ai, ai/prompt, roadmap, schema, store, fsx, skills
services/topics ──> services/roadmaps, roadmap, schema, store, fsx
services/rebuild ──> services/{roadmaps,exercises,evaluation,status}, roadmap, schema, store, fsx
services/syncsvc ──> services/status, store, fsx
jobs ──> ai, ai/prompt, store, fsx, domain
ai/antigravity ──> ai, ai/procgroup, fsx ;  ai/prompt ──> ai, fsx
leaf packages: ai, ai/procgroup, config, domain, fsx, render, roadmap, schema, store, ui, status
```

There are no cycles. Dependencies point downward: handlers → services → infrastructure.

### 2.4 Architectural concerns (no separate findings unless noted)

- **Service-to-service coupling.** `evaluation` imports `exercises` (heading and manifest helpers) and `topics` (answer selection). `topics` imports `roadmaps` (`TopicDir`). `rebuild` imports four services. This is pragmatic reuse, not duplication; the risk is moderate. Shared pure helpers such as `TopicDir`, answer selection and path builders could later move to a small `internal/layout` package. Not filed as a defect.
- **SQL in HTTP handlers.** `handlers_settings.go` queries `meta` and `jobs` directly (`:77`, `:111`, `:128-141`, `:164`). That is acceptable for settings, but it is why the adapter-switch check cannot be atomic with enqueue (CR-42).
- **Duplicated numbering logic.** `roadmaps/confirm.go:153-158` and `topics/suggestions.go:89-93` each compute `COUNT(*)+1`. Both share the CR-06 defect, and one helper should fix both.
- **Single writer connection.** `WithTx` plus a mutex serialises every write. This is correct for SQLite, but any long work inside a transaction blocks the whole app (CR-16).
- **`ai.Switch` is shared mutable state.** Jobs read `Name()`, `Model()` and `Run()` at different moments, and the only guard is a check in the UI (CR-42, CR-31).
- **The architecture fits the job.** Over-engineering is minimal: the abstractions (`Adapter`, `Switch`, `Composer`, `fsx.Root`) each earn their place. No change is recommended for its own sake.

---

## 3. Critical and High-Priority Findings

No **P0** findings. Four **P1** findings.

### CR-01 — P1 — Functional/Correctness — Exercises can never be generated for an accepted suggested topic

- **Files:**
  - `internal/services/topics/suggestions.go:89-96`
  - `internal/services/exercises/exercises.go:111,145-148`
  - `internal/services/exercises/build.go:91,136`
  - `internal/schema/manifest.schema.json:15`
- **Current implementation:** `AcceptSuggestion` inserts the new topic with `stable_key = "suggestion-"+id` (`suggestions.go:96`). Exercise generation copies `t.StableKey` into `manifest.topic.stable_key` (`build.go:91`). `Build` then validates the manifest against the schema (`build.go:136`), which requires `"pattern": "^[0-9a-f]{16}$"` (`manifest.schema.json:15`).
- **Explanation:** every exercise set for an `origin='suggested'` topic fails validation. The error is mapped to `invalid_output`, so `WatchValid` makes a second, paid AI call that fails the same way, and the job ends `failed(invalid_output)`.
- **Reproduction (confirmed with an overlay probe):** call `exercises.Build(testdata/agy-crit-ids.json, Meta{StableKey: "suggestion-7", …})`. Result: `ai: invalid output: … at '/topic/stable_key': 'suggestion-7' does not match pattern '^[0-9a-f]{16}$'`. The same call with a 16-hex key succeeds.
- **Impact:** a whole documented feature (R5 suggestions → new topic → exercises → evaluation) is dead. Each attempt costs two AI calls, and the user sees a misleading "invalid AI output" error.
- **Recommended solution:** generate a 16-hex key for suggested topics, for example `roadmap.StableKey`-style `fsx.HashString("suggestion:"+id)[:16]`. Add a migration that rewrites existing `suggestion-N` keys, together with any already-written `manifest.json`/answer headers (there are none, since generation always failed). Alternatively, relax the schema pattern to `^([0-9a-f]{16}|suggestion-[0-9]+)$` and check that `rebuild` and the answer template headers accept it.
- **Regression test:** an end-to-end service test. Accept a suggestion, generate a lesson, then generate exercises with the fake adapter, and assert the job is `succeeded` and `manifest.json` validates. Also add a unit test asserting that every `stable_key` the app produces matches the manifest schema pattern.
- **Confidence:** High (reproduced).

### CR-02 — P1 — Process management/AI — A background grandchild holding stdout discards a valid result or hangs the run (earlier finding A1, still open)

- **File:** `internal/ai/antigravity/adapter.go:176-196` (`StdoutPipe` at `:181`; `readStream` then `cmd.Wait()` at `:192-194`), and `outcome()` at `:281-292`.
- **Current implementation:**
  ```go
  cmd.WaitDelay = 5 * time.Second
  stdout, err := cmd.StdoutPipe()
  ...
  res, rawRes := readStream(runCtx, stdout, events, log, dec)
  waitErr := cmd.Wait()
  ```
- **Explanation:**
  - `readStream` reads until EOF, and only then is `Wait` called. `WaitDelay` only bounds the I/O copying that happens inside `Wait`, so it never bounds this read.
  - If the CLI (agy, claude or codex) starts a helper process (MCP server, language server, telemetry uploader) that inherits stdout and outlives the leader, the read blocks until cancel, timeout or stall.
  - `outcome()` checks `ctx.Err()` and `runCtx.Err()` **before** looking at `res.Status`, so a SUCCESS result that has already been received is reported as `timeout` or `stalled`.
  - A helper that called `setsid` escapes the process-group kill entirely, so the run blocks until that helper exits.
  - With `Timeout = 0` and `StallTimeout = 0` (allowed by config, see CR-40) the run hangs forever.
- **Reproduction (overlay probe with a fake CLI that prints a SUCCESS result, runs `sleep 6 &`, and exits 0):**

  | Setting | Result |
  |---|---|
  | `Timeout = 1s` | `ai: timeout: killed after 1.1s`, structured output empty |
  | `Stall = 1s` | `ai: stalled: no event for 1s` |
  | `setsid sleep 6 &` | `Run` returned only after 6.01 s; a long-lived daemon would block indefinitely |
- **Impact:**
  - Valid AI work (tokens, minutes of runtime) is thrown away, with a wrong error code.
  - Cancel and graceful shutdown wait on `runnerDone` (`cmd/hub/main.go:353`) and can hang.
  - It depends on CLI behaviour the app does not control. That behaviour changes between CLI versions, and now spans three different CLIs.
- **Recommended solution:**
  - Do not use `StdoutPipe`. Assign `cmd.Stdout` to an `io.Writer` (a line splitter feeding the NDJSON decoder and the log) so `exec` owns the copy and `WaitDelay` bounds it after the leader exits.
  - Alternatively, run `Wait` concurrently and close the read side once the leader has exited and `WaitDelay` has elapsed.
  - Also make `outcome()` prefer a received final `SUCCESS` result when the leader exited 0.
- **Regression test:** fake-binary scenarios `sleep 30 &` and `setsid sleep 30 &` after a SUCCESS line. Assert `err == nil`, the structured output is set, and `Run` returns within `Timeout + KillGrace + WaitDelay`.
- **Confidence:** High (reproduced with a fake CLI). Whether the real CLIs leave such helpers behind is unverified.

### CR-03 — P1 — Data integrity/Recovery — `commit_journal` is written but never replayed, so a crash during commit wedges a topic (earlier finding E1, still open)

- **Files:**
  - Journal writes: `internal/services/lessons/lessons.go:289-296`, `internal/services/exercises/exercises.go:234-241`, `internal/services/evaluation/commit.go:141-148`
  - Files are written before the transaction: `lessons.go:300-328`, `exercises.go:249-259`, `commit.go:155-164`
  - Recovery: `internal/jobs/runner.go:204-211`
- **Current implementation:**
  - Each commit records a journal, writes its files with `WriteNew`, then runs the DB transaction.
  - `Runner.Recover` only runs `UPDATE jobs SET status='failed' … WHERE status='running'`.
  - `grep commit_journal` finds no reader anywhere in the Go code.
  - Output names are re-derived from the DB: `MAX(attempt_no)+1` (`commit.go:64-67`) and `maxVer+1` (`exercises.go:90-91,111`).
- **Explanation:** if the process dies (kill, crash, power loss, Windows console close) after the files are written and before the transaction commits, the files stay on disk with no rows. The next job computes the same name, `WriteNew` returns `ErrFileExists`, and the commit is refused. This repeats on every later attempt. The same orphan state also arises when an undo `Remove` fails (the error is ignored, e.g. `commit.go:152`) or when a commit panics (CR-33).
  - **First lesson:** `lesson.md` is left behind, so every retry fails.
  - **Lesson regeneration:** `lesson.md` already holds the new text. On retry, the hash comparison with the history copy (`lessons.go:313`) fails every time.
  - **Evaluation:** `attempt-K/` exists, so every evaluation of that set fails. The mechanism is proven by `TestEvaluateCommitCollisionNeverOverwrites` (`evaluation_test.go:431`).
- **Reproduction:** kill the process just before `s.DB.WithTx` at `commit.go:166`, restart, and evaluate again. It fails every time with `file already exists … attempt-1/q01-report.md`.
- **Impact:** a topic is stuck for that job kind until the user finds and deletes the files by hand. No user data is lost (`WriteNew` is never-overwrite), but the DB and disk disagree, and nothing in the UI explains it.
- **Recommended solution:** implement TDD §6.6 (line 1153) once, in a shared function called from `Runner.Recover` before dispatch. For each job with `commit_journal IS NOT NULL` and `status != 'succeeded'`:
  - delete every journal path that exists, whose SHA equals the journal entry, and that has no `files.rel_path` row;
  - for a lesson regeneration, restore `lesson.md` from the history copy recorded in the journal;
  - clear the journal.

  Paths whose hash differs from the journal are kept and reported.
- **Regression test:** for each of the three commit kinds, write the journal and its files without rows, run `Recover`, and assert the files are gone and the next job succeeds with the same K / vN. Add a negative case: a modified file (hash differs) is kept.
- **Confidence:** High.

### CR-04 — P1 — Availability/Correctness — Sync scan fails completely when any unregistered file under `topics/` has a name `fsx` rejects

- **Files:** `internal/services/syncsvc/syncsvc.go:194-205` (walk), `:226-234` (hash), `internal/fsx/root.go:123-126,183-191`.
- **Current implementation:** `unregistered()` walks `topics/` with `fs.WalkDir` and collects every regular file. When at least one registered file is missing, `match()` hashes every candidate through `s.Root.HashFile(p)`, which calls `Root.Open` → `ValidateRelPath`. Any error aborts the scan: `return nil, nil, fmt.Errorf("hashing %s: %w", p, err)`.
- **Explanation:** `ValidateRelPath` rejects:
  - Windows device names (`aux.md`);
  - the characters `?:*<>|"`;
  - a trailing dot or space;
  - paths longer than 220 bytes, which a long Persian file name reaches quickly.

  These names are legal and common on Linux and macOS, and users put their own notes in topic folders.
- **Reproduction (confirmed by a reviewer probe; mechanism verified by reading):** delete `lesson.md`, add `topics/01-http/aux.md` (or `why?.md`, or a 110-character Persian name), then run Scan. The scan returns an error, and `/sync` shows «اسکن ناموفق بود».
- **Impact:** sync is disabled until the user guesses which file to rename. Missing-file detection, move detection and status recomputation all stop.
- **Recommended solution:** in `unregistered` (or before hashing), call `fsx.ValidateRelPath(p)`. If it fails, list the file under a new "unsupported name" category, or as `New`. Never hash it or use it as a move candidate.
- **Regression test:** missing `lesson.md` plus `topics/01-http/aux.md` and `topics/01-http/a?.md`: Scan succeeds, `Missing=[lesson]`, and the odd files are reported but not matched.
- **Confidence:** High.

---
## 4. Medium and Low-Priority Findings

Each entry gives: **Location**, **Current**, **Why**, **Scenario**, **Impact**, **Fix**, **Test**, **Confidence**. "POTENTIAL" marks risks whose trigger was not demonstrated.

### 4.1 P2 — Medium

#### CR-05 — P2 — Data integrity — Leftover files from a crash or a failed undo block roadmap Confirm / re-import forever
- **Location:** `internal/services/roadmaps/confirm.go:105` (`rev-%03d.md` from `MAX(rev_no)+1`); `:207-222` (undo with `_ = s.root.Remove(rel)`); `:268`, `:286`, `:292` (`source.md` and `source-history/source.NNNN.md` numbered from `COUNT(files)`).
- **Current:** files are written with `WriteNew` before `tx.Commit`. Undo errors are discarded. Names are re-derived from DB counters.
- **Why:** files left without rows have three causes: a kill between the write and the commit, power loss with WAL + `synchronous(NORMAL)`, or an undo `Remove` blocked by an antivirus or indexer lock on Windows. The next Confirm chooses the same name and `WriteNew` fails. Same root cause as CR-03, but there is no journal here.
- **Scenario:** an orphan `roadmaps/go/rev-002.md` makes every re-import of `go` fail. An orphan `topics/04-x/source.md` makes the new import fail. Both were reproduced by a reviewer with pre-placed files.
- **Impact:** the roadmap cannot be re-imported. The message says "nothing was saved" and gives no hint about the leftover file.
- **Fix:**
  - If the existing file's SHA equals the bytes about to be written, adopt it (an idempotent retry).
  - Otherwise pick the next free number on disk for rev and history files, and use the CR-06 helper for topic folders.
  - Log undo failures.
- **Test:** pre-place the orphan `rev-002.md` with identical bytes and with different bytes. Re-import succeeds in both cases (adopt, or `rev-003`).
- **Confidence:** High.

#### CR-06 — P2 — Correctness — The global `NN-` topic number comes from `COUNT(*)` and collides after a rebuild
- **Location:** `internal/services/roadmaps/confirm.go:150-158,175-176`; `internal/services/topics/suggestions.go:89-93`; interacts with `internal/services/rebuild/rebuild.go:221-303`.
- **Current:** `SELECT COUNT(*) FROM topics` then `TopicDir(seq+1, title)`. The comment says "Topics are never deleted … gap-free".
- **Why:** `rebuild` keeps the on-disk numbers (`maxSeq`) and skips archived topics that have no folder. After a rebuild, `COUNT(*)` can be lower than the highest `NN` in use.
- **Scenario (reviewer-reproduced):** disk has `roadmaps/go/rev-001.md` (`# Go\n## A`) and `topics/03-b/…`. Rebuild leaves `03-b` and `04-a`. Importing a roadmap with `## B` then fails with `UNIQUE constraint failed: topics.dir_name`.
- **Impact:** any import or suggestion-accept whose slug matches a folder at the computed number fails every time. The `NN` prefix also stops being unique as an invariant.
- **Fix:** one helper used by both callers. Set `seq = max(NN parsed from every topics.dir_name, NN of folders under topics/) + 1`, and skip any candidate folder that exists on disk.
- **Test:** the reproduction above; Confirm succeeds with `05-b` or later. Plus a suggestion-accept variant.
- **Confidence:** High.

#### CR-07 — P2 — Crash safety — `WriteNew` writes protected files in place, so a crash leaves a truncated never-overwrite file
- **Location:** `internal/fsx/writenew.go:34-47`.
- **Current:** `OpenFile(final, O_WRONLY|O_CREATE|O_EXCL)` → write → fsync → close. The file is removed only on a returned error.
- **Why:** TDD §6.4/§6.6 (line 1148) specifies "tmp in the same folder → hard-link to the final name". A crash during the write leaves a partial file under the final name. That name is never overwritten afterwards (reports, `result.json`, answer templates the user then types into). A partial file also defeats CR-03's hash-based cleanup.
- **Scenario:** power loss while writing `answers/v1/q03-….go` leaves a half template. The user then edits a corrupt header.
- **Impact:** permanent corrupt protected files. This needs a crash, so it is rare.
- **Fix:** write `.tmp-<rand>` with `O_EXCL`, fsync, `Root.Link(tmp, final)` (fails if the target exists; `os.Root.Link` exists since Go 1.25), remove the tmp, then fsync the directory (CR-34).
- **Test:** inject a failing writer after a partial write. Assert that the final name does not exist and that no tmp file is left behind.
- **Confidence:** High that the code deviates from the spec. The crash trigger is not reproduced.

#### CR-08 — P2 — Data integrity — The evaluation commit trusts `manifest.json` and `rubric-vN.json` from disk without validation or a DB cross-check
- **Location:** `internal/services/evaluation/commit.go:54-61` (unmarshal only), `:90-123` (loops over `m.Exercises`, uses `me.Weight`); `internal/services/evaluation/evaluation.go:133-143` (rubric parsed without `ValidateRubric`/`CrossCheckRubric`).
- **Current:** `json.Unmarshal(mb, &m)`, with no `schema.ValidateManifest`, no SHA comparison with `files.sha256`, and no check that every sent exercise is in the manifest. The `exercises.weight` DB column is ignored.
- **Why:** `manifest.json` is user-editable (`immutable=0`).
- **Scenario:**
  - The user deletes `q03` from `manifest.json`. The AI evaluation of `q03` is paid for but silently dropped, `total_count` shrinks, and the topic score is computed over fewer exercises.
  - The user edits a weight, and the score changes.
  - A rubric exercise with `criteria: []` passes `Check` with a score of 0.
- **Impact:** wrong official scores, and wasted AI calls.
- **Fix:**
  - Compare the manifest and rubric SHAs with their `files.sha256` rows; or validate them with the schema plus cross-checks and compare them to the DB exercise list.
  - Fail if any `sel.Send` exercise is missing from the manifest.
  - Take weights from the DB (or assert equality).
- **Test:** remove a sent exercise from `manifest.json`. The commit fails with `ErrInvalidState`, and nothing is written.
- **Confidence:** High on the code path. The trigger needs a manual edit.

#### CR-09 — P2 — Correctness/UX — Answers held for manual review are recorded as `unanswered` with score 0 (earlier finding E2, still open)
- **Location:** `internal/services/topics/select.go:47-49,64-66` (fills `sel.Review`); `internal/services/evaluation/commit.go:90-123,237-245` (`carried()` gives `Status: "unanswered"`); `internal/services/evaluation/report.go:209-211` (warning only for `flags`).
- **Current:** `sel.Review` is never read by the commit.
- **Why / Scenario:** a previously evaluated answer grows past `max_answer_bytes` (200 KB) or becomes binary. The user evaluates another answer. The large answer's earlier score is replaced by 0 and labelled «بدون پاسخ». R3.4/RK17 require «نیازمند بازبینی دستی».
- **Impact:** silent score loss and a misleading label.
- **Fix:** record a `needs_review` flag in `attempt_items.flags` (or a distinct status) and render the existing «⚠ نیازمند بازبینی» badge. Optionally carry the previous score.
- **Test:** evaluate q01 and q02; grow q02 past the cap; evaluate q01 again. Assert q02's item is flagged `needs_review` and is not labelled unanswered.
- **Confidence:** High.

#### CR-10 — P2 — Filesystem security — In-root symlinks under `.app/` redirect sandbox deletion and writes into user folders; `Root.RemoveAll` has no path guard (earlier C1 + C2, still open)
- **Location:** `internal/ai/prompt/composer.go:94-105` (`RemoveAll(work)`, `MkdirAll`, `WriteNew`); `internal/fsx/root.go:167-180` (`RemoveAll`, guarded by a comment only); `internal/jobs/runner.go:327`; `internal/services/maintenance/maintenance.go:58-81`.
- **Current:** `os.Root` blocks only links that escape the root. Symlinked intermediate components that stay inside the root are followed.
- **Why:** if `.app/work` (or `.app/work/<job>`) is a symlink or Windows junction to `../topics`, `RemoveAll(".app/work/7")` deletes `topics/7`, and the sandbox is then written into it. A reviewer reproduced this on go1.26.4: `topics/7/answer.go` was deleted and `topics/7/inputs` was created.
- **Who can plant the link:** the user, or any tool process that runs with write access inside the base directory (an AI CLI whose sandbox is misconfigured or bypassed).
- **Impact:** deletion of user files, limited to entries whose names match job IDs (or `<job>/<exID>`). The deletion is narrow, but answers are exactly what the project promises never to lose.
- **Fix:**
  - In `Composer.Compose`, runner cleanup and maintenance, `Lstat` each component of `.app/{work,staging,logs}` and refuse symlinks or reparse points. Or open `.app/work` as its own `os.Root` after an `Lstat` check.
  - Restrict `Root.RemoveAll` to paths strictly under `.app/work/`, `.app/staging/` or `.app/logs/`.
- **Test:** symlink `.app/work` → `topics`. `Compose` fails, and `topics/7` is unchanged. `RemoveAll("topics")` returns an error.
- **Confidence:** High for the mechanism; Medium for exploitability.

#### CR-11 — P2 — Security (AI sandbox) — Claude/Codex runs are not confined to the sandbox for reads, and project settings files are honoured (POTENTIAL)
- **Location:** `internal/ai/antigravity/dialects.go:76-78` (`--tools Read,Glob,Grep --allowedTools Read,Glob,Grep … --setting-sources project`), `:101` (`codex exec --sandbox read-only`).
- **Current:** read tools are allowed with no path scope. Codex's read-only sandbox can read the whole disk. `WorkDir` is inside the base directory.
- **Why:**
  - TDD §6.3/D5 says the AI sees only copies of whitelisted inputs. A prompt injection inside an imported book or `source.md` can make the model read `app.db`, other topics' answers or `~/.ssh/*`, and echo the contents into its JSON output. That output goes to the AI provider and is stored in lessons and reports.
  - `--setting-sources project` lets a `.claude/settings.json` (hooks, env) in the base directory or the git root apply to every run.
- **Impact:** confidentiality of local files towards the AI provider. Possible code execution through hooks if the base directory contains a hostile `.claude/settings.json`.
- **Fix:**
  - Use path-scoped permission rules (e.g. `Read(./**)`), or omit `--allowedTools` so that out-of-cwd reads are denied in `-p` mode.
  - Pass an empty `--setting-sources`.
  - For Codex, document the residual risk.
  - Verify all of this against the real CLIs.
- **Test:** an argument-builder test asserting scoped rules and no project settings. A manual check with real CLIs: a prompt asking to read `../../app.db` is refused.
- **Confidence:** Low–Medium (CLI semantics not verified).

#### CR-12 — P2 — Process management — `Probe` ignores its deadline when a child holds the pipe, and reports timeouts as "not authenticated"
- **Location:** `internal/ai/antigravity/adapter.go:80-85` (`exec.CommandContext(c, bin, args...).CombinedOutput()`), `:104-107`; caller `internal/httpserver/handlers_settings.go:62-73` (holds `probeMu`).
- **Current:** no `WaitDelay` and no process group, so a context cancel kills only the leader, and `CombinedOutput` waits for pipe EOF.
- **Scenario (reviewer-reproduced):** a fake `--version` that runs `sleep 3 &` takes 3.00 s with a 500 ms context. While the probe runs, every `/settings` view blocks on `probeMu`. After the context expires, later sub-probes fail and return `Authenticated=false` with `err == nil`, so the UI shows an "auth required" hint for what was a timeout.
- **Impact:** a stuck settings page and a misleading diagnosis.
- **Fix:** set `cmd.WaitDelay` and use `procgroup` for probe commands. Return context/timeout errors as `ErrTimeout`. Do not hold `probeMu` across the subprocess call (use a singleflight-style result instead).
- **Test:** fake binary with `sleep 3 &`: Probe returns within deadline + `WaitDelay` with a timeout error, not "unauthenticated".
- **Confidence:** High.

#### CR-13 — P2 — Windows/Process execution — npm `.cmd` shims for `claude`/`codex` (and a `.cmd` editor) receive multi-line arguments through `cmd.exe` (POTENTIAL)
- **Location:** `internal/ai/antigravity/adapter.go:66` (`exec.LookPath` resolves `claude.cmd` via PATHEXT); `internal/ai/prompt/composer.go:80` (multi-line prompt); `internal/ai/antigravity/dialects.go:80` (schema text as an argument); `internal/httpserver/handlers_exercises.go:277` (`exec.Command(Editor, path)`).
- **Why:** Go documents that `os/exec` does not quote arguments correctly for `.bat`/`.cmd` files. `cmd.exe` ends the command at a newline and interprets `^ & | % < >` once quoting is toggled by embedded `\"`. Validator error text (which can echo AI-provided values) is appended to the retry prompt (`internal/ai/retry.go:31`).
- **Scenario:** on Windows with an npm-installed `claude`, everything after the first newline of `-p` (the security preamble and later flags) may be lost, and Claude then runs without `--output-format stream-json`.
- **Impact:** Claude and Codex may not work on Windows at all. Possible argument or command injection in the worst case.
- **Fix:** refuse `.bat`/`.cmd` binaries with an actionable message (point to the native `.exe` or the node entry point), or pass the prompt and schema through stdin or a file (`claude -p` reads stdin; `codex exec -`).
- **Test:** a Windows-only test with a `.cmd` echo shim: arguments arrive intact, or are refused.
- **Confidence:** Low–Medium (not run on Windows).

#### CR-14 — P2 — Data integrity/Portability — The SQLite DSN is built from an unescaped path; `#` or `%` in `base_dir` opens the wrong file
- **Location:** `internal/store/db.go:32`.
- **Current:** `dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&…&_txlock=immediate", absPath)`.
- **Why:** SQLite URI parsing treats `#` as the start of a fragment and decodes `%HH` sequences.
- **Scenario (reviewer-reproduced):** `base_dir=…/C#dir` creates the database at `…/C`, outside the base directory, in its parent. `…/pct%41dir` fails with "unable to open database file". `D:\Projects\C#\Learn` is a realistic Windows path.
- **Impact:** the database lives outside the base directory, two base directories can share one `app.db` while each holds its own flock, and `rebuild --db` is affected the same way.
- **Fix:** escape the path before building the URI (`%`→`%25`, `#`→`%23`, `?`→`%3f`), or build it with `url.URL{Scheme: "file", Path: …}`, checking the Windows drive-letter form.
- **Test:** `store.Open(t.TempDir()+"/a#b/app.db")` and `…/p%41/app.db`. The file exists at exactly that path, and no stray file exists in the parent.
- **Confidence:** High.

#### CR-15 — P2 — Performance/DoS — Stable-key de-duplication is quadratic in the number of duplicate titles
- **Location:** `internal/roadmap/stablekey.go:45-56` (`for n := 2; used[p]; n++ { p = base + "#" + strconv.Itoa(n) }`).
- **Why:** the loop restarts at 2 for every duplicate.
- **Measurements:** `strings.Repeat("## h\n", n)` takes 0.28 s for 2.5 k duplicates, 1.19 s for 5 k and 5.4 s for 10 k. A 1 MB upload (~200 k headings) extrapolates to tens of minutes.
- **Impact:** `Select` runs on every draft render, `SetChoice`, `Confirm`, and several times per analyze job, so one pathological upload pins a CPU core for each request.
- **Fix:** keep `next[base]` and start the loop at `max(2, next[base])`. The keys stay identical.
- **Test:** 50 k duplicate headings finish in under 1 s. The existing stable-key golden tests stay green.
- **Confidence:** High (measured).

#### CR-16 — P2 — Performance — `roadmap.Slice` re-normalises the whole file on every call, so `Outline` and `Confirm` are O(headings × size), and `Confirm` does this inside the write transaction
- **Location:** `internal/roadmap/parse.go:81-93`; called once per heading in `internal/services/roadmaps/analyze.go:273` and once per topic in `confirm.go:237` (`recordSource`).
- **Measurements:** a 1 MB file with ~95 k headings takes 1.8 s per 500 calls, about 5.7 minutes in total.
- **Impact:** in `Confirm` this runs while holding the single writer connection in an `IMMEDIATE` transaction, which blocks every other write in the app (job heartbeats and status updates included).
- **Fix:** normalise and compute line starts once (e.g. `roadmap.NewLines(src).Slice(a, b)`). Compute the slices before opening the transaction. Consider a cap on topics per import.
- **Test:** `Outline` over 50 k headings in under 1 s; a benchmark for `Confirm`.
- **Confidence:** High (measured).

#### CR-17 — P2 — Performance — The re-import diff runs unbounded Levenshtein over every new × gone pair on every preview render
- **Location:** `internal/services/roadmaps/diff.go:103-105,113-147`; called from `internal/httpserver/handlers_roadmaps.go:357`.
- **Why:** the cost is O(|New|·|Gone|·L²), with no length cap and no early exit. Renaming the H1 changes every key, so every topic becomes both "new" and "gone".
- **Measurements:** 3000 × 3000 titles of ~25 characters take ~20 s per page load.
- **Impact:** a slow or hung preview, repeated on every htmx update.
- **Fix:** skip pairs whose length ratio already makes similarity < 0.6. Cap compared titles at ~200 runes. Skip guessing when pairs exceed a budget. Cache the result in the unused `import_drafts.diff` column.
- **Test:** `diffTopics` with 3000 × 3000 finishes in under 1 s.
- **Confidence:** High (measured).

#### CR-18 — P2 — Recovery — `hub rebuild` aborts entirely when a topic folder has been copied
- **Location:** `internal/services/rebuild/rebuild.go:243-275`.
- **Why:** the second folder with the same `stable_key` in its manifest or `result.json` falls back to a "suggested" insert with the same key, which violates `UNIQUE(roadmap_id, stable_key)`.
- **Scenario (reviewer-reproduced):** `topics/02-http` and `topics/02-http-bak` with the same manifest. Rebuild fails with `UNIQUE constraint failed`, and nothing is recovered.
- **Impact:** the disaster-recovery tool fails on a common user habit (backup copies of folders).
- **Fix:** track used keys per roadmap. On a clash, use `HashString("rebuild:"+dir)[:16]` and emit a warning.
- **Test:** the reproduction above; rebuild succeeds with two topics and one warning.
- **Confidence:** High.

#### CR-19 — P2 — Recovery (design) — Rebuild always applies the `deepest` rule and puts unmatched topics in the first roadmap (POTENTIAL)
- **Location:** `internal/services/rebuild/rebuild.go:152` (`roadmap.Rule{Kind: roadmap.RuleDeepest}`), `:307-318` (`recoveredRoadmap` = the first roadmap by id).
- **Why:**
  - Topics imported with the `chapters`, `items` or `level` rule do not match by key after a rebuild. They become "suggested" topics in roadmap #1, or are matched by slug and given the slug as their title.
  - Every deepest-rule leaf is inserted as a new active topic.
- **Impact:** after a rebuild, any roadmap that used non-default rules has a distorted topic list. The CLI only prints a generic warning.
- **Fix:** persist the rule on disk (e.g. `roadmaps/<slug>/rule.json` written with `WriteAtomic` at Confirm, or a per-topic `topic.json` holding key, roadmap and range) and read it back during rebuild. Choose the fallback roadmap per folder.
- **Test:** Confirm a chapters-rule import, drop the DB, rebuild, and assert that the keys and the roadmap membership match.
- **Confidence:** Medium.

#### CR-20 — P2 — Frontend reliability — htmx error responses are silently dropped, including every POST after a server restart (stale CSRF token)
- **Location:** `internal/ui/templates/layout.html:6` (`responseHandling` last rule `{"code":"...","swap":false,"error":true}`); `internal/ui/static/app.js:1-15` (no `htmx:responseError`/`htmx:sendError` listener); `internal/httpserver/middleware.go:20-36` (one CSRF token per process).
- **Scenario:** open `/topics/1`, restart `hub serve`, then click «تولید درس». The response is 403 and nothing visible happens. The same applies to 404 (expired draft) and 500 on any htmx action.
- **Impact:** buttons look broken, with no hint to reload. High frequency in normal use (every restart with open tabs).
- **Fix:** one delegated listener in `app.js`: on 403, `location.reload()` (which fetches a fresh token); otherwise show a `role="alert"` message using `textContent`.
- **Test:** an acceptance or browser test: an htmx POST with a wrong token shows an alert or reloads. A template test that asserts the hook exists.
- **Confidence:** High.

#### CR-21 — P2 — Dependency — `golang.org/x/net v0.26.0` is far behind and has known `x/net/html` advisories (reachability likely; not confirmed by govulncheck)
- **Location:** `go.mod:24` (indirect). Used via `bluemonday` → `golang.org/x/net/html.NewTokenizer` (`bluemonday@v1.0.27/sanitize.go:224`) → `internal/render`.
- **Why:**
  - GO-2025-3595 (CVE-2025-22872; tokenizer mis-handles unquoted attributes ending in `/`) is fixed in v0.38.0 and affects the `Tokenizer` that bluemonday uses.
  - GO-2024-3333 (CVE-2024-45338) affects only `html.Parse*`, which bluemonday does not call.
  - `v0.58.0` is already in the local module cache, so upgrading is trivial.
- **Impact:** goldmark without `WithUnsafe` already drops raw HTML, so this weakens the second XSS defence layer rather than opening a direct hole.
- **Fix:** `go get golang.org/x/net@latest && go mod tidy`, then run `make vuln` with network access and record the result.
- **Test:** the existing XSS corpus (`render_test.go`), plus a payload from the CVE.
- **Confidence:** Medium (advisory applicability judged by reading; govulncheck could not run).

### 4.2 P3 — Low

#### CR-22 — P3 — Process management (Unix) — Grandchildren get no SIGTERM grace, and every `Close` arms a stray SIGKILL timer (earlier G1/G3)
- **Location:** `internal/ai/procgroup/procgroup_unix.go:26-46`; called from `adapter.go:194`.
- **Current:** `Kill` sends `SIGTERM` to `-pgid` and `time.AfterFunc(killGrace, SIGKILL -pgid)`. The timer handle is discarded. `release()` sends `SIGKILL` immediately.
- **Why / Scenario:**
  - On cancel, the leader exits promptly, `Close` → `release` SIGKILLs the group at once, and grandchildren lose their grace period.
  - After a successful run, the 3 s timer still fires `kill(-pgid, SIGKILL)`. If the pgid has been reused (likely on macOS, where pid_max is 99999), an unrelated process group is killed.
- **Impact:** unclean child shutdown, and a small chance of killing an unrelated process.
- **Fix:** keep the timer and `Stop()` it in `release`. After a clean exit, send SIGTERM and arm a single timer instead of an immediate SIGKILL. Never re-arm after `Wait` has reaped the group.
- **Test:** a grandchild that traps SIGTERM receives it and has ≥1 s before SIGKILL; no signal is sent 3 s after a successful `Close` (spy on the kill function).
- **Confidence:** High.

#### CR-23 — P3 — Lifecycle — SIGHUP (closing the terminal) is not handled, so AI CLIs keep running (earlier G2)
- **Location:** `cmd/hub/main.go:100,266` (`signal.NotifyContext(…, os.Interrupt, syscall.SIGTERM)`).
- **Why:** the hub dies from SIGHUP's default action without cancelling. The children are in their own process group (`Setpgid`), so they get no SIGHUP and keep running, spending tokens.
- **Fix:** add `syscall.SIGHUP` (in a Unix-only file). Optionally set `Pdeathsig: SIGKILL` on Linux.
- **Test:** a subprocess test that sends SIGHUP to `hub serve` with a running fake job; the job ends `failed/interrupted` and the child is gone.
- **Confidence:** High.

#### CR-24 — P3 — Process management (Windows) — Job Object attach errors are discarded (earlier G6)
- **Location:** `internal/ai/procgroup/procgroup_windows.go:38-49`.
- **Why:** if `OpenProcess`/`AssignProcessToJobObject` fails, a tree kill silently degrades to killing the leader only. This is exactly risk RK6.
- **Fix:** return the error from `Start` (and kill the child), or at least log it.
- **Test:** a Windows test that forces assignment to fail (for example, an already-closed job handle) and asserts an error.
- **Confidence:** High (by reading).

#### CR-25 — P3 — Robustness — Parallel evaluation steps can collide on the log directory name
- **Location:** `internal/ai/antigravity/adapter.go:327-338` (`.app/logs/jobs/<job>/<UTC-ns>-<task>`).
- **Why:** up to 3 steps with the same job and task start concurrently, and the directory name contains no step or exercise ID. A same-tick start makes `WriteNew(prompt.txt)` fail → `internal` error. This is most likely on Windows, whose clock resolution is coarse.
- **Fix:** include `req.Step` (or a random suffix) in the directory name.
- **Test:** two `Run`s with a frozen clock and the same job/task both succeed and write separate logs.
- **Confidence:** Medium–Low.

#### CR-26 — P3 — Cancellation — `Run` with an already-cancelled context returns `ErrToolFailed`, and `WatchValid` retries after cancel
- **Location:** `internal/ai/antigravity/adapter.go:126-191` (no early `ctx.Err()`; `Start` error wrapped as `ErrToolFailed` at `:190`); `internal/ai/retry.go:34`.
- **Impact:** breaks the Adapter contract (`ai.go:27-28`), writes a useless log directory, and gives a wrong error code in places that do not re-map it.
- **Fix:** check `ctx.Err()` at the top of `Run` and before the retry in `WatchValid`.
- **Test:** `Run(cancelledCtx)` → `errors.Is(err, ai.ErrCanceled)`; `WatchValid` makes exactly 1 call when ctx is cancelled after the first invalid output.
- **Confidence:** High.

#### CR-27 — P3 — Resource management — stderr and stdout lines are buffered without bounds
- **Location:** `internal/ai/antigravity/adapter.go:179` (`var stderr bytes.Buffer`), `:262-264` (`ReadBytes('\n')`); `stream.go:81` (lowercased full copy).
- **Impact:** a verbose or misbehaving CLI can grow hub memory without limit during runs that last up to tens of minutes.
- **Fix:** a ring buffer for the last 64 KB of stderr; a per-line cap (e.g. 16 MB) that maps overflow to `invalid_output`.
- **Test:** a fake CLI writes 100 MB to stderr; RSS growth stays bounded and the error includes the tail.
- **Confidence:** Medium (POTENTIAL).

#### CR-28 — P3 — Unicode — Byte-index truncation can split UTF-8 runes
- **Location:** `internal/ai/retry.go:28-29` (`note[:maxRetryNote]`); `internal/ai/antigravity/stream.go:95-96` (`s[:300]`).
- **Impact:** invalid UTF-8 in `jobs.error_message`, in the UI and in the retry prompt, for Persian validator text.
- **Fix:** truncate on a rune boundary (step back while `!utf8.RuneStart`).
- **Test:** truncating a Persian string at every byte length yields `utf8.ValidString`.
- **Confidence:** High.

#### CR-29 — P3 — Error classification — Substring patterns match anywhere in stderr
- **Location:** `internal/ai/antigravity/stream.go:63-88`; `adapter.go:296-308`.
- **Why:** generic words in debug output ("quota project", "/login") classify an unrelated failure as `rate_limited`/`auth_required`, which leads to a wrong actionable hint.
- **Fix:** classify `res.Error` first, then only the last N lines of stderr.
- **Test:** stderr with an early "quota project" debug line followed by a crash → `tool_error`.
- **Confidence:** Medium (POTENTIAL).

#### CR-30 — P3 — Schema drift — AI output schemas are looser than the manifest/rubric schemas they feed; `limits.max_lesson_bytes` is never enforced
- **Location:**
  - `internal/schema/ai-exercises-output.schema.json`: no `maxLength` on title/slug/ext, no `maxItems` on criteria, no `minLength` on `reference_answer.content`.
  - `rubric.schema.json:41,47,54,86` and `manifest.schema.json:57,67-68` do have these limits.
  - `internal/config/config.go:75,121,205`: `MaxLessonBytes` has no consumer (verified by grep).
- **Impact:** not a bypass, since `Build` re-validates (`build.go:135-146`). But Claude's `--json-schema` constrains output only to the loose schema, so over-long values always burn the single retry. Lesson size is unbounded.
- **Fix:** copy the downstream limits into the AI schemas; enforce `MaxLessonBytes` in `lessons.Validate`.
- **Test:** a schema test asserting that every limit in `manifest`/`rubric` is at least as strict in the AI schema; an oversized lesson returns `invalid_output`.
- **Confidence:** High.

#### CR-31 — P3 — Provenance — The evaluation retry fingerprint leaves out the adapter name
- **Location:** `internal/services/evaluation/evaluation.go:158-166` (`fp.WriteString(ai.ModelFor(s.Adapter, s.Model)+"\x00")`); `internal/ai/switch.go:57`.
- **Scenario:** an evaluation fails under `claude` (model `""`). The user switches to `codex` (model `""`) and clicks Retry. The fingerprint is unchanged, so claude's staged results are reused, while `result.json`/`attempts` record `codex` as the evaluator.
- **Fix:** add `s.Adapter.Name()` to the fingerprint.
- **Test:** retry after an adapter switch re-runs every step.
- **Confidence:** High.

#### CR-32 — P3 — Defence in depth — Staged evaluation outputs are not checked against the current pick at commit
- **Location:** `internal/services/evaluation/commit.go:96-111` (`out.AnswerSHA256` and `out.ExerciseID` are ignored).
- **Why:** this is safe today only because the fingerprint gates `inherit()`. A future change to the fingerprint could embed a report for different bytes.
- **Fix:** `if out.ExerciseID != me.ID || out.AnswerSHA256 != pk.SHA { return ErrInvalidState }`.
- **Test:** tamper with a staged output's SHA; the commit fails.
- **Confidence:** High (missing check); no current trigger.

#### CR-33 — P3 — Error handling — A panic inside a commit skips the file undo, and a panic inside a parallel evaluation step crashes the server
- **Location:** commit undo runs only on a returned error: `lessons.go:330-332`, `exercises.go:261-347`, `evaluation/commit.go:166-231` (`store/db.go:105-109` rolls back and re-panics). `evaluation.go:205-210` runs `wg.Go(...)` with no `recover`, while `runner.go:343-354` recovers only the handler goroutine.
- **Impact:** orphan files, which then wedge the topic (CR-03); a step panic takes down the whole process instead of failing the job. No panicking path is known today.
- **Fix:** run undo on any non-success via a `committed` flag in a `defer`; add a `defer recover()` inside the `wg.Go` closure that records the panic as the step error.
- **Test:** inject a panic into a step and into a tx closure; the job ends `failed`, no files remain, and the server is still alive.
- **Confidence:** High (by reading).

#### CR-34 — P3 — Durability — No directory fsync after create/rename, and `WriteAtomic` temp files left by a crash are never cleaned up
- **Location:** `internal/fsx/writenew.go:43` (`WriteNew`), `:91-107` (`WriteAtomic` create → rename).
- **Impact:**
  - On power loss after the SQLite commit, the new directory entry or the rename may be lost, so rows point to missing files or `lesson.md` reverts.
  - `.tmp-lesson.md-<hex>` leftovers stay forever and show up in sync as "new".
- **Fix:** fsync the parent directory on Unix (a no-op on Windows); sweep `.tmp-*` files older than N minutes at startup.
- **Test:** a unit test with an injected directory-sync spy; a maintenance test that removes stale tmp files and keeps fresh ones.
- **Confidence:** Medium (depends on the filesystem).

#### CR-35 — P3 — Memory — Answer files are read whole after a `Stat`-based size check
- **Location:** `internal/services/topics/select.go:40-66`.
- **Why:** the cap is checked with `Stat`; then `root.ReadFile` reads the whole file and the size is re-checked afterwards (correctness holds). A file that grows between the two calls, such as a huge paste or a binary, is fully loaded into memory.
- **Fix:** `io.ReadAll(io.LimitReader(f, max+1))`.
- **Test:** a file that grows after `Stat` (via a test hook) never allocates more than max+1 bytes.
- **Confidence:** Medium (POTENTIAL).

#### CR-36 — P3 — Backup — An interrupted `VACUUM INTO` leaves a 0-byte backup that blocks that day's backup and pushes a good one out of the rotation
- **Location:** `internal/services/maintenance/maintenance.go:90-119`; triggered from `cmd/hub/main.go:325-344` (startup maintenance runs on the signal ctx).
- **Scenario (reviewer-reproduced):** Ctrl+C during the startup backup leaves `app-YYYY-MM-DD.db` at 0 bytes. Later runs that day skip the backup because `Exists`. The rotation counts the empty file and deletes a real one.
- **Fix:** `VACUUM INTO …db.tmp`, rename on success, and remove the tmp file on failure; ignore 0-byte files in the existence and rotation checks.
- **Test:** `Run` with a cancelled ctx leaves no `app-*.db`; a later run creates a non-empty backup.
- **Confidence:** High.

#### CR-37 — P3 — Availability — If `Recover` fails at startup, the runner stops for good while the UI keeps accepting jobs
- **Location:** `internal/jobs/runner.go:214-219`; `cmd/hub/main.go:346-351` (only logs).
- **Scenario:** an external process (a DB browser, or `hub migrate`, see CR-39) holds the write lock for more than 5 s at startup. Recover gets `SQLITE_BUSY`. Every job enqueued in that session stays `queued` forever, and that scope's buttons stay disabled.
- **Fix:** retry `Recover` with backoff inside `Run`, or make the failure fatal for `serve`.
- **Test:** hold `BEGIN IMMEDIATE` on a second connection, start `Run`, then release; queued jobs eventually run.
- **Confidence:** Medium.

#### CR-38 — P3 — Reliability — A failed final status write leaves a job `running` until restart
- **Location:** `internal/jobs/runner.go:321-325` (the error is only logged).
- **Impact:** the scope stays busy; Cancel sets a flag nobody reads; Retry is refused. After restart, `Recover` marks a job whose commit succeeded as `failed/interrupted`, and a Retry then creates a duplicate version.
- **Fix:** retry the final `UPDATE` with backoff on the `WithoutCancel` ctx; if it still fails, record the failure in memory so the UI can show it.
- **Test:** inject a failing writer for the first 2 attempts; the status still lands.
- **Confidence:** Medium (POTENTIAL; needs BUSY > 5 s or disk errors).

#### CR-39 — P3 — Migrations — `hub migrate` takes no instance lock, and a database newer than the binary is accepted
- **Location:** `cmd/hub/main.go:131-160` (no flock, unlike `rebuild` at `:183-188`); `internal/store/db.go:143-145` (`if current >= target { return nil }`).
- **Scenario:**
  - (a) A newer `hub migrate` runs a table-rebuild migration under a running older server, which can lead to BUSY and then CR-37.
  - (b) An older `hub.exe` opens a database migrated by a newer one and runs against an unknown schema.
- **Fix:** take `.app/hub.lock` in `runMigrate`; return an error when `current > len(migrations)`.
- **Test:** set `user_version=99` → `store.Open` fails; `migrate` while a lock is held → refuses.
- **Confidence:** High (code), Low (frequency).

#### CR-40 — P3 — Configuration — Validation gaps: integer durations, zero timeouts, retention ≤ 0, a missing `--config` file, unknown keys
- **Location:** `internal/config/config.go:55-63,143-222`.
- **Current / Why:**
  - BurntSushi/toml decodes an integer into `time.Duration` as nanoseconds, so `timeout_lesson = 600` means 600 ns and every lesson times out.
  - `stall_timeout = 0` silently disables the watchdog (`ai/watch.go:14`).
  - `log_retention_days <= 0` makes maintenance delete all job logs and failed jobs' work/staging (their retry checkpoints) every hour.
  - `Load` ignores a `--config` path that does not exist (`:147-155`), so a typo silently runs with defaults.
  - Unknown or misspelled keys are ignored (no `MetaData.Undecoded()` check).
- **Fix:** reject timeouts under 1 s and retention < 1 day; error when an explicit `--config` file is missing; reject undecoded keys.
- **Test:** table tests for each case in `config_test.go`.
- **Confidence:** High.

#### CR-41 — P3 — Configuration/Docs — The default config location in the design is never used (`DefaultConfigPath` is dead code)
- **Location:** `internal/config/config.go:130-141` (no callers); `technical-design-document.md:95,1307,1518` say the config lives in `%APPDATA%\LearningHub\config.toml`, outside the learning directory (for R9 secrets separation).
- **Current:** without `--config`, the current working directory is the base dir and defaults apply; `INSTALL.md` documents this actual behaviour.
- **Fix:** either load `DefaultConfigPath()` when `--config` is absent and the file exists, or delete the function and update the TDD.
- **Test:** with `APPDATA` pointing to a temp dir containing `config.toml`, `Load("")` applies it (if that option is chosen).
- **Confidence:** High.

#### CR-42 — P3 — Concurrency — The adapter switch has a check-then-act race with job enqueue
- **Location:** `internal/httpserver/handlers_settings.go:128-145` (`SELECT COUNT(*) … status IN ('queued','running')` … `Switch.Set(name)`); contract comment in `internal/ai/switch.go:21`.
- **Scenario:** a job is enqueued from another tab between the count and `Set`, and then runs with a mixed adapter name, model and `Run`.
- **Fix:** perform the busy check and `Set` under the runner's enqueue serialisation, or have each job capture the adapter (name, model, runner) at enqueue/claim time.
- **Test:** concurrent enqueue and switch under `-race` with a barrier; a job never records adapter A while it runs B.
- **Confidence:** High (code); the trigger needs two tabs.

#### CR-43 — P3 — CLI/UX — `hub doctor` checks the configured adapter rather than the one selected in the UI; the model warning always names `agy`; doctor writes outside `fsx`
- **Location:** `cmd/hub/main.go:93-102` (`newAdapter(cfg, nil, cfg.Adapter.Kind)`, `Model: cfg.Adapter.Model`, `os.WriteFile(<base>/.doctor_test)`); `internal/httpserver/handlers_settings.go:55` (message hard-codes `agy models`/`adapter.model`).
- **Impact:**
  - `doctor` can report "ready" for agy while the UI uses Claude (acknowledged in `PROGRESS.md:210`).
  - The settings warning is wrong for Claude and Codex.
  - The `.doctor_test` probe bypasses the `fsx` rule and would overwrite a user file with that name.
- **Fix:** read `meta.adapter_kind` via `SavedAdapter` (opening the DB read-only); use `Adapter.Name()` and the matching config section in the message; use `fsx.WriteNew` with a random name.
- **Test:** a doctor test with `meta.adapter_kind='claude'` probes Claude; a message test for each adapter.
- **Confidence:** High.

#### CR-44 — P3 — CLI — `hub serve` exits with status 0 on fatal startup errors
- **Location:** `cmd/hub/main.go:281-284`, `:286-289`, `:293-295`, `:360-362` (`return` instead of `os.Exit(1)`).
- **Impact:** scripts and service managers treat failing to open the base dir, failing to install skills, or failing to bind the port (e.g. 8787 already in use) as success.
- **Fix:** have `runServe` return an error; `main` sets exit code 1 after the deferred cleanup.
- **Test:** a subprocess test: an occupied port gives exit status ≠ 0.
- **Confidence:** High.

#### CR-45 — P3 — Frontend — A Back navigation past htmx's history cache shows a bare fragment on `/topics`
- **Location:** `internal/ui/templates/topics.html:7` (`hx-push-url="true"`); `internal/httpserver/handlers_topics.go:39-42` (renders the fragment whenever `HX-Request == "true"`).
- **Why:** on a history-cache miss, htmx 2.0.4 sends `HX-Request: true` plus `HX-History-Restore-Request: true` and swaps the response into `<body>`.
- **Scenario:** more than 10 search steps, then Back repeatedly; or `localStorage` unavailable. The page loses its header, nav and search form.
- **Fix:** render the fragment only when `HX-History-Restore-Request != "true"`, or set `"refreshOnHistoryMiss": true`.
- **Test:** `GET /topics?q=x` with both headers returns the full layout.
- **Confidence:** High.

#### CR-46 — P3 — Frontend/A11y — The draft form posts on text-input `change`, losing focus and racing the merge button
- **Location:** `internal/ui/templates/draft.html:20-21` (`hx-trigger="change" hx-swap="outerHTML"`), `:41` (`ch_title` inputs without `id`), `:43` (`merge` submit buttons).
- **Scenario:**
  - Tabbing between chapter titles POSTs and replaces the form each time, so focus and in-flight typing are lost.
  - Editing a title and then clicking «ادغام با فصل بعد» sends two POSTs; if the blur POST commits last, the merge is undone.
- **Fix:** trigger htmx only for the `select` and checkboxes (a delegated listener in `app.js` calling `htmx.trigger(form,"selection-change")`), or give the inputs stable ids.
- **Test:** a browser test: tab between titles keeps focus and value; merge after a title edit gives N−1 chapters.
- **Confidence:** High (focus loss); Medium (race).

#### CR-47 — P3 — Frontend — The copy-path button label can stay "کپی شد" after a double click
- **Location:** `internal/ui/static/app.js:5,14`.
- **Fix:** `btn.dataset.label ??= btn.textContent;` and restore from `dataset.label`.
- **Test:** a JS unit test or browser test with two clicks within 1.5 s.
- **Confidence:** High.

#### CR-48 — P3 — Hardening — No `Cross-Origin-Resource-Policy` header, and `GET /settings` launches the AI probe
- **Location:** `internal/httpserver/middleware.go:57-62` (header set); `internal/httpserver/handlers_settings.go:62-73,95`.
- **Why:**
  - Any website can embed `<img src="http://127.0.0.1:8787/files/N">`. The Host check passes, and ORB does not protect `application/octet-stream`, so the bytes reach the attacker's renderer process (a Spectre-class read) and an existence oracle is available.
  - Likewise, a cross-site `GET /settings` spawns the AI CLI probe (up to ~90 s, once per process).
- **Fix:** set `Cross-Origin-Resource-Policy: same-origin` globally. Refuse `Sec-Fetch-Site: cross-site` for non-navigation GETs. Probe only on `POST /settings/doctor` or on same-origin views.
- **Test:** a middleware test asserting CORP; a cross-site `GET /settings` does not call `Probe`.
- **Confidence:** Medium.

#### CR-49 — P3 — Frontend — A manual `?tab=` on `/topics/{id}` renders the wrong empty state
- **Location:** `internal/httpserver/handlers_panel.go:59` (`a.panelData(r, id, tabOf(r), nil)`).
- **Scenario:** `/topics/1?tab=reports` renders the reports tab without loading the set, so it shows «هنوز مجموعه تمرینی…» even when data exists.
- **Fix:** pass `""` in `showTopic`.
- **Test:** `GET /topics/1?tab=reports` renders the lesson tab.
- **Confidence:** High (manual URLs only).

#### CR-50 — P3 — Recovery — Rebuild trusts `report_file` paths from `result.json` and aborts on some inconsistencies
- **Location:** `internal/services/rebuild/rebuild.go:574-581`; `internal/schema/result.schema.json:63` (no pattern on `report_file`).
- **Why:**
  - `report_file: "../../../01-x/lesson/lesson.md"` registers another topic's file as an immutable report, or triggers a later `UNIQUE rel_path` abort.
  - An exercise missing from the manifest, or a folder name that fails `ValidateRelPath`, aborts the whole rebuild instead of warning (same root cause as CR-04).
- **Fix:** require `report_file == path.Base(report_file)` and `fsx.ValidateSegment`; turn these cases into warn-and-skip.
- **Test:** a `result.json` with `report_file: "../x.md"` produces a warning, not a failure.
- **Confidence:** Medium.

#### CR-51 — P3 — Correctness — Stable-key normalisation does not fold invisible format characters
- **Location:** `internal/roadmap/stablekey.go:15-34`; `internal/roadmap/parse.go:160`.
- **Why:** only ZWNJ is folded. U+200B, U+200D, U+200E/U+200F and an embedded U+FEFF survive, so `"Go‏Lang"` and `"GoLang"` get different keys. Persian text pasted from Word or the web often carries RLM/LRM.
- **Impact:** re-importing the same roadmap from another editor archives topics and creates new folders (progress is detached, not lost). Invisible-only titles also pass `ValidateChapters`.
- **Fix:** drop `unicode.Cf` characters other than ZWNJ in `Normalize`. This changes the keys of affected existing titles, so match on old-or-new keys during the next re-import.
- **Test:** `Normalize("Go‏Lang") == Normalize("GoLang")`; a chapter title of only U+200B is rejected.
- **Confidence:** Medium.

#### CR-52 — P3 — Sync — Scan writes stale observations, ApplyMoves does not re-verify hashes, and empty files can be matched as moves
- **Location:** `internal/services/syncsvc/syncsvc.go:133-144` (unconditional `UPDATE … WHERE id=?` with values from before the transaction), `:381-390` (only checks that the target exists); content-hash matching includes the empty-content hash.
- **Impact:** a concurrent Confirm or lesson write can have its `observed_sha256` overwritten until the next scan. A moved-file proposal can be applied after the file changed. An emptied and deleted answer can be "moved" onto an unrelated empty file (the user still confirms).
- **Fix:** guard the updates with `AND rel_path=? AND sha256=?`; recompute the match in ApplyMoves; exclude the empty hash from matching.
- **Test:** change a target file between Scan and Apply; Apply refuses it.
- **Confidence:** Medium (POTENTIAL).

#### CR-53 — P3 — Consistency — One attempt mixes two timestamp formats (earlier E6, still open)
- **Location:** `internal/services/evaluation/commit.go:68` (`started_at` from SQLite, milliseconds) vs `:71` (`time.RFC3339`, seconds); both go into `result.json` and `attempts`.
- **Fix:** use one formatter, e.g. `2006-01-02T15:04:05.000Z07:00` in UTC, matching `nowSQL`.
- **Test:** `result.json` timestamps match one regex.
- **Confidence:** High.

#### CR-54 — P3 — Test reliability — The jobs cancel tests are flaky
- **Location:** `internal/jobs/cancel_test.go:74-85,163-172`, against `internal/jobs/runner.go:321-330` (status is written before the work/staging cleanup).
- **Evidence:** 1 failure in about 30 stressed runs: `cancel_test.go:171: succeeded job must clean work/ and staging/`. The plain `go test -race ./...` run in this review passed.
- **Fix (test only):** poll for the directories to disappear, with a deadline.
- **Test:** `go test -race -count=200 ./internal/jobs` is green.
- **Confidence:** High.

#### CR-55 — P3 — Code quality — gofmt drift and dead code in `fsx`
- **Location:**
  - `gofmt -l` lists `internal/fsx/fsx_test.go`, `internal/fsx/root.go:13-16`, `internal/fsx/safename.go:15-21`.
  - `safename.go:47` (`validSegmentRegex` is unused), `:60-63` (a no-op `if`).
  - The Windows reserved list (`:25-31`) lacks `CONIN$`, `CONOUT$`, `COM0`/`LPT0` and the superscript digit variants.
- **Fix:** `gofmt -w`; delete the dead code; complete the reserved list. Add `gofmt -l` (and `staticcheck`) to `make test`.
- **Test:** CI fails if `gofmt -l` produces output; table test for the reserved names.
- **Confidence:** High.

#### CR-56 — P3 — Release/Packaging — `dist/` binaries and `dist/INSTALL.md` do not correspond to the current source
- **Location:** `dist/hub`, `dist/hub.exe`, `dist/INSTALL.md` (gitignored local artifacts).
- **Evidence:**
  - `go version -m dist/hub` → `mod learninghub v0.0.0-20261107154052-ceae190d6ef1+dirty`, i.e. T6.8 (`ceae190`) with uncommitted changes, while HEAD is `f638844` (T7.11).
  - `diff INSTALL.md dist/INSTALL.md` shows that `dist/INSTALL.md` lacks the stall-timeout hint and the T7.11 AI-tool selection section.
  - The binaries lack all Phase 7 features: chapters, the design system, the Claude/Codex adapters.
- **Fix:** rebuild with `make release` from a clean tree; embed the VCS revision in `hub version` (`debug.ReadBuildInfo`) and refuse a `+dirty` release build.
- **Test:** a release-script check that `go version -m` shows no `+dirty` and the revision equals HEAD.
- **Confidence:** High.

#### CR-57 — P3 — Documentation — Outdated or contradictory documents
- **Location / evidence:**
  - `AGENTS.md` = `GEMINI.md` (byte-identical): titled "Antigravity Agent Rules". The continuity protocol does not point readers to the open review reports (`review-report.md`'s process finding P1, still unaddressed).
  - `implementation-strategy-report.md:7-40`: describes 6 phases and `agy` as the only AI tool (there are now 7 phases and 3 tools).
  - `skills/answer-evaluator/SKILL.md:7,20`: names `inputs/answer`, while the composer writes `inputs/answer<ext>` (earlier C7).
  - `technical-design-document.md`: config location (see CR-41); `WriteNew` tmp+link (CR-07); journal recovery (CR-03) is described as implemented behaviour.
  - `PROGRESS.md:11-15`: "Phase 7 complete" and "100% green", while the T5.8 real run, the Windows checklist and `make vuln` are still open.
  - There is no operational documentation for recovering from the CR-03 / CR-05 wedges (which files to delete).
- **Fix:** update these documents. Add a "Known open findings" pointer to `CODE_REVIEW_TODO.md` in `AGENTS.md` and `PROGRESS.md`.
- **Test:** none (documentation).
- **Confidence:** High.

#### CR-58 — P3 — Testing — Untested critical paths
- **Evidence:**
  - Packages without tests: `cmd/hub` (0 %), `internal/services/status`, `internal/ai/fake`.
  - Low coverage: `internal/config` 44.9 %, `internal/ai` 53.2 %, `internal/services/topics` 54.5 %.
  - No test covers a crash or partial commit, accept-suggestion → exercises (CR-01), a background child holding stdout (CR-02), odd file names in sync or rebuild (CR-04, CR-50), large or pathological uploads (CR-15–CR-17), or SIGHUP.
  - No Windows runtime tests (only `GOOS=windows go vet`/build), and no CI configuration in the repository.
- **Fix:** see Phase 5 of the action plan.
- **Confidence:** High.

---

## 5. Security Audit

**Threat model used.** A single local user. The attackers considered are:
- a hostile web page in the user's browser (CSRF, DNS rebinding, cross-origin reads);
- hostile content inside an imported book or roadmap, or inside AI output (prompt injection, XSS, path injection);
- a compromised or misbehaving AI CLI child process.

There is no authentication, by design: the server binds to loopback only, and `config.go:172` refuses any other listen address.

| Area | Status | Evidence / findings |
|---|---|---|
| CSRF | **Strong** | 256-bit `crypto/rand` token, `subtle.ConstantTimeCompare`, required on all non-safe methods; `Sec-Fetch-Site` check; `Origin: null` refused; `Referrer-Policy: same-origin` chosen so that `Origin` is still sent (`middleware.go`). One weakness is usability: a stale token after a restart fails silently (CR-20). |
| DNS rebinding | **Strong** | Host allowlist (`127.0.0.1`, `localhost`, `::1`; `middleware.go:47-54`). CR-I1 notes that the port is ignored, which is harmless. |
| XSS | **Strong** | `html/template` everywhere; `template.HTML` only for `render.Markdown`; goldmark without `WithUnsafe` plus a bluemonday allowlist, image removal and BiDi-control stripping; a 28-payload XSS corpus test. Second-layer dependency: CR-21. |
| CSP / headers | **Strong** | `default-src 'self'; script-src 'self'; style-src 'self'; … frame-ancestors 'none'`; no inline scripts, `on*` handlers, `hx-on` or `style=` (grep-verified); htmx `allowEval:false`; `nosniff`; COOP. Missing: CORP (CR-48). |
| Request limits | Good | Body cap and 413 before form parsing; upload ≤ 1 MB. CR-I2: on the header-CSRF path an oversized body becomes an empty form rather than a 413 (not reachable today). |
| Path traversal | **Strong** | Every path goes through `fsx.ValidateRelPath` and `os.Root`; `/files/{id}` serves DB paths only; `?v=`/`?attempt=` select from DB rows. Exceptions: in-root symlinks (CR-10); trust in `report_file` during rebuild (CR-50). |
| Overwrite protection | Good | `WriteNew` uses `O_EXCL`; undo lists contain only files the job created. Crash windows: CR-03, CR-07. |
| Process execution | Good | No shell; model names starting with `-` refused; effort value whitelisted; per-dialect forbidden-flag lists; `dangerously*` refused in config and adapters; Codex prompt after `--`; `exec.LookPath` refuses binaries found via `.` (`ErrDot`). Gaps: Windows `.cmd` shims (CR-13); AI read scope and project settings (CR-11). |
| Secrets in logs | Good | Masking of prompt, events, stderr, response and args (`stream.go:101-118`, tested). CR-I9: children inherit the full environment, which is required for CLI auth. |
| SQL injection | **None found** | Every query is parameterised; search uses `instr()`, so there are no LIKE-wildcard issues. |
| Rubric confidentiality | Good | Rubric file locked in SQL until every exercise is evaluated (tested). CR-I5: each report embeds that exercise's own criteria by design. |
| Dependency CVEs | Needs confirmation | CR-21; govulncheck could not be run. |

**Overall risk:** Low for the intended single-user loopback deployment. The two residual items worth acting on first are CR-11 (AI read scope) and CR-10 (symlink-redirected deletion).

## 6. Concurrency and Process Management

**Job runner (`internal/jobs`) — correct.**
- The dispatcher wakes on a 1-slot channel after enqueue commits, with a 2 s ticker as fallback, so no wakeup is missed.
- Claiming is a CAS (`WHERE id=? AND status='queued'`).
- `Cancel` sets `cancel_requested` inside `BEGIN IMMEDIATE`, and `execute` registers its cancel function before re-reading the flag, so a cancel is never lost.
- The commit gate is one atomic `UPDATE … WHERE cancel_requested=0`, after which `fn` runs under `WithoutCancel`.
- Every handler ends with `return j.Commit(...)`.
- The semaphore is released on every path. Panics in handlers are recovered (`runner.go:343-354`).
- `go test -race` was clean in this run and in ~40 stress runs by a reviewer.
- Open items: Recover failure (CR-37), final-write failure (CR-38), the step-goroutine panic (CR-33), and test flakiness (CR-54).

**AI process lifecycle — the weakest area.**

| Concern | Linux/macOS | Windows |
|---|---|---|
| Tree kill on cancel/timeout | `Setpgid` + SIGTERM → SIGKILL after 3 s (CR-22: no grace for grandchildren; a stray timer) | Job Object `KILL_ON_JOB_CLOSE` + `TerminateJobObject`; attach errors ignored (CR-24); documented CreateProcess→Assign race window |
| Hub crash (SIGKILL) | Group survives (documented `shortcut:`) | Job handle closes, so the tree dies |
| Terminal close | SIGHUP not handled → children survive (CR-23) | CTRL_CLOSE_EVENT is mapped to SIGTERM, but Windows allows only ~5 s (CR-I14) |
| Stdout held by a helper | Result discarded or run hangs (CR-02) | Same mechanism (pipe inheritance) |
| Probe | No `WaitDelay` (CR-12) | Same |
| Zombies | `cmd.Wait` always called; the editor is reaped in a goroutine | n/a |

**Stall watchdog (`ai/watch.go`):** correct. The forwarder exits on `close(in)`, so no goroutine leaks, and services pass `events=nil`, so a slow consumer cannot cause false stalls. Disabled silently by `stall_timeout=0` (CR-40).

**Retry (`ai/retry.go`):** exactly one retry, and only for `invalid_output`. It does not check ctx before retrying (CR-26).

**Shutdown ordering (`cmd/hub/main.go:345-371`):** correct. HTTP `Shutdown` (5 s) runs, then the deferred `cancel()`/`<-runnerDone`, then `root.Close`, `db.Close`, unlock. CR-I8: the maintenance and heartbeat goroutines are not joined (harmless apart from CR-36).

## 7. Database and Data Integrity

- **Connection model:** one pooled connection, with pragmas in the DSN (so they apply to every new connection): `foreign_keys(1)`, `busy_timeout(5000)`, `journal_mode(WAL)`, `synchronous(NORMAL)`, `_txlock=immediate`. `WithTx` adds a mutex and rolls back and re-panics on panic. No deadlock pattern was found: every multi-row read closes its rows before writing.
- **Migrations:** embedded, ordered by number, one transaction per file, `foreign_key_check` before commit, `user_version` set in the same transaction, rollback tested, and the 0003 table rebuild preserves ids and references (`migrate_test.go`). Gaps: no instance lock for `migrate`, and downgrades are accepted (CR-39). The deferred `PRAGMA foreign_keys = ON` error is ignored (CR-I7).
- **Uniqueness and invariants:** partial unique indexes `uq_lesson_current`, `uq_set_current`, `uq_jobs_active_scope`; `UNIQUE(set_id, attempt_no)`; `UNIQUE(attempt_id, exercise_id)`; `UNIQUE(roadmap_id, stable_key)`; `UNIQUE(dir_name)`. The `dir_name` numbering invariant is broken by CR-06.
- **Atomicity of file + DB commits.** The pattern is: files first (`WriteNew`), then one transaction, then compensating deletes. This is correct for application errors (tested with triggers that fail mid-transaction). It is **not** crash-safe:
  - the journal is never replayed (CR-03);
  - roadmap Confirm has no journal (CR-05);
  - protected files are written in place (CR-07);
  - directory entries are not fsynced, and `synchronous(NORMAL)` can lose the last transaction on power loss (CR-34);
  - a panic skips undo (CR-33).

  In every case the effect is a stuck operation or orphan files; **no case was found that overwrites or deletes user data** (apart from CR-10).
- **Trust in on-disk data:** the evaluation commit trusts `manifest.json` (CR-08); rebuild trusts `report_file` (CR-50); rebuild validates manifest and result schemas.
- **Indexes:** adequate for the single-user scale. CR-I6: `Runner.Latest` cannot use the partial index; `claim` orders by a wall-clock string.
- **Backups:** `VACUUM INTO` gives a consistent WAL snapshot and never overwrites. Interruption leaves a 0-byte file (CR-36).
- **Partial-update scenarios found:** CR-03, CR-05, CR-07, CR-33, CR-34, CR-38, plus CR-I12 (the analyze commit does two `UPDATE`s outside a transaction).

## 8. AI Integration

- **Adapter layer:** one `Adapter` interface with three dialects (agy, `claude -p --output-format stream-json`, `codex exec --json`) behind `ai.Switch`. Arguments are built without a shell, with forbidden-flag lists per dialect (`dialects.go:44-52`). The Codex event shapes are untested against the real CLI (a documented `shortcut:` at `dialects.go:204`).
- **Prompt composition:** a whitelisted sandbox (`AGENTS.md`, `SKILL.md`, `profile.md`, `inputs/{task.json, roadmap_excerpt.md, lesson.md, source.md, outline.json, question.md, rubric.json, answer<ext>}`), built from byte copies only. The work directory is wiped before every run. A per-step sandbox for parallel evaluation fixes earlier finding C3. The security preamble goes both into `-p` and after the `SKILL.md` frontmatter. 4 KB prompt cap; CR-I10: the retry note is appended after the check, and the boundary differs from the test.
- **Output parsing:** the NDJSON reader uses `bufio.Reader.ReadBytes`, so there is no 64 KB Scanner limit (but see CR-27 for unbounded lines). Non-JSON lines are ignored. agy's `result.structured_output` is preferred, with a fallback to a pure-JSON `response`.
- **Validation chain (no bypass found):** JSON Schema (`additionalProperties:false` everywhere) → service checks:
  - `lessons.Validate`;
  - `exercises.Build`, which discards AI ids, assigns `qNN`/`cN`, recomputes file names, then re-validates manifest and rubric plus all cross-checks and re-parses the headings;
  - `evaluation.Check`: the exercise id, each criterion exactly once, `awarded ≤ max_points`; the official score is computed by the app and the AI score is stored only as `ai_reported_score`;
  - `roadmap.ValidateChapters`: range, order, overlap, nesting, titles.

  Weak points: AI schemas looser than the downstream ones (CR-30), on-disk manifest trust (CR-08), the key format bug (CR-01).
- **Error classification:** typed errors map to `error_code`; Persian messages via `ai.Message`. Substring matching is too broad (CR-29); probe timeouts are misreported (CR-12); a cancelled-before-start run is misclassified (CR-26).
- **Timeouts, stall, cancellation:** generally correct; process-lifecycle gaps in §6.
- **Unicode/Persian:** NFC normalisation, Arabic ی/ک folding, ZWNJ preserved; BiDi controls stripped before rendering; `<bdi>` wrapping in templates. Gaps: byte truncation (CR-28); invisible format characters in keys (CR-51).
- **Compatibility with CLI changes:** isolated in `dialects.go`, with fake-CLI contract tests for agy. Claude and Codex have only argument-builder and event-parser unit tests (codex not installed; PROGRESS.md:210). Risk: CR-11, CR-13.
- **Prompt injection (answers):** `guard.go` is a phrase list; it flags but never changes scores (CR-I13, accepted risk RK5).

## 9. HTTP and Frontend

- **Routing:** Go 1.22 method patterns enforce methods (405). `pathID` rejects non-numeric and ≤ 0 ids. All state-changing routes are POST.
- **Handlers:** thin; pages render into a buffer before writing, so a template error never sends half a page. Errors are logged with `slog` and shown as generic Persian messages, with no absolute paths or raw errors in responses.
- **Frontend/backend contract:** every form field name matches what its handler reads; every `hx-target` (`#job-area`, `#draft-ai`, `#topic-results`, `this`) exists; every POST carries the CSRF token (htmx via `hx-headers` on `<body>`).
- **Job card polling:** `every 2s` while the job is active, stopping on finish via `HX-Refresh`, so no polling loop is left behind.
- **Defects:**
  - silent htmx errors (CR-20);
  - history restore renders a bare fragment (CR-45);
  - the draft form's trigger (CR-46);
  - the copy button (CR-47);
  - CORP and the side effect on `GET /settings` (CR-48);
  - `?tab=` (CR-49);
  - the adapter-switch race (CR-42);
  - doctor messages (CR-43).
- **Server configuration:** `ReadHeaderTimeout` and `IdleTimeout` are set; there is no `Read/WriteTimeout` (CR-I3, acceptable on loopback; a `WriteTimeout` would cut off the ~90 s probe). `/static/` uses `no-store` and `serveFile` reads whole files into memory (CR-I4; negligible).
- **Accessibility:** RTL with `dir="auto"` on rendered blocks; labels present. CR-46 is the only issue found that affects function (focus loss).

## 10. Testing Assessment

**Commands executed (this review, Linux amd64, go1.26.4):**

| Command | Result |
|---|---|
| `go test ./...` | **PASS** (all packages; `cmd/hub`, `ai/fake`, `domain`, `services/status`, `ui` have no test files) |
| `go vet ./...` | **PASS** |
| `go test -race ./...` | **PASS** |
| `go test -cover ./...` | see table below |
| `GOOS=windows go vet ./...` | **PASS** |
| `GOOS=windows go build ./cmd/hub` | **PASS** |
| `go mod verify` | all modules verified |
| `go mod tidy -diff` | no diff |
| `gofmt -l .` | 3 files (CR-55) |
| Targeted probes with `go test -overlay` (scratch files only) | confirmed CR-01, CR-02, CR-04, CR-05, CR-06, CR-10, CR-12, CR-14, CR-15, CR-16, CR-17, CR-18, CR-36, CR-54 |
| `staticcheck`, `govulncheck`, `golangci-lint` | **not run**: not installed, and installing tools is out of scope. `make vuln` (`go run …govulncheck@v1.9.0`) needs network access to vuln.go.dev and was not run. |
| `make test` / `make release` | not run as such. `make test` = vet + test, both run above. `make release` would delete and rewrite `dist/` (a modification). |

**Coverage (statement coverage from `go test -cover`):**

| Package | % | Package | % |
|---|---|---|---|
| scoring | 100.0 | services/syncsvc | 81.7 |
| roadmap | 96.9 | services/exercises | 80.3 |
| render | 96.7 | jobs | 80.5 |
| skills | 90.0 | maintenance | 80.0 |
| services/evaluation | 84.8 | schema | 79.4 |
| ai/antigravity | 82.3 | httpserver | 79.3 |
| services/roadmaps | 82.2 | procgroup | 76.7 |
| services/lessons | 81.8 | rebuild | 75.5 |
| store | 73.5 | fsx | 70.2 |
| ai/prompt | 67.2 | topics | 54.5 |
| ai | 53.2 | config | 44.9 |
| cmd/hub, status, ai/fake | 0 | | |

**Quality of the existing tests: high.**
- Tests assert the actual invariants: file hashes before and after an operation, row counts, `errors.Is` sentinels.
- Several are mutation-checked: removing the commit gate, the undo or one sanitiser layer makes a test fail.
- Real fake-binary contract tests for agy, golden tests for the parser, an XSS corpus, `-race` runs in the runner tests, and an acceptance journey in `httpserver/acceptance_test.go`.
- Determinism: mostly good. CR-54 is a known flaky pair, and `procgroup` tests rely on 3 s timing.

**Gaps (detailed in CR-58):**
- crash and partial-commit recovery;
- the suggestion → exercises path;
- misbehaving child processes (stdout held, SIGHUP);
- hostile file names in sync and rebuild;
- pathological uploads (performance);
- config validation edge cases;
- the `cmd/hub` lifecycle and exit codes;
- Windows runtime behaviour (Job Objects, `.cmd` shims, console close);
- htmx error handling in a real browser.

**Tests that look covered but are not:** `skills_test` and the schema tests validate the *shape* of generated data, but no test generates exercises for a topic whose key did not come from `roadmap.StableKey`, which is why CR-01 went unnoticed. `TestEvaluateCommitCollisionNeverOverwrites` proves no overwrite happens, and so also documents the CR-03 wedge without flagging it as a defect.

## 11. Performance Assessment

Only issues with measurements or a defensible complexity argument are listed.
- **CR-15:** quadratic duplicate-title handling (measured 5.4 s for 10 k duplicates).
- **CR-16:** O(headings × size) slicing, run inside the writer transaction (measured ~5.7 min for 95 k headings).
- **CR-17:** O(N·M·L²) Levenshtein on every preview render (measured ~20 s for 3 k × 3 k).
- **CR-27:** unbounded stderr and line buffers.
- **CR-35:** full read of answer files.
- **Not problems at the intended scale:** the topic list (PROGRESS reports p95 ≈ 17 ms for 500 topics), per-render `Runner.Latest` (CR-I6), whole-file reads in `serveFile` (CR-I4), and `no-store` static assets.
- No unbounded goroutine creation was found: evaluation parallelism is capped at 3 (`config.go:219`) and `jobs.max_concurrent` is enforced by a semaphore.

## 12. Cross-Platform Compatibility

| Topic | Linux | Windows | Finding |
|---|---|---|---|
| Build / vet | ✅ | ✅ (cross-compiled) | — |
| CGO | none (`CGO_ENABLED=0` in Makefile; modernc sqlite) | same | — |
| Process trees | process groups (CR-22, CR-23) | Job Objects (CR-24); not run on Windows | PROGRESS.md:67 lists the manual Windows test as open |
| `.cmd` shims for claude/codex/editor | n/a | likely broken (CR-13) | — |
| Reserved file names | legal on Linux → sync/rebuild failures (CR-04, CR-50) | rejected by the OS | CR-55 (incomplete list) |
| Case-insensitive filesystem | n/a | roadmap slugs are checked on disk and in the DB; answer names are ASCII `qNN-slug.ext` | — |
| DB path characters | `#`/`%` (CR-14) | same, e.g. `C#` folders | — |
| Atomic replace | rename | Go 1.26 `Renameat` with `FILE_RENAME_REPLACE_IF_EXISTS` | — |
| Directory fsync | missing (CR-34) | no-op | — |
| Signals | SIGHUP missing (CR-23) | console close ~5 s window (CR-I14) | — |
| Config path | `~/.config/learninghub` never read (CR-41) | `%APPDATA%` never read (CR-41) | — |
| Log directory names | ns-resolution clock | coarse clock → collisions possible (CR-25) | — |
| Distributed binaries | stale (CR-56) | stale (CR-56) | — |

## 13. Dependency Assessment

| Module | Version | Purpose | Assessment |
|---|---|---|---|
| `modernc.org/sqlite` (+ libc, mathutil, memory) | v1.61.0 | CGO-free SQLite | Required by the no-CGO rule; large but justified. |
| `github.com/yuin/goldmark` | v1.7.13 | Markdown AST | Appropriate. |
| `github.com/microcosm-cc/bluemonday` (+ douceur, gorilla/css) | v1.0.27 | HTML sanitiser | Appropriate; pulls in `x/net`. |
| `golang.org/x/net` (indirect) | **v0.26.0** | `x/net/html` for bluemonday | **Outdated, with probable advisories (CR-21).** |
| `github.com/santhosh-tekuri/jsonschema/v6` | v6.0.3 | JSON Schema 2020-12 | Appropriate. |
| `github.com/BurntSushi/toml` | v1.6.0 | Config | Appropriate (watch integer → Duration, CR-40). |
| `github.com/gofrs/flock` | v0.13.1 | Single-instance lock | Appropriate, cross-platform. |
| `golang.org/x/sys` | v0.49.0 | Windows Job Objects | Required. |
| `golang.org/x/text` | v0.43.0 | NFC normalisation | Required. |
| `google/uuid`, `dustin/go-humanize`, `ncruces/go-strftime`, `remyoudompheng/bigfft`, `mattn/go-isatty` | indirect | modernc internals | Unavoidable transitive dependencies. |

- No unnecessary direct dependencies and no duplicate functionality. `go mod tidy -diff` is clean and `go mod verify` passes.
- Only CR-21 is a probable vulnerability. Every other package is "possibly older" without a known advisory affecting the code paths used, and is **not** reported as a vulnerability.
- Recommendation: run `make vuln` with network access and record the output in `PROGRESS.md`. It is still open there.

## 14. Positive Findings

These design decisions are correct and worth preserving:

1. **Strict separation of deterministic and non-deterministic work:**
   - the AI returns JSON only;
   - the software assigns all ids, file names and paths;
   - output is validated twice: by JSON Schema, then by Go cross-checks (`exercises/build.go:97-146`, `evaluation.go:359-397`, `roadmap/chapters.go`);
   - the official score is computed by the app, never taken from the AI.
2. **`fsx.Root` over `os.Root`** for every filesystem operation, plus `ValidateRelPath`/`ValidateSegment`; `WriteNew` with `O_EXCL`; undo lists that contain only paths created by this job.
3. **Job runner correctness:** atomic commit gate + `WithoutCancel`, CAS claiming, race-free cancel, the "late cancel" semantics, checkpointed parallel evaluation steps reused only under an identical input fingerprint.
4. **Security middleware:** CSRF + `Sec-Fetch-Site` + Origin + Host + strict CSP with no inline code, and htmx configured to keep the CSP intact.
5. **Two-layer Markdown sanitisation** with a BiDi-control strip and an XSS corpus test.
6. **Process execution without a shell**, with per-dialect forbidden-flag lists, `ErrDot`-safe lookup, and masked logs.
7. **Migrations:** transactional, FK-checked and tested, including the 0003 table rebuild.
8. **Parser determinism:** goldmark AST, golden tests, CRLF/BOM handling, stable keys from the normalised heading path.
9. **Lesson regeneration history:** copies the on-disk version (including user edits) to history before the atomic replace, and never overwrites a different history file.
10. **Test culture:** mutation-checked tests, fake-binary contract tests, `-race`-clean runner tests, acceptance journeys (note: `make test` itself runs only `vet` + `test`, without `-race`).
11. **The `shortcut:` comment convention** documents known limits in code (procgroup crash, codex events, injection guard).

## 15. Recommended Action Plan

**Phase 1 — Critical fixes (P1)**
- CR-01: valid stable keys for suggested topics, plus a regression test.
- CR-02: let `exec` own stdout (`cmd.Stdout = writer`) so `WaitDelay` bounds it; prefer a received SUCCESS result.
- CR-03: shared journal replay in `Runner.Recover` for lessons, exercises and evaluation.
- CR-04: skip or report unsupported file names in sync instead of aborting.

**Phase 2 — Security and reliability**
- Filesystem crash safety: CR-07 (tmp + link), CR-34 (directory fsync, tmp sweep), CR-05 (idempotent Confirm), CR-06 (numbering helper).
- Security: CR-10 (symlink checks + `RemoveAll` guard), CR-11 (AI read scope / setting sources), CR-13 (Windows `.cmd`), CR-21 (upgrade `x/net`, run govulncheck), CR-48 (CORP).
- Data integrity: CR-08 (verify the manifest/rubric at commit), CR-09 (needs-review label), CR-14 (DSN escaping), CR-18 / CR-19 / CR-50 (rebuild robustness).
- Process lifecycle: CR-12 (probe), CR-22 (procgroup timers), CR-23 (SIGHUP), CR-24 (Windows attach errors), CR-26 (cancel before start), CR-33 (panic undo / step recover).
- Runner/DB: CR-36 (backup tmp), CR-37 (Recover retry), CR-38 (final write retry), CR-39 (migrate lock / downgrade).
- UX reliability: CR-20 (htmx errors).

**Phase 3 — Architecture and maintainability**
- CR-42 (capture the adapter per job; atomic switch), CR-31 (fingerprint includes the adapter).
- CR-40, CR-41 (config validation and location), CR-43, CR-44 (CLI correctness).
- CR-30 (align the AI schemas, enforce the lesson cap), CR-32 (staged-output check).
- CR-51 (normalisation), CR-52 (sync guards), CR-53 (timestamps), CR-55 (gofmt, dead code).
- CR-56 (reproducible release), CR-57 (documentation).
- Optional: move shared helpers (`TopicDir`, numbering, answer selection) into one small package (§2.4).

**Phase 4 — Performance and optimisation**
- CR-15 (linear de-duplication), CR-16 (single-pass slicing, outside the transaction), CR-17 (bounded diff with caching).
- CR-27 (bounded buffers), CR-35 (bounded reads), CR-25 (unique log directories).

**Phase 5 — Test improvements**
- CR-58: crash-injection tests for every commit kind; suggestion → exercises; misbehaving-child fake CLIs; hostile file-name fixtures for sync and rebuild; pathological upload benchmarks; config table tests; `cmd/hub` subprocess tests (exit codes, SIGHUP).
- A Windows CI job (Job Object tree kill, `.cmd` shim, path tests).
- CR-54: fix the flaky tests.
- Add `gofmt -l`, `staticcheck` and `govulncheck` to `make test` / CI.
- Browser tests for the htmx error and history paths (CR-20, CR-45, CR-46, CR-47, CR-49).

## 16. Review Limitations

- **Platform:** only Linux amd64 was executed. All Windows behaviour (Job Objects, `.cmd` shims, console close, case-insensitivity, reserved names, the `C:\` form in the DSN) was assessed by reading the code and by `GOOS=windows go vet`/`build`.
- **Real AI CLIs:** `agy`, `claude` and `codex` were deliberately not invoked. CR-02, CR-11 and CR-12 were proven with fake binaries; whether the real tools leave stdout-holding helpers behind, and how their permission flags behave, is unverified. Codex is not installed (PROGRESS.md:210).
- **Crash scenarios:** simulated by placing orphan files or reading the code. No kill -9 or power-loss experiment was run.
- **Static and vulnerability tools:** `staticcheck`, `govulncheck` and `golangci-lint` are not installed; vuln.go.dev was not contacted. CR-21 rests on advisory knowledge, not a govulncheck call graph.
- **Real browser:** no browser was driven. The htmx findings are based on htmx 2.0.4 source semantics.
- **Files read only in part:** some long test files (`acceptance_test.go`, `analyze_test.go`, `chapters_test.go`, `sync_test.go`, `settings_test.go`, `list_test.go`, `select_test.go`, `suggestions_test.go`) were checked for helpers and security assertions rather than line by line. Golden JSON fixtures, `htmx.min.js` (version and specific code paths only), the font binaries, and `app.css` (grep only) were not read in full.
- **Existing user data:** no migration was run against any real `app.db`; only temporary databases were used.
- **`dist/`:** only metadata was inspected (`go version -m`, `diff`); the binaries were not executed.
- **Design document:** `technical-design-document.md` (1691 lines) was consulted by section for the behaviours under review, not audited end to end.

## 17. Final Assessment

Learning Hub is a carefully built, security-conscious local application. Its core invariants hold under normal operation and are backed by meaningful tests:
- the AI never writes directly;
- user answers and reports are never overwritten;
- all filesystem access is confined to the base directory;
- the web UI is locked to loopback with CSRF and CSP.

**It is not yet ready for unattended everyday use, for four reasons:**
1. A main feature is broken: suggested topics cannot get exercises (CR-01).
2. The unhappy paths are not recovered. A crash or kill during a commit wedges a topic or roadmap until the user deletes files by hand (CR-03, CR-05, CR-07), and a misbehaving AI CLI child can discard results or block shutdown (CR-02).
3. A single unusual file name disables sync (CR-04).
4. Windows, the primary target per `INSTALL.md`, has never been exercised at runtime (CR-13, CR-24, and the open manual checklist), and the distributed binaries are stale (CR-56).

After Phase 1, and the crash-safety and process-lifecycle parts of Phase 2, the application should be dependable for its intended single-user, local deployment. Nothing found requires an architectural redesign; every issue can be fixed locally within the existing package structure.

---

### Appendix A — Informational Observations (no confirmed defect)

| ID | Observation | Location |
|---|---|---|
| CR-I1 | The Host check ignores the port; Origin also accepts `http://127.0.0.1` and `http://localhost` without a port. Harmless (rebinding is still blocked; CSRF and Sec-Fetch-Site still apply). | `internal/httpserver/middleware.go:51,92-93` |
| CR-I2 | On POSTs carrying `X-CSRF-Token`, the body is not pre-parsed, so a `MaxBytesError` is swallowed by `FormValue` and the handler sees an empty form instead of a 413. Not reachable with the current tiny htmx forms. | `middleware.go:112-123` |
| CR-I3 | No `ReadTimeout`/`WriteTimeout` (only ReadHeader/Idle). Acceptable on loopback; a `WriteTimeout` would cut off the ~90 s probe. | `internal/httpserver/server.go:68-73` |
| CR-I4 | `serveFile` reads whole files; `/static/` is served `no-store`. Negligible on loopback. | `handlers_exercises.go:232`; `middleware.go` |
| CR-I5 | Each evaluation report embeds that exercise's rubric criteria while `rubric-vN.json` stays locked. This matches the design; state it explicitly in the TDD. | `internal/services/evaluation/report.go:96-98` |
| CR-I6 | `Runner.Latest` cannot use the partial `uq_jobs_active_scope` index; `claim` orders by the wall-clock `enqueued_at` (`ORDER BY id` would be strictly FIFO). | `internal/jobs/view.go:61`; `runner.go:263` |
| CR-I7 | The error from the deferred `PRAGMA foreign_keys = ON` after migration is ignored; if it ever failed, the single pooled connection would run without FK enforcement. | `internal/store/db.go:150` |
| CR-I8 | The maintenance goroutine and per-job heartbeats are not joined before `db.Close`. Harmless apart from CR-36. | `cmd/hub/main.go:325-344`; `runner.go:299` |
| CR-I9 | AI children inherit the full environment (needed for CLI auth). Variables such as `ANTHROPIC_API_KEY` or `CLAUDECODE` in the launching shell change Claude's behaviour; document this. | `internal/ai/antigravity/adapter.go:176` |
| CR-I10 | The retry note is appended after the 4 KB prompt check; `composer.go:82` uses `>` while its test asserts `>=` (earlier C9). `Compose` accepts an empty schema, so the adapter skips `--json-schema`; all services pass one today (earlier C5). | `internal/ai/retry.go:10-11,31-32`; `internal/ai/prompt/composer.go:82` |
| CR-I11 | `ExtractAnswerContent` takes the first answer marker; an AI-supplied title containing the marker text on header line 2 would shift it. Practically harmless. | `internal/fsx/answertpl.go:78-92`; `exercises/build.go:123` |
| CR-I12 | The analyze job's commit does two separate `UPDATE`s outside a transaction; a lone `\r` (classic Mac line endings) parses as one heading; a stale `source_start/end_line` can remain after a rule change (display only). | `internal/services/roadmaps/analyze.go:172-184` |
| CR-I13 | The answer-injection guard is a phrase list (documented `shortcut:`); it flags but never changes scores (accepted risk RK5, earlier E4). | `internal/services/evaluation/guard.go` |
| CR-I14 | On Windows, closing the console gives about 5 s for shutdown; the Job Object kills children anyway, but in-flight jobs may not be recorded as interrupted until the next start (Recover handles them). | `cmd/hub/main.go:266` |

### Appendix B — Status of Earlier Review Findings (verified against `f638844`)

| Earlier ID | Status | Now |
|---|---|---|
| A1 (stdout held → timeout/hang) | Still open | CR-02 |
| C1 (symlinked `.app/work` deletes user folder) | Still open (narrower scope than first reported) | CR-10 |
| C2 (`RemoveAll` unrestricted) | Still open (current callers are safe) | CR-10 |
| C3 (one sandbox per job) | **Fixed** (`evaluation.go:316` sets `Step`; `composer.go:88-93`) | — |
| C5 (empty schema accepted) | Open by design; not a bypass | CR-I10 |
| C7 (`inputs/answer` in SKILL.md) | Mostly mitigated; documentation drift remains | CR-57 |
| C9 (4096-byte boundary) | Still open (trivial) | CR-I10 |
| G1 / G3 (no SIGTERM grace; stray SIGKILL timer) | Still open | CR-22 |
| G2 (SIGHUP) | Still open | CR-23 |
| G6 (Windows attach errors ignored) | Still open | CR-24 |
| G7 (Windows Kill after TerminateJobObject) | Benign | — |
| E1 (journal never replayed) | Still open | CR-03 |
| E2 (needs-review shown as unanswered) | Still open | CR-09 |
| E4 (phrase-list guard) | Accepted risk | CR-I13 |
| E6 (timestamp formats) | Still open | CR-53 |
| Process P1 (review reports not linked from continuity docs) | Still open | CR-57 |
