# Code Review Remediation Backlog

This backlog comes from `CODE_REVIEW_REPORT.md`, reviewed at commit `f638844` on 2026-11-08. Each task has the same ID as its report finding; the report has the full evidence.

- **Status legend:** `- [ ]` pending, `- [x]` completed. No task has been started.
- **Order:** work by priority (P1 → P3). Respect the **Depends on** lines.
- **Definition of done for every task:** the listed tests are added and pass; `go vet ./...`, `go test -race ./...` and `gofmt -l .` are clean; `PROGRESS.md` is updated per the continuity protocol.

There are **no P0 tasks**.

## Summary

| Priority | Tasks |
|---|---|
| P1 | CR-01, CR-02, CR-03, CR-04 |
| P2 | CR-05 … CR-21 |
| P3 | CR-22 … CR-58 |
| INFO (optional) | CR-I1 … CR-I14 |

---

## P1 — High

### - [x] CR-01 — Suggested topics can never get exercises (invalid `stable_key`)
- **Problem:** `AcceptSuggestion` stores `stable_key = "suggestion-N"`. The manifest schema requires `^[0-9a-f]{16}$`, so `exercises.Build` rejects every set for a suggested topic. The AI is called twice and the job fails with `invalid_output`.
- **Affected files:**
  - `internal/services/topics/suggestions.go:89-96`
  - `internal/services/exercises/build.go:91,136`
  - `internal/schema/manifest.schema.json:15`
  - new migration `internal/store/migrations/0004_*.sql` (if existing rows must be rewritten)
  - `internal/services/rebuild/rebuild.go` (check how it handles suggested keys)
- **Expected outcome:** every topic the app creates has a key that passes the manifest, rubric and result schemas.
- **Suggested implementation:**
  - Generate the key as `fsx.HashString("suggestion:"+strconv.FormatInt(id,10))[:16]`, or reuse `roadmap` key hashing on a synthetic path.
  - Add a migration that rewrites `topics.stable_key LIKE 'suggestion-%'` to the new format. No manifests exist for those topics, because generation always failed.
- **Required regression tests:**
  - Service test: accept suggestion → generate lesson (fake) → generate exercises (fake) → job `succeeded`, and `manifest.json` validates.
  - Unit test: the key produced by `AcceptSuggestion` matches `^[0-9a-f]{16}$`.
  - Migration test: an old `suggestion-7` row is rewritten.
- **Acceptance criteria:**
  - [ ] The end-to-end flow suggestion → exercises → evaluation works with the fake adapter.
  - [ ] No code path inserts a non-hex `stable_key` (grep plus test).
  - [ ] Existing databases are migrated without data loss.
- **Depends on:** none.

### - [x] CR-02 — AI child holding stdout discards a valid result or hangs `Run`
- **Problem:** `adapter.go` reads `StdoutPipe` to EOF before `cmd.Wait()`, so `WaitDelay` never bounds the read. A grandchild that keeps stdout open makes a SUCCESS result end as `timeout`/`stalled`. A `setsid`'d grandchild blocks cancel and shutdown until it exits.
- **Affected files:** `internal/ai/antigravity/adapter.go:176-196,262-264,281-292`; `internal/ai/antigravity/adapter_test.go` (fake CLI scenarios).
- **Expected outcome:** `Run` returns within `Timeout + KillGrace + WaitDelay` whatever background processes the CLI leaves behind, and a received SUCCESS result is kept when the leader exits 0.
- **Suggested implementation:**
  - Replace `StdoutPipe` with `cmd.Stdout = lineWriter{…}`, a writer that splits lines and feeds the NDJSON decoder, the events channel (with a select on ctx) and the log. `exec` then owns the copy and `WaitDelay` closes it.
  - Alternatively, run `cmd.Wait()` in a goroutine; once the leader has exited and `WaitDelay` has passed, close the reader.
  - In `outcome()`, check `res.Status == "SUCCESS" && leaderExitCode == 0` before the ctx checks.
- **Required regression tests:** new fake-CLI scenarios, all with assertions on duration:
  - (a) print SUCCESS, `sleep 30 &`, exit 0 → `err == nil`, structured output set, returns in under 6 s;
  - (b) the same with `setsid sleep 30 &` → returns within the bound;
  - (c) `Timeout=0, Stall=0` with (a) → still returns.
- **Acceptance criteria:**
  - [x] All three scenarios pass under `-race`.
  - [x] Cancel during a run with a lingering grandchild completes in ≤ `KillGrace + WaitDelay`.
  - [x] Existing adapter tests stay green.
- **Depends on:** none. Coordinate with CR-22, which touches the same kill path.

### - [x] CR-03 — Implement `commit_journal` replay (crash recovery for lesson, exercises and evaluation commits)
- **Problem:** journals are written but never read. A crash between the file writes and the DB transaction leaves orphan files, and every later job of that kind for that topic or set fails with `ErrFileExists`.
- **Affected files:**
  - `internal/jobs/runner.go:204-211` (`Recover`)
  - `internal/services/lessons/lessons.go:289-332`
  - `internal/services/exercises/exercises.go:234-259`
  - `internal/services/evaluation/commit.go:141-164`
  - a new shared helper (e.g. `internal/jobs/journal.go`)
- **Expected outcome:** after a restart, every job that is not succeeded and has a journal is reconciled; the next job of the same kind succeeds with the same K or vN.
- **Suggested implementation:**
  1. Define one journal JSON format, shared by all three commits: list of `{rel_path, sha256}`, plus an optional `{restore: {path, from_history, sha}}` for lesson regeneration.
  2. In `Recover`, before the status update, `SELECT id, commit_journal FROM jobs WHERE commit_journal IS NOT NULL AND status != 'succeeded'`.
  3. For each entry: if the file exists, its SHA matches, and no `files.rel_path` row exists, remove it.
  4. For a lesson regeneration whose `lesson.md` SHA equals the new SHA while the DB still holds the old one, restore `lesson.md` from the history file, then remove the history copy.
  5. Set `commit_journal = NULL`.
  6. Never delete a file whose SHA differs; log it and report it on the job instead.
  7. Clear the journal on successful commit too.
- **Required regression tests (one per commit kind):**
  - Write the journal and its files without rows → `Recover` → files removed → the next job succeeds with the same K / vN.
  - Negative case: a modified file is kept and reported.
  - Lesson regeneration case: the previous `lesson.md` is restored byte-for-byte.
- **Acceptance criteria:**
  - [x] A crash-injection test (a hook before `WithTx`) for each of the 3 commits recovers automatically.
  - [x] No user-modified file is ever deleted by the recovery.
  - [x] TDD §6.6 and `PROGRESS.md` mark journal recovery as implemented.
- **Depends on:** none. CR-07 makes recovery more reliable, so do it next. CR-33 relies on this task.

### - [x] CR-04 — Sync scan aborts on any file name that `fsx` rejects
- **Problem:** `syncsvc.unregistered` collects every file under `topics/`; `match` hashes them through `Root.HashFile` → `ValidateRelPath`. One name such as `aux.md`, `why?.md`, `notes. ` or a long Persian file name makes Scan fail completely.
- **Affected files:** `internal/services/syncsvc/syncsvc.go:194-205,226-234`; `internal/httpserver/handlers_sync.go` and `internal/ui/templates/sync.html` (to show the new category).
- **Expected outcome:** Scan always completes. Unsupported names are reported, never hashed and never used as move candidates.
- **Suggested implementation:**
  - In `unregistered`, run `fsx.ValidateRelPath(p)`. On error, append to a new `Report.Unsupported []string` and skip the file.
  - Render the list in `sync.html` with a hint to rename.
  - Share the helper with CR-50.
- **Required regression tests:** missing `lesson.md` plus files `aux.md`, `a?.md`, `x. ` and a 230-byte name → Scan succeeds, `Missing=[lesson.md]`, all four are listed as unsupported, and none appear in the move proposals.
- **Acceptance criteria:**
  - [x] Scan never returns an error caused by a user file name.
  - [x] The UI shows unsupported files.
- **Depends on:** none.

---

## P2 — Medium

### - [x] CR-05 — Make roadmap Confirm/re-import idempotent with leftover files
- **Problem:** an orphan `rev-NNN.md`, `source.md` or `source-history/source.NNNN.md` (left by a crash or a failed undo) makes every later Confirm fail, and undo errors are ignored.
- **Affected files:** `internal/services/roadmaps/confirm.go:105,207-222,268,286,292`.
- **Expected outcome:** a retry succeeds without manual cleanup, and nothing is overwritten.
- **Suggested implementation:**
  - Add a helper `writeOrAdopt(rel, data)`: if the file exists with an identical SHA, adopt it; if it exists with a different SHA, choose the next free number for revision and history files, and use the CR-06 helper for topic folders.
  - Log every undo `Remove` error.
- **Required regression tests:** orphan `rev-002.md` with the same bytes → adopted; with different bytes → `rev-003.md`; orphan `topics/04-x/source.md` → the import uses a free folder.
- **Acceptance criteria:**
  - [x] All three scenarios succeed.
  - [x] Pre-existing files are byte-identical afterwards.
- **Depends on:** CR-06.

### - [x] CR-06 — Compute topic folder numbers from the maximum in use, not `COUNT(*)`
- **Problem:** `COUNT(*)+1` collides with on-disk or DB `NN-` numbers after a rebuild, which causes `UNIQUE dir_name` failures.
- **Affected files:** `internal/services/roadmaps/confirm.go:150-176`; `internal/services/topics/suggestions.go:89-93`; `internal/services/roadmaps` (`TopicDir`).
- **Expected outcome:** one helper produces a free `NN-slug` that is unique both in the DB and on disk.
- **Suggested implementation:** `NextTopicSeq(tx, root)` = max(NN parsed from `topics.dir_name`, NN of directories under `topics/`) + 1; skip any candidate whose folder exists. Use it from both callers and remove the stale comment.
- **Required regression tests:** the rebuild-gap scenario from the report (`03-b`, `04-a`, then import `## B`) → succeeds with `05-b` or later; a suggestion-accept variant.
- **Acceptance criteria:**
  - [x] No caller computes topic numbers from `COUNT(*)` (grep).
  - [x] Both tests pass.
- **Depends on:** none.

### - [x] CR-07 — `WriteNew` via temp file + hard link (crash-safe never-overwrite)
- **Problem:** `WriteNew` writes directly to the final name, so a crash leaves a truncated protected file that can never be rewritten.
- **Affected files:** `internal/fsx/writenew.go:20-48`; `internal/fsx/fsx_test.go`.
- **Expected outcome:** the final name appears only with complete, fsynced content (TDD §6.4).
- **Suggested implementation:** create `.<base>.tmp-<rand>` with `O_EXCL` → write → fsync → close → `rawRoot.Link(tmp, final)` (`EEXIST` → `ErrFileExists`) → remove tmp → fsync the directory (CR-34). If the platform refuses hard links, fall back to the current behaviour and leave a `shortcut:` comment.
- **Required regression tests:**
  - A failing writer after a partial write → final name absent, tmp removed.
  - Existing target → `ErrFileExists`, target unchanged.
  - Concurrent `WriteNew` calls to the same name → exactly one wins.
- **Acceptance criteria:**
  - [x] All existing `WriteNew` tests pass.
  - [x] Never-overwrite semantics are unchanged.
- **Depends on:** none. Improves CR-03.

### - [x] CR-08 — Verify the manifest and rubric at evaluation commit
- **Problem:** the evaluation commit and step use an unvalidated, user-editable `manifest.json` and `rubric-vN.json`. Removed exercises are dropped silently, edited weights change scores, and empty criteria are accepted.
- **Affected files:** `internal/services/evaluation/commit.go:54-123`; `internal/services/evaluation/evaluation.go:133-143`.
- **Expected outcome:** the official score always uses the set as recorded in the DB.
- **Suggested implementation:**
  - Compare the SHA of each file with its `files.sha256` row; on mismatch, fail with `ErrInvalidState` and a clear message.
  - Alternatively, run `ValidateManifest`/`ValidateRubric` plus the cross-checks and compare the exercise list with the DB.
  - Require every `sel.Send` exercise to be in the manifest. Use DB weights.
- **Required regression tests:** remove a sent exercise from the manifest → commit fails and nothing is written; change a weight → fail (or DB weight used); rubric with `criteria: []` → fail.
- **Acceptance criteria:**
  - [ ] All three scenarios are covered.
  - [ ] The normal flow is unchanged.
- **Depends on:** none.

### - [x] CR-09 — Record answers that need manual review as "needs review", not "unanswered"
- **Problem:** answers in `sel.Review` (oversized or binary) become `unanswered` with score 0, with no label.
- **Affected files:** `internal/services/evaluation/commit.go:90-123,237-245`; `internal/services/evaluation/report.go:209-211`; `internal/services/topics/select.go:47-66`; templates for item status labels.
- **Expected outcome:** these items carry a `needs_review` flag and show «⚠ نیازمند بازبینی».
- **Suggested implementation:** pass `sel.Review` into `carried()`; set `Flags: ["needs_review"]` (the `attempt_items.flags` column already exists); add the label mapping in the UI and in `summary.md`.
- **Required regression tests:** evaluate q01 and q02; grow q02 past the cap; evaluate again → q02 is flagged `needs_review` and is not labelled unanswered in the result, the summary and the UI.
- **Acceptance criteria:**
  - [ ] The label is visible.
  - [ ] The score policy is documented (0 or carried).
- **Depends on:** none.

### - [ ] CR-10 — Refuse in-root symlinks under `.app/` and restrict `Root.RemoveAll`
- **Problem:** a symlink or junction such as `.app/work` → `topics` makes sandbox cleanup delete user folders and write the sandbox into them. `RemoveAll` accepts any path.
- **Affected files:** `internal/fsx/root.go:167-180`; `internal/ai/prompt/composer.go:94-105`; `internal/jobs/runner.go:327`; `internal/services/maintenance/maintenance.go:58-81`.
- **Expected outcome:** no operation under `.app/` follows a symlink or reparse point; `RemoveAll` only works under `.app/work/`, `.app/staging/` or `.app/logs/`.
- **Suggested implementation:**
  - Add `fsx.Root.EnsureRealDir(rel)`, which runs `Lstat` on each path component and fails on `ModeSymlink` or `ModeIrregular` (a Windows reparse point).
  - Call it before `RemoveAll`/`MkdirAll` in the composer, the runner and maintenance.
  - Add a prefix allowlist inside `RemoveAll`.
- **Required regression tests:** `.app/work` → `topics` symlink → `Compose` fails and `topics/7` is unchanged; `RemoveAll("topics")` returns an error; maintenance skips symlinked entries.
- **Acceptance criteria:**
  - [ ] All tests pass on Linux.
  - [ ] Windows junction handling is reviewed in code.
- **Depends on:** none.

### - [ ] CR-11 — Confine Claude/Codex reads to the sandbox and ignore project settings
- **Problem:** `--allowedTools Read,Glob,Grep` has no path scope; `--setting-sources project` honours `.claude/settings.json` in the base directory or git root; `codex --sandbox read-only` can read the whole disk.
- **Affected files:** `internal/ai/antigravity/dialects.go:67-113`; `internal/ai/antigravity/dialects_test.go`; `INSTALL.md` (risk note).
- **Expected outcome:** an AI run can read only its `WorkDir`, and no project hooks or settings apply.
- **Suggested implementation:**
  - Verify with the real CLI version, then use path-scoped rules (e.g. `Read(./**)`, `Glob(./**)`, `Grep(./**)`) or drop `--allowedTools` so that `-p` denies reads outside cwd.
  - Pass `--setting-sources ""`, or whatever value the CLI uses for "none".
  - For Codex, document the residual risk in `INSTALL.md` and the settings page.
- **Required regression tests:** an argument-builder test asserting the scoped rules and the setting sources. A manual check (recorded in `PROGRESS.md`): a prompt that asks to read `../../app.db` is refused by the real CLI.
- **Acceptance criteria:**
  - [ ] Tests pass.
  - [ ] The manual verification is recorded.
  - [ ] The residual Codex risk is documented.
- **Depends on:** none.

### - [ ] CR-12 — Bound `Probe` subprocesses and classify probe timeouts correctly
- **Problem:** `Probe` uses `CombinedOutput` without `WaitDelay` or a process group, so it can exceed its deadline. The settings handler holds `probeMu` during the probe. Timeouts are reported as "not authenticated".
- **Affected files:** `internal/ai/antigravity/adapter.go:70-110`; `internal/httpserver/handlers_settings.go:62-73`.
- **Expected outcome:** `Probe` returns within deadline + `WaitDelay`; a timeout gives `ErrTimeout` with a clear message; `/settings` never blocks for longer than one probe.
- **Suggested implementation:** set `cmd.WaitDelay = 2*time.Second` and start the command via `procgroup`; map `ctx.Err()` to `ai.ErrTimeout`; replace mutex-held probing with a singleflight-style cached result.
- **Required regression tests:** fake `--version` that runs `sleep 3 &` with a 500 ms context → returns in under 3 s with a timeout error; concurrent `/settings` requests do not serialize behind the probe.
- **Acceptance criteria:**
  - [ ] Both tests pass.
  - [ ] The "auth required" hint no longer appears on a timeout.
- **Depends on:** CR-48, which removes the probe from `GET /settings`; do the two together.

### - [ ] CR-13 — Handle Windows `.cmd`/`.bat` shims safely (claude, codex, editor)
- **Problem:** npm installs `claude.cmd` and `codex.cmd`. Go does not quote arguments correctly for `cmd.exe`, so the multi-line prompt and schema arguments can be truncated or interpreted.
- **Affected files:** `internal/ai/antigravity/adapter.go:66,126-176`; `internal/ai/antigravity/dialects.go:67-113`; `internal/httpserver/handlers_exercises.go:277`.
- **Expected outcome:** on Windows, `.cmd`/`.bat` targets are refused with an actionable message, or the prompt and schema are passed through stdin or a file.
- **Suggested implementation:**
  - After `LookPath`, if the extension is `.cmd` or `.bat` (case-insensitive), return `ErrAdapterMissing` with a hint (use the native `.exe`, or configure `binary` to point at `node <entry>.js`).
  - Alternatively, feed the prompt via `cmd.Stdin` (`claude -p` with no prompt argument reads stdin; `codex exec -`) and the schema via a file in `WorkDir`.
- **Required regression tests:** Windows-only (`//go:build windows`): a `.cmd` echo shim is refused, or receives a multi-line argument intact. A cross-platform test that the stdin path delivers the exact prompt bytes.
- **Acceptance criteria:**
  - [ ] Windows CI (CR-58) runs the test.
  - [ ] The manual Windows checklist covers Claude and Codex.
- **Depends on:** CR-58 (Windows CI) for verification.

### - [ ] CR-14 — Escape the database path in the SQLite DSN
- **Problem:** `fmt.Sprintf("file:%s?…", absPath)` lets `#`, `?` and `%` change which file is opened.
- **Affected files:** `internal/store/db.go:24-40`; `internal/store/db_test.go`.
- **Expected outcome:** the database is always created at exactly `<base_dir>/app.db` (or the given `--db`).
- **Suggested implementation:** `p := strings.NewReplacer("%", "%25", "#", "%23", "?", "%3f").Replace(filepath.ToSlash(absPath))`. Verify the Windows `file:C:/…` form with modernc, and add a leading `/` if it is required.
- **Required regression tests:** `Open(tmp/"a#b"/"app.db")`, `Open(tmp/"p%41"/"app.db")` and `Open(tmp/"q?x"/"app.db")` (skip the last on Windows) → the file exists at the exact path and nothing is created in the parent.
- **Acceptance criteria:**
  - [ ] Tests pass on Linux and, through CR-58, on Windows.
- **Depends on:** none.

### - [ ] CR-15 — Make stable-key de-duplication linear
- **Problem:** `childPath` restarts the `#n` search at 2 for every duplicate, which is O(d²); 10 k duplicate headings take 5.4 s.
- **Affected files:** `internal/roadmap/stablekey.go:45-56`; `internal/roadmap/select.go` (the caller holding `used`).
- **Expected outcome:** linear time, and identical keys for every existing input.
- **Suggested implementation:** keep `next map[string]int` beside `used`; start the loop at `max(2, next[base])` and store `next[base] = n+1`.
- **Required regression tests:** benchmark plus a test: 50 k `## h` headings → `Select` takes under 1 s. Golden and stable-key tests unchanged.
- **Acceptance criteria:**
  - [ ] Golden outputs are byte-identical.
  - [ ] The performance test passes.
- **Depends on:** none.

### - [ ] CR-16 — Slice roadmap sources in one pass and outside the write transaction
- **Problem:** `roadmap.Slice` re-normalises the whole file on each call; `Outline` and `Confirm.recordSource` call it per heading or topic, the latter inside the IMMEDIATE transaction.
- **Affected files:** `internal/roadmap/parse.go:81-93`; `internal/services/roadmaps/analyze.go:273`; `internal/services/roadmaps/confirm.go:237`.
- **Expected outcome:** O(size + topics), and no CPU-heavy work while the writer is held.
- **Suggested implementation:** add `type Lines struct{…}` with `NewLines(src)` (normalise and compute line starts once) and `Slice(start, end)`. Compute every `source.md` body before `WithTx`. Optionally add a `limits.max_topics_per_import` cap.
- **Required regression tests:** `Outline` over 50 k headings in under 1 s; equality test `NewLines(src).Slice(a,b) == Slice(src,a,b)` for random ranges; `Confirm` benchmark.
- **Acceptance criteria:**
  - [ ] Results are byte-identical to the old `Slice`.
  - [ ] The benchmark improves by ≥ 10×.
- **Depends on:** none.

### - [ ] CR-17 — Bound and cache the re-import diff guesses
- **Problem:** fuzzy matching runs Levenshtein over every new × gone pair on each preview render; 3 k × 3 k takes ~20 s.
- **Affected files:** `internal/services/roadmaps/diff.go:103-147`; `internal/httpserver/handlers_roadmaps.go:357`; `import_drafts.diff` column (exists, unused).
- **Expected outcome:** a diff render finishes in under 1 s for large inputs.
- **Suggested implementation:**
  - Skip a pair when `min(len)/max(len) < 0.6`.
  - Cap compared runes at 200.
  - Skip guessing entirely when pairs exceed 250 k, and show a note.
  - Cache the computed diff in `import_drafts.diff`, keyed by draft content + rule + checked items.
- **Required regression tests:** `diffTopics` with 3000 × 3000 → under 1 s; the existing diff tests stay green.
- **Acceptance criteria:**
  - [ ] Guess quality is unchanged on the existing fixtures.
- **Depends on:** none.

### - [ ] CR-18 — Rebuild must survive copied topic folders
- **Problem:** two folders with the same `stable_key` in their manifest or result make rebuild fail with `UNIQUE(roadmap_id, stable_key)`.
- **Affected files:** `internal/services/rebuild/rebuild.go:243-275`.
- **Expected outcome:** rebuild completes with a warning for the duplicate.
- **Suggested implementation:** track used keys per roadmap; on a clash, use `fsx.HashString("rebuild:"+dir)[:16]` and append a warning.
- **Required regression tests:** `topics/02-http` and `topics/02-http-bak` with the same manifest → success, two topics, one warning.
- **Acceptance criteria:**
  - [ ] Test passes.
  - [ ] The CLI prints the warning.
- **Depends on:** none.

### - [x] CR-19 — Persist the selection rule so rebuild can reproduce topics
- **Problem:** rebuild always applies `deepest` and puts unmatched topics in the first roadmap, which distorts roadmaps imported with chapters, items or level rules.
- **Affected files:** `internal/services/rebuild/rebuild.go:152,307-318`; `internal/services/roadmaps/confirm.go` (to write the rule file).
- **Expected outcome:** after a rebuild, topic keys and roadmap membership equal the originals.
- **Suggested implementation:**
  - At Confirm, write `roadmaps/<slug>/rule.json` (`WriteAtomic`) holding the rule and the chapter ranges, or a per-topic `topic.json` with key, roadmap slug and range.
  - Rebuild reads it, falling back to `deepest`, and chooses the fallback roadmap per folder (from the `roadmaps/<slug>` that the topic's `source.md` came from).
- **Required regression tests:** confirm a chapters-rule import → delete the DB → rebuild → keys and roadmaps match; legacy roadmap without `rule.json` → current behaviour plus a warning.
- **Phase 8 (TDD §13):** the rule file must also cover the `tree` rule, and rebuild must restore `topics.parent_id` (parent = nearest chosen ancestor heading); add a tree-rule case to the regression test.
- **Acceptance criteria:**
  - [x] Both tests pass.
  - [x] The TDD rebuild section is updated.
- **Depends on:** CR-06 (numbering helper reused by rebuild), CR-18.
- **Done (2026-11-10, R5):** per-revision `roadmaps/<slug>/rev-NNN.choice.json` (rule + excluded keys) instead of one `rule.json`, because the rule can change on re-import. `TestRebuildKeepsSelectionRule` (level rule with an exclusion, then a chapters re-import); legacy case in `TestRebuildFromFiles`. Not done: per-folder fallback roadmap for unmatched topics (still the first roadmap); `tree` + `parent_id` left to T8.3.

### - [ ] CR-20 — Surface htmx errors and recover from a stale CSRF token
- **Problem:** responses not in 2xx/409/413/422 are neither swapped nor shown. After a server restart every htmx POST silently returns 403.
- **Affected files:** `internal/ui/static/app.js`; `internal/ui/templates/layout.html:6` (add an alert container); optionally `internal/httpserver/middleware.go` (send a distinguishable 403 body or header, e.g. `HX-Trigger: csrf-stale`).
- **Expected outcome:** the user always sees feedback; a stale token leads to an automatic reload.
- **Suggested implementation:** in `app.js`, `document.body.addEventListener("htmx:responseError", …)` and `"htmx:sendError"`: on a 403 CSRF failure, `location.reload()`; otherwise write a Persian message with `textContent` into `<div id="flash" role="alert">`.
- **Required regression tests:** template test: the layout contains `#flash` and `app.js` registers both listeners. Browser or acceptance test: an htmx POST with a wrong token reloads the page or shows the alert.
- **Acceptance criteria:**
  - [ ] Manual check: restart the server with a tab open, click an action → page reloads and the action works.
- **Depends on:** none.

### - [x] CR-21 — Upgrade `golang.org/x/net` and run govulncheck
- **Problem:** the indirect `x/net v0.26.0` (used by bluemonday's tokenizer) predates fixes such as GO-2025-3595.
- **Affected files:** `go.mod:24`, `go.sum`; `PROGRESS.md` (record the `make vuln` result).
- **Expected outcome:** current `x/net`, and a clean govulncheck report recorded.
- **Suggested implementation:** `go get golang.org/x/net@latest && go mod tidy`; `make vuln` with network access; also upgrade other modules if govulncheck reports anything reachable.
- **Required regression tests:** the existing `render` XSS corpus, plus a payload from CVE-2025-22872 (unquoted attribute ending in `/`).
- **Acceptance criteria:**
  - [x] `govulncheck ./...` reports no reachable vulnerabilities.
  - [x] The output is pasted into `PROGRESS.md`.
- **Depends on:** none. Unblocks the govulncheck CI step in CR-58.

---

## P3 — Low

### - [ ] CR-22 — Fix process-group kill timers (Unix)
- **Problem:** `Kill` arms a SIGKILL timer that is never stopped (risk of killing a reused pgid). `release()` sends SIGKILL immediately, so grandchildren get no grace period.
- **Affected files:** `internal/ai/procgroup/procgroup_unix.go:26-46`; `procgroup_test.go`.
- **Expected outcome:** SIGTERM, a single grace timer, then SIGKILL; nothing is signalled after the group is known to be gone.
- **Suggested implementation:** store `*time.Timer` on `Group`; `release` stops it; after a clean leader exit, send SIGTERM and arm one timer instead of an immediate SIGKILL; make `Kill` idempotent.
- **Required regression tests:** a grandchild trapping SIGTERM gets ≥ 1 s before SIGKILL; a spy kill function records no signal 3 s after a successful `Close`.
- **Acceptance criteria:**
  - [ ] Existing procgroup tests stay green.
- **Depends on:** coordinate with CR-02.

### - [ ] CR-23 — Handle SIGHUP in `serve` and `doctor`
- **Problem:** closing the terminal kills the hub without cancellation; AI children in their own group keep running.
- **Affected files:** `cmd/hub/main.go:100,266` (plus a Unix-only file for the signal list).
- **Expected outcome:** SIGHUP triggers the same graceful shutdown as SIGTERM.
- **Suggested implementation:** a `shutdownSignals` var with `os.Interrupt, syscall.SIGTERM, syscall.SIGHUP` on Unix and without SIGHUP on Windows; optionally `SysProcAttr.Pdeathsig = SIGKILL` on Linux.
- **Required regression tests:** subprocess test: start `hub serve` with a slow fake job, send SIGHUP → process exits, job is `failed/interrupted`, the child process is gone.
- **Acceptance criteria:**
  - [ ] Test passes on Linux.
- **Depends on:** CR-44 (testable exit path).

### - [ ] CR-24 — Do not ignore Windows Job Object attach errors
- **Problem:** errors from `OpenProcess`/`AssignProcessToJobObject` are discarded, so tree-kill silently degrades.
- **Affected files:** `internal/ai/procgroup/procgroup_windows.go:38-49`.
- **Expected outcome:** `Start` fails (and kills the child) if attaching fails, or the failure is at least logged.
- **Suggested implementation:** return a wrapped error; the caller maps it to `tool_error`.
- **Required regression tests:** Windows-only: force failure with a closed job handle → `Start` returns an error and no process remains.
- **Acceptance criteria:**
  - [ ] Test runs in Windows CI.
- **Depends on:** CR-58.

### - [x] CR-25 — Unique log directories for parallel AI runs
- **Problem:** `.app/logs/jobs/<job>/<ns>-<task>` can collide between parallel evaluation steps.
- **Affected files:** `internal/ai/antigravity/adapter.go:327-338`.
- **Expected outcome:** each run gets its own directory.
- **Suggested implementation:** include `req.Step` (validated with `fsx.ValidateSegment`) and a short random suffix in the name.
- **Required regression tests:** two runs with an injected fixed clock and the same job and task → two directories, both succeed.
- **Acceptance criteria:**
  - [x] Test passes.
- **Depends on:** none.

### - [ ] CR-26 — Respect cancellation before starting and before retrying
- **Problem:** `Run(cancelledCtx)` returns `ErrToolFailed`; `WatchValid` starts a second attempt after a cancel.
- **Affected files:** `internal/ai/antigravity/adapter.go:126-191`; `internal/ai/retry.go:34`.
- **Expected outcome:** `ErrCanceled`/`ErrTimeout` with no process and no log directory.
- **Suggested implementation:** an early `if err := ctx.Err(); err != nil { return nil, mapCtxErr(err) }` in `Run` and before the retry.
- **Required regression tests:** `Run(cancelled)` → `errors.Is(err, ai.ErrCanceled)`; `WatchValid` with a cancel after the first invalid output → exactly 1 adapter call.
- **Acceptance criteria:**
  - [ ] Tests pass.
- **Depends on:** none.

### - [ ] CR-27 — Bound stderr and NDJSON line buffers
- **Problem:** stderr is an unbounded `bytes.Buffer`; `ReadBytes('\n')` has no line limit.
- **Affected files:** `internal/ai/antigravity/adapter.go:179,262-264`; `internal/ai/antigravity/stream.go:81`.
- **Expected outcome:** memory stays bounded for any CLI output.
- **Suggested implementation:** a tail ring buffer for stderr (64 KB); a per-line cap of 16 MB → `invalid_output`.
- **Required regression tests:** a fake CLI writes 100 MB to stderr → bounded allocation (`testing.AllocsPerRun` or a measurement); an over-long line → `invalid_output`.
- **Acceptance criteria:**
  - [ ] Tests pass.
- **Depends on:** CR-02 (the same reader code changes).

### - [ ] CR-28 — Truncate strings on rune boundaries
- **Problem:** byte slicing splits Persian UTF-8 in error messages and retry notes.
- **Affected files:** `internal/ai/retry.go:28-29`; `internal/ai/antigravity/stream.go:95-96`.
- **Expected outcome:** truncated strings are always valid UTF-8.
- **Suggested implementation:** a helper `truncateRunes(s, maxBytes)` that steps back to `utf8.RuneStart`.
- **Required regression tests:** for every byte length of a Persian string, the result is `utf8.ValidString`.
- **Acceptance criteria:**
  - [ ] Tests pass.
- **Depends on:** none.

### - [ ] CR-29 — Narrow the error-classification input
- **Problem:** pattern matching scans the whole stderr, so generic words produce wrong `rate_limited`/`auth_required` codes.
- **Affected files:** `internal/ai/antigravity/stream.go:63-88`; `adapter.go:296-308`.
- **Expected outcome:** classification uses `res.Error` first, then only the last lines of stderr.
- **Suggested implementation:** run `classify(res.Error)`; if that is empty, `classify(lastLines(stderr, 20))`.
- **Required regression tests:** stderr with an early "quota project" debug line followed by a crash → `tool_error`; a real "rate limit" final line → `rate_limited`.
- **Acceptance criteria:**
  - [ ] Existing classification tests stay green.
- **Depends on:** CR-27.

### - [ ] CR-30 — Align AI output schemas with downstream limits; enforce `max_lesson_bytes`
- **Problem:** AI schemas lack the length and count limits of `manifest`/`rubric`, which wastes retries; `limits.max_lesson_bytes` is never used.
- **Affected files:** `internal/schema/ai-*.schema.json`; `internal/schema/rubric.schema.json`; `internal/schema/manifest.schema.json`; `internal/services/lessons/lessons.go` (`Validate`); `cmd/hub/main.go` (pass the limit).
- **Expected outcome:** anything the AI schema accepts, `Build` accepts; oversized lessons are rejected.
- **Suggested implementation:** copy the `maxLength`/`maxItems`/`minLength` limits; add a `MaxBytes` field to `lessons.Service` and check it in `Validate`.
- **Required regression tests:** a schema-consistency test comparing limits field by field; a lesson over the cap → `invalid_output`.
- **Acceptance criteria:**
  - [ ] Tests pass.
  - [ ] No config option is left without a consumer (grep).
- **Depends on:** none.

### - [ ] CR-31 — Include the adapter name in the evaluation retry fingerprint
- **Problem:** after switching between tools with the same (empty) model, a retry reuses another tool's staged results.
- **Affected files:** `internal/services/evaluation/evaluation.go:158-166`.
- **Expected outcome:** a different adapter means a different fingerprint, so no reuse.
- **Suggested implementation:** `fp.WriteString(s.Adapter.Name() + "\x00")`.
- **Required regression tests:** fail under fake adapter "a", switch to "b", retry → every step re-runs.
- **Acceptance criteria:**
  - [ ] Test passes.
- **Depends on:** consider together with CR-42.

### - [ ] CR-32 — Check staged evaluation outputs against the current pick
- **Problem:** the commit ignores `out.ExerciseID` and `out.AnswerSHA256`.
- **Affected files:** `internal/services/evaluation/commit.go:96-111`.
- **Expected outcome:** a mismatch fails the commit with `ErrInvalidState`.
- **Suggested implementation:** a two-line guard before use.
- **Required regression tests:** tamper with a staged output's SHA → commit fails and no files are written.
- **Acceptance criteria:**
  - [ ] Test passes.
- **Depends on:** none.

### - [ ] CR-33 — Undo on panic in commits; recover panics in evaluation step goroutines
- **Problem:** undo runs only on a returned error; a panic in a `wg.Go` step crashes the server.
- **Affected files:** `internal/services/lessons/lessons.go:330-332`; `internal/services/exercises/exercises.go:261-347`; `internal/services/evaluation/commit.go:166-231`; `internal/services/evaluation/evaluation.go:205-210`.
- **Expected outcome:** a panic leaves no orphan files and fails only the job.
- **Suggested implementation:** a `committed` flag plus `defer func(){ if !committed { undo() } }()`; inside `wg.Go`, `defer func(){ if p := recover(); p != nil { once.Do(func(){ first = fmt.Errorf("panic: %v", p) }) } }()`.
- **Required regression tests:** inject a panic into a step → job `failed`, server alive; inject a panic into the tx closure → no files remain.
- **Acceptance criteria:**
  - [ ] Tests pass under `-race`.
- **Depends on:** CR-03 (the journal covers crashes that undo cannot).

### - [ ] CR-34 — Fsync directories after create or rename; sweep stale temp files
- **Problem:** directory entries are not fsynced; `.tmp-*` leftovers from `WriteAtomic` are never removed.
- **Affected files:** `internal/fsx/writenew.go:43,91-107`; `internal/services/maintenance/maintenance.go`.
- **Expected outcome:** durable create and rename on Unix; no tmp files older than 1 h.
- **Suggested implementation:** a `syncDir(rel)` helper (no-op on Windows), called after `WriteNew`, `WriteAtomic` and `Link`; maintenance walks the known lesson and topic directories for `.tmp-*` older than 1 h.
- **Required regression tests:** a directory-sync spy is called; maintenance removes an old tmp file and keeps a fresh one.
- **Acceptance criteria:**
  - [ ] Tests pass.
- **Depends on:** CR-07.

### - [ ] CR-35 — Bound answer-file reads
- **Problem:** after a `Stat` check, the whole file is read into memory.
- **Affected files:** `internal/services/topics/select.go:40-66`.
- **Expected outcome:** at most `max_answer_bytes + 1` bytes are read.
- **Suggested implementation:** open, then `io.ReadAll(io.LimitReader(f, max+1))`.
- **Required regression tests:** a file larger than the cap → `Review`, with allocation bounded (an injected reader).
- **Acceptance criteria:**
  - [ ] Test passes.
- **Depends on:** CR-09 (same selection data).

### - [ ] CR-36 — Make the daily backup atomic
- **Problem:** an interrupted `VACUUM INTO` leaves a 0-byte `app-DATE.db`, which blocks that day's backup and pushes a real one out of the rotation.
- **Affected files:** `internal/services/maintenance/maintenance.go:90-119`.
- **Expected outcome:** only complete backups carry the final name.
- **Suggested implementation:** remove any stale `.tmp`, run `VACUUM INTO app-DATE.db.tmp`, rename on success, remove on failure; ignore 0-byte files in the exists and rotation checks.
- **Required regression tests:** `Run` with a cancelled ctx → no `app-*.db`; a second run the same day → a non-empty backup exists.
- **Acceptance criteria:**
  - [ ] Tests pass.
- **Depends on:** none.

### - [ ] CR-37 — Retry `Recover` instead of stopping the runner
- **Problem:** if `Recover` fails at startup, `Run` returns and jobs never execute, while the UI still enqueues them.
- **Affected files:** `internal/jobs/runner.go:214-219`; `cmd/hub/main.go:346-351`.
- **Expected outcome:** the runner keeps trying with backoff (or `serve` exits with an error).
- **Suggested implementation:** a loop with exponential backoff up to 30 s, honouring ctx; log every failure.
- **Required regression tests:** hold `BEGIN IMMEDIATE` on a second connection, start `Run`, release after 6 s → a queued job runs.
- **Acceptance criteria:**
  - [ ] Test passes under `-race`.
- **Depends on:** none.

### - [ ] CR-38 — Retry the final job-status write
- **Problem:** if the final `UPDATE` fails, the job stays `running`; after a restart it becomes `failed` even if its commit succeeded.
- **Affected files:** `internal/jobs/runner.go:318-325`.
- **Expected outcome:** the final status always lands, or the failure is visible.
- **Suggested implementation:** 5 retries with backoff on the `WithoutCancel` ctx; if all fail, keep an in-memory override that `Get` returns.
- **Required regression tests:** an injected writer failing twice → status recorded.
- **Acceptance criteria:**
  - [ ] Test passes.
- **Depends on:** none.

### - [ ] CR-39 — Lock `hub migrate`; refuse databases newer than the binary
- **Problem:** `migrate` ignores `hub.lock`; `migrateTo` treats `current > target` as up to date.
- **Affected files:** `cmd/hub/main.go:131-160`; `internal/store/db.go:136-160`.
- **Expected outcome:** `migrate` refuses while `serve` runs; an older binary refuses a newer schema with a clear message.
- **Suggested implementation:** reuse the flock code from `runRebuild`; `if current > target { return fmt.Errorf("database schema v%d is newer than this hub (v%d)", …) }`.
- **Required regression tests:** `user_version=99` → `Open` fails; `migrate` with the lock held → exit 1.
- **Acceptance criteria:**
  - [ ] Tests pass.
- **Depends on:** CR-44 (exit codes).

### - [ ] CR-40 — Tighten config validation
- **Problem:** integer durations are read as nanoseconds; zero or negative timeouts are accepted; `log_retention_days <= 0` purges everything; a missing `--config` file is ignored; unknown keys are ignored.
- **Affected files:** `internal/config/config.go:55-63,143-222`; `internal/config/config_test.go`.
- **Expected outcome:** invalid configurations fail fast with a clear message.
- **Suggested implementation:**
  - Require every timeout ≥ 1 s (or 0 only where explicitly documented as "disabled").
  - Require `LogRetentionDays >= 1`.
  - Error if an explicit `--config` file does not exist.
  - `md, err := toml.DecodeFile(...)`; if `len(md.Undecoded()) > 0`, return an error that lists the keys.
- **Required regression tests:** one table case per rule.
- **Acceptance criteria:**
  - [ ] `config` coverage ≥ 80 %.
  - [ ] `INSTALL.md` lists the valid ranges.
- **Depends on:** none.

### - [ ] CR-41 — Resolve the default config location (code vs TDD)
- **Problem:** `DefaultConfigPath()` is unused; the TDD says config lives in `%APPDATA%\LearningHub\config.toml`.
- **Affected files:** `internal/config/config.go:130-141`; `cmd/hub/main.go` (Load calls); `technical-design-document.md:95,1307,1518`; `INSTALL.md`.
- **Expected outcome:** code and docs agree.
- **Suggested implementation:** either load `DefaultConfigPath()` when `--config` is absent and the file exists (preferred by the TDD R9 rationale), or delete the function and update the TDD.
- **Required regression tests:** `APPDATA` (or `HOME`) pointed at a temp dir containing `config.toml` → `Load("")` applies it (if option 1 is chosen).
- **Acceptance criteria:**
  - [ ] The decision is recorded in `PROGRESS.md`.
  - [ ] Docs are updated.
- **Depends on:** CR-40.

### - [ ] CR-42 — Make the adapter switch atomic with respect to jobs
- **Problem:** the check for active jobs and `Switch.Set` race with `Enqueue`.
- **Affected files:** `internal/httpserver/handlers_settings.go:120-151`; `internal/ai/switch.go`; `internal/jobs/runner.go` (Enqueue).
- **Expected outcome:** a job always runs with the adapter it recorded.
- **Suggested implementation (preferred):** capture the adapter per job. At claim time, store `adapter` in `jobs` and resolve `Switch.Get(name)` inside the handler, instead of reading the global current adapter. Alternative: perform the busy check and `Set` inside the runner's enqueue serialisation (a `Runner.WithIdle(func() error)`).
- **Required regression tests:** concurrent enqueue and switch with a barrier under `-race` → the recorded adapter equals the adapter that ran.
- **Acceptance criteria:**
  - [ ] Test passes.
  - [ ] The comment in `switch.go:21` is updated.
- **Depends on:** CR-31.

### - [ ] CR-43 — `hub doctor` and settings messages follow the selected adapter; doctor writes via `fsx`
- **Problem:** doctor probes `cfg.Adapter.Kind`/`cfg.Adapter.Model`, not the saved selection; the model warning hard-codes `agy`; the `.doctor_test` file is written with `os.WriteFile` and can overwrite a user file.
- **Affected files:** `cmd/hub/main.go:80-129`; `internal/httpserver/handlers_settings.go:50-56`.
- **Expected outcome:** doctor reports on the tool the UI will use, with correct wording, and never overwrites files.
- **Suggested implementation:**
  - Open the DB read-only (`mode=ro`) and read `SavedAdapter`, falling back to the config.
  - Use the selected tool's model.
  - Build the message from `Adapter.Name()` and the matching TOML section.
  - [x] Use `fsx.WriteNew` with a random name, then remove it. (done in R10; adapter-selection parts still open)
- **Required regression tests:** `meta.adapter_kind='claude'` → doctor probes `claude`; a message test for each adapter; an existing `.doctor_test` is not modified.
- **Acceptance criteria:**
  - [ ] Tests pass.
  - [ ] `PROGRESS.md:210` caveat removed.
- **Depends on:** none.

### - [ ] CR-44 — Non-zero exit status on fatal `serve` errors
- **Problem:** several fatal startup paths `return` from `runServe`, so the process exits 0.
- **Affected files:** `cmd/hub/main.go:220-372`.
- **Expected outcome:** exit 1 after the deferred cleanup.
- **Suggested implementation:** `func runServe(args) error`; `main` calls `os.Exit(1)` on a non-nil result, after `runServe` has returned (so its defers have run).
- **Required regression tests:** subprocess test: port 8787 occupied (use a custom `--config` with a free port that the test binds first) → exit status 1.
- **Acceptance criteria:**
  - [ ] Test passes.
  - [ ] `cmd/hub` has at least one test file.
- **Depends on:** none.

### - [ ] CR-45 — Full page on htmx history restore for `/topics`
- **Problem:** a history-cache miss returns the fragment, which replaces `<body>`.
- **Affected files:** `internal/httpserver/handlers_topics.go:39-42` (or `internal/ui/templates/layout.html:6`).
- **Expected outcome:** Back navigation always shows the full page.
- **Suggested implementation:** `if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-History-Restore-Request") != "true"`; or add `"refreshOnHistoryMiss":true` to the htmx config.
- **Required regression tests:** request with both headers → the response contains `<header`.
- **Acceptance criteria:**
  - [ ] Test passes.
- **Depends on:** none.

### - [ ] CR-46 — Draft form: do not post on text-input `change`
- **Problem:** blur on chapter-title inputs posts and swaps the form, which loses focus and races the merge buttons.
- **Affected files:** `internal/ui/templates/draft.html:20-43`; `internal/ui/static/app.js`.
- **Expected outcome:** only the rule select and the checkboxes trigger live updates; title edits are submitted explicitly.
- **Suggested implementation:** a delegated `change` listener in `app.js` that calls `htmx.trigger(form, "selection-change")` only for `select` and checkbox targets; set `hx-trigger="selection-change"`; give inputs stable `id`s.
- **Required regression tests:** browser test: tabbing between titles keeps focus and value; title edit + merge → N−1 chapters with the edited title.
- **Acceptance criteria:**
  - [ ] Manual check recorded.
- **Depends on:** CR-20 (shared `app.js` changes).

### - [ ] CR-47 — Copy button restores its original label
- **Problem:** a second click within 1.5 s saves "کپی شد" as the original label.
- **Affected files:** `internal/ui/static/app.js:5,14`.
- **Expected outcome:** the label always returns to the original.
- **Suggested implementation:** `btn.dataset.label ??= btn.textContent;` and restore from `dataset.label`.
- **Required regression tests:** a browser or DOM test with two quick clicks.
- **Acceptance criteria:**
  - [ ] Manual check.
- **Depends on:** none.

### - [ ] CR-48 — Add CORP and remove the side effect from `GET /settings`
- **Problem:** without `Cross-Origin-Resource-Policy`, cross-site pages can embed `/files/{id}`; a cross-site `GET /settings` spawns the AI probe.
- **Affected files:** `internal/httpserver/middleware.go:57-62`; `internal/httpserver/handlers_settings.go:62-95`.
- **Expected outcome:** cross-origin subresource loads are blocked; the probe runs only on an explicit POST (or same-origin navigation).
- **Suggested implementation:** set `Cross-Origin-Resource-Policy: same-origin`; refuse GETs with `Sec-Fetch-Site: cross-site` unless `Sec-Fetch-Dest: document`; show the "run doctor" button instead of auto-probing.
- **Required regression tests:** header assertion; a cross-site `GET /settings` does not call `Probe` (spy adapter).
- **Acceptance criteria:**
  - [ ] Tests pass.
  - [ ] The settings page still shows the cached result.
- **Depends on:** do together with CR-12.

### - [ ] CR-49 — Ignore `?tab=` on `/topics/{id}`
- **Problem:** a manual `?tab=` renders a tab without its data.
- **Affected files:** `internal/httpserver/handlers_panel.go:59`.
- **Expected outcome:** `showTopic` always renders the lesson tab.
- **Suggested implementation:** pass `""` instead of `tabOf(r)`.
- **Required regression tests:** `GET /topics/1?tab=reports` → lesson tab content.
- **Acceptance criteria:**
  - [ ] Test passes.
- **Depends on:** none.

### - [ ] CR-50 — Rebuild validates `report_file` and warns instead of aborting
- **Problem:** `report_file` from disk is joined without validation; some inconsistencies (and unsupported folder names) abort the whole rebuild.
- **Affected files:** `internal/services/rebuild/rebuild.go:574-581` (and the folder walk); `internal/schema/result.schema.json:63`.
- **Expected outcome:** bad entries are skipped with warnings, and rebuild completes.
- **Suggested implementation:** require `report_file == path.Base(report_file)` and `fsx.ValidateSegment`; add a `pattern` to the schema; turn "exercise not in manifest" into a warning; skip folders that fail `ValidateRelPath` (shared helper with CR-04).
- **Required regression tests:** `report_file: "../x.md"` → a warning; a folder named `x?` → a warning; rebuild succeeds.
- **Acceptance criteria:**
  - [ ] Tests pass.
- **Depends on:** CR-04.

### - [ ] CR-51 — Fold invisible format characters in `Normalize`
- **Problem:** ZWSP, ZWJ, LRM/RLM and embedded BOM change stable keys and slip past the title checks.
- **Affected files:** `internal/roadmap/stablekey.go:15-34`; `internal/roadmap/parse.go:160`; `internal/roadmap/chapters.go` (empty-title check).
- **Expected outcome:** visually identical titles get identical keys.
- **Suggested implementation:** drop `unicode.Is(unicode.Cf, r)` characters except ZWNJ. On the next re-import, match old keys by recomputing them with the legacy normaliser, to avoid archiving existing topics.
- **Required regression tests:** `Normalize("Go\u200fLang") == Normalize("GoLang")`; a chapter title made only of U+200B is rejected; a re-import of an existing roadmap whose titles contain RLM archives nothing.
- **Acceptance criteria:**
  - [ ] Golden tests are updated deliberately, with a note in `PROGRESS.md`.
- **Depends on:** none.

### - [ ] CR-52 — Sync: guard updates, re-verify moves, ignore empty-file matches
- **Problem:** Scan writes stale observations unconditionally; ApplyMoves does not re-check hashes; empty files can match as moves.
- **Affected files:** `internal/services/syncsvc/syncsvc.go:133-144,226-260,381-390`.
- **Expected outcome:** no stale overwrite, no move applied to a changed file, no empty-file matching.
- **Suggested implementation:** `UPDATE … WHERE id=? AND rel_path=? AND sha256=?`; recompute the target hash (or header) in ApplyMoves; skip `sha256("")` in matching.
- **Required regression tests:** change the target between Scan and Apply → refused; an empty missing file and an empty new file → no proposal.
- **Acceptance criteria:**
  - [ ] Tests pass.
- **Depends on:** CR-04.

### - [ ] CR-53 — One timestamp format per attempt
- **Problem:** `started_at` has millisecond precision; `finished_at` uses RFC 3339 seconds.
- **Affected files:** `internal/services/evaluation/commit.go:64-72`.
- **Expected outcome:** both use `2006-01-02T15:04:05.000Z` in UTC.
- **Suggested implementation:** a shared `timefmt` constant used for both values.
- **Required regression tests:** `result.json` timestamps match one regex.
- **Acceptance criteria:**
  - [ ] Test passes.
- **Depends on:** none.

### - [ ] CR-54 — Fix flaky cancel tests in `internal/jobs`
- **Problem:** the tests check work and staging cleanup immediately after the status changes, but cleanup happens after the status write.
- **Affected files:** `internal/jobs/cancel_test.go:74-85,163-172`.
- **Expected outcome:** deterministic tests.
- **Suggested implementation:** poll until the directories are gone, with a 5 s deadline.
- **Required regression tests:** `go test -race -count=200 ./internal/jobs` green.
- **Acceptance criteria:**
  - [ ] 200 iterations pass.
- **Depends on:** none.

### - [x] CR-55 — gofmt, dead code and the reserved-name list in `fsx`
- **Problem:** three files are not gofmt-clean; `validSegmentRegex` is unused; there is a no-op `if`; the reserved-name list is incomplete.
- **Affected files:** `internal/fsx/fsx_test.go`, `internal/fsx/root.go`, `internal/fsx/safename.go:25-63`; `Makefile` (test target).
- **Expected outcome:** clean formatting, no dead code, full reserved list (`CONIN$`, `CONOUT$`, `COM0`, `LPT0`, superscript ¹²³ variants).
- **Suggested implementation:** `gofmt -w`; delete the dead code; extend the list; add `test -z "$(gofmt -l .)"` to `make test`.
- **Required regression tests:** a reserved-name table test; the `make test` gofmt gate.
- **Acceptance criteria:**
  - [x] `gofmt -l .` is empty.
  - [x] Tests pass.
- **Depends on:** none.

### - [ ] CR-56 — Reproducible release artifacts
- **Problem:** `dist/hub` and `dist/hub.exe` were built from `ceae190+dirty` (T6.8), not HEAD; `dist/INSTALL.md` is outdated.
- **Affected files:** `Makefile` (release target), `cmd/hub/main.go` (`version` output), `dist/`.
- **Expected outcome:** release binaries always match a clean commit, and `hub version` prints the revision.
- **Suggested implementation:** in `make release`, fail if `git status --porcelain` is non-empty; print `debug.ReadBuildInfo()` `vcs.revision`/`vcs.modified` in `hub version`; rebuild `dist/` after the Phase 1 fixes.
- **Required regression tests:** a release-script check that `go version -m dist/hub` shows no `+dirty` and the revision equals `git rev-parse HEAD`.
- **Acceptance criteria:**
  - [ ] `dist/` is rebuilt from a clean tree.
  - [ ] `diff INSTALL.md dist/INSTALL.md` is empty.
- **Depends on:** Phase 1 (CR-01 … CR-04) completed first.

### - [ ] CR-57 — Update outdated and contradictory documentation
- **Problem:** `AGENTS.md`/`GEMINI.md` are Antigravity-only and do not point to open review findings; `implementation-strategy-report.md` describes 6 phases and only `agy`; `skills/answer-evaluator/SKILL.md:7,20` names `inputs/answer`; the TDD describes journal recovery and tmp+link writes as if implemented; `PROGRESS.md` says "complete / 100% green" while acceptance items are open; there is no recovery runbook.
- **Affected files:** `AGENTS.md`, `GEMINI.md`, `PROGRESS.md`, `implementation-strategy-report.md`, `technical-design-document.md`, `skills/answer-evaluator/SKILL.md`, `INSTALL.md`.
- **Expected outcome:** the docs match the code, and the continuity protocol includes "read `CODE_REVIEW_TODO.md` for open findings".
- **Suggested implementation:**
  - Edit each file.
  - Bump the `answer-evaluator` skill version. Note that `skills.Install` never overwrites user copies, so document how users get the update.
  - Add a short "Recovering from a stuck commit" section to `INSTALL.md` until CR-03/CR-05 ship.
- **Required regression tests:** `skills_test`: every `inputs/...` path mentioned in a SKILL.md exists in the composer whitelist.
- **Acceptance criteria:**
  - [ ] Docs reviewed.
  - [ ] The skills test passes.
- **Depends on:** decisions in CR-03, CR-07, CR-41.

### - [ ] CR-58 — Close the critical test gaps and add CI
- **Problem:**
  - No tests for: crash recovery, suggestion → exercises, misbehaving children, hostile file names, pathological uploads, config edge cases, `cmd/hub`, SIGHUP, htmx error paths.
  - No Windows runtime tests and no CI configuration in the repository.
  - `config` (44.9 %), `ai` (53.2 %) and `topics` (54.5 %) have low coverage.
- **Affected files:** test files across the packages above; a new CI workflow (e.g. `.github/workflows/ci.yml` or the equivalent for your host); `Makefile`.
- **Expected outcome:** a CI matrix (ubuntu, windows) running `vet`, `gofmt`, `test -race`, `staticcheck`, `govulncheck`; regression tests for every CR task.
- **Suggested implementation:**
  - Add the tests listed in each task.
  - Add a crash-injection hook (an unexported package variable called right before `WithTx` in each commit) for CR-03/CR-05 tests.
  - Windows jobs for CR-13, CR-14, CR-24.
  - Add `make lint` (gofmt + staticcheck) and `make vuln` to CI.
- **Required regression tests:** this task is the test suite itself.
- **Acceptance criteria:**
  - [ ] CI is green on Linux and Windows.
  - [ ] Coverage ≥ 70 % for `config`, `ai`, `topics`.
  - [ ] Every CR task's tests are in CI.
- **Depends on:** CR-21 (for a green govulncheck). Grows with each task.

### - [x] CR-59 — Provide `favicon.ico` or 204 handler to clear browser console 404
- **Problem:** Browser requests `/favicon.ico` and receives 404 Not Found, logging an error in Chrome Developer Tools console.
- **Affected files:** `internal/httpserver/server.go`, `internal/ui/templates/layout.html`, or `internal/ui/static/`.
- **Expected outcome:** Browsers loading any page encounter zero 404 console errors for favicon.
- **Acceptance criteria:**
  - [x] Loading `/favicon.ico` returns 200 (with valid SVG/ICO) or 204 No Content.
  - [x] Chrome console logs 0 errors on clean load.

### - [x] CR-60 — Improve search empty state UX and group title matching on `/topics`
- **Problem:**
  - When `?q=` matches 0 topics, the page outputs `0 موضوع یافت شد` and displays the "ورود نقشه راه جدید" (New Roadmap) button as if the library is empty, confusing users.
  - Search only queries `topics.title_norm`; it does not match roadmap group/phase names (e.g. searching "Recursion" misses topics inside "Phase 1 — Math, Complexity & Recursion").
- **Affected files:** `internal/services/topics/list.go`, `internal/ui/templates/topics.html`.
- **Expected outcome:** Clear "هیچ موضوعی با این مشخصات یافت نشد" message when filtered count is 0 while total topics > 0; optionally match group names.
- **Acceptance criteria:**
  - [x] `topics.html` renders a distinct empty search result message instead of the initial setup state.
  - [x] Test covers empty search result display.

### - [x] CR-61 — BiDi isolation and formatting in Persian RTL interface
- **Problem:** Multiple places where English identifiers, technical flags, and punctuation are mixed with Persian text produce visual scrambling:
  - Outdated skills list in `settings.html`: `:chapter-detector 1.0.0 ← 2.0.0`
  - Technical flags in `settings.html`: inverted parentheses `( --json-schema )` and broken `--dangerously-skip-permissions`
  - Draft preview heading in `draft.html`: `persian.md :پیش‌نمایش ورود`
  - Privacy ack date in `settings.html`: raw UTC ISO timestamp instead of localized date
  - Markdown bullet points with operators in `source.md`: `var و :=` reordered as `=: و var`
- **Affected files:** `internal/ui/templates/settings.html`, `internal/ui/templates/draft.html`, `internal/ui/static/app.css`, `internal/render/render.go`.
- **Expected outcome:** English tokens and punctuation isolated with `<bdi>` or `dir="ltr"` and formatted correctly.
- **Acceptance criteria:**
  - [x] All English/technical phrases in Persian text use `<bdi>` or `dir="ltr"`.
  - [x] Visual verification in Chrome shows no inverted punctuation or scrambled token orders.

### - [x] CR-62 — Mobile responsive layout fixes for `/settings` (390px)
- **Problem:** At 390px viewport width, the AI adapter `<select>` and "ذخیره" submit button wrap onto separate lines awkwardly, and definition list `<dl>` elements have irregular alignment.
- **Affected files:** `internal/ui/static/app.css`, `internal/ui/templates/settings.html`.
- **Expected outcome:** Clean vertical or horizontal stacking without awkward overflow or misaligned keys.
- **Acceptance criteria:**
  - [x] Settings page renders smoothly at 390px width without awkward wraps.

---

## INFO — Optional improvements (not defects)

- [ ] **CR-I1** — Remove the port-less `Origin` entries; optionally require the configured port in the Host check (`internal/httpserver/middleware.go:47-54,88-93`).
- [ ] **CR-I2** — Parse the form (or check `Content-Length`) on the header-CSRF path so oversized htmx bodies return 413 (`middleware.go:112-123`).
- [ ] **CR-I3** — Document why `ReadTimeout`/`WriteTimeout` are unset (`internal/httpserver/server.go:68-73`).
- [ ] **CR-I4** — Stream `serveFile` with `http.ServeContent`; give `/static/` a short `max-age` (`handlers_exercises.go:232`).
- [ ] **CR-I5** — State in the TDD that each report embeds that exercise's rubric criteria (`evaluation/report.go:96-98`).
- [ ] **CR-I6** — Add `CREATE INDEX idx_jobs_scope ON jobs(scope_key, id)`; order `claim` by `id` (`jobs/view.go:61`, `runner.go:263`).
- [ ] **CR-I7** — Check the error from the deferred `PRAGMA foreign_keys = ON` and fail `Open` on error (`store/db.go:150`).
- [ ] **CR-I8** — Join the maintenance goroutine before `db.Close` (`cmd/hub/main.go:325-353`).
- [ ] **CR-I9** — Document environment inheritance (`ANTHROPIC_API_KEY`, `CLAUDECODE`) in `INSTALL.md`.
- [ ] **CR-I10** — Re-check the prompt size after appending the retry note; align `>` vs `>=`; require a non-empty `Schema` in `Compose` (`ai/retry.go`, `ai/prompt/composer.go:82`).
- [ ] **CR-I11** — Search for the answer marker only after the header lines (`fsx/answertpl.go:78-92`).
- [ ] **CR-I12** — Put the analyze commit's two `UPDATE`s in one transaction (`services/roadmaps/analyze.go:172-184`).
- [ ] **CR-I13** — Keep the phrase-list injection guard as an accepted risk (no action; tracked for visibility).
- [ ] **CR-I14** — Verify Windows console-close shutdown in the manual checklist (`INSTALL.md`).

---

## Dependency Overview

```
CR-06 ─┬─> CR-05
       └─> CR-19 <── CR-18
CR-07 ──> CR-34          CR-03 ──> CR-33
CR-04 ──> CR-50, CR-52   CR-31 ──> CR-42
CR-02 ──> CR-27 ──> CR-29;  CR-02 ↔ CR-22 (same kill path)
CR-12 ↔ CR-48 (do together)   CR-20 ──> CR-46
CR-44 ──> CR-23, CR-39   CR-40 ──> CR-41
CR-09 ──> CR-35          CR-21 ──> CR-58 (govulncheck in CI)
CR-58 ──> CR-13, CR-24 (Windows verification)
CR-01..CR-04 ──> CR-56 (rebuild dist after Phase 1)
CR-03, CR-07, CR-41 ──> CR-57 (docs reflect decisions)
```
