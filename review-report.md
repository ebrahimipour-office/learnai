# Review Report: Work Completed So Far

**Date:** 2026-11-01
**Scope:** everything up to `e2c4fc4`. That is Phase 1, Phase 2 and T3.1. Two commits are new since the last review:
- `3258cb4`: T2.7, re-import with a diff report.
- `e2c4fc4`: T3.1, the `ai.Adapter` interface and the `antigravity` stream-json runner.

**Earlier reports:** this replaces the review up to `09a10b4` (commit `d794ec7`). Full details of older findings are in `git show d794ec7:review-report.md` (R-series and P1), `git show 397815e:review-report.md` (S, M, L, N and H series) and `git show 25a314f:review-report.md` (older detail).
- Only `services/roadmaps`, `httpserver`, `ui` and the new `internal/ai` changed since then.
- `cmd/hub`, `store`, `config`, `fsx`, `roadmap`, `schema` and `services/topics` are untouched, so every earlier finding in them is still open as described.

**Naming:** new findings are **I1–I9** (re-import, T2.7) and **A1–A13** (AI adapter, T3.1). They don't reuse the older N-series numbers.

**Method:**
- All of these pass: `go vet ./...`, `go test -race -count=1 ./...`, `CGO_ENABLED=0 go test -count=1 ./...`, `CGO_ENABLED=0 GOOS=windows go build ./...`, `GOOS=windows go vet ./internal/ai/...` and `GOOS=windows go test -c ./internal/ai/antigravity`. The only exception is `gofmt -l`, which still flags the same 6 older files (L9).
- Two separate reviewers, neither of whom wrote the code under review, each read one commit against the design doc. That covered T2.7 against the T2.7 row, R2.5, R11.2-1, RK8, §3.4 and §7, and T3.1 against §6.1, §6.2, §6.5, §6.7, the T3.1 row and RK1/RK2.
  - **How findings were confirmed:** with throwaway probe tests in isolated worktrees, all since deleted. These included a fake `agy` that leaves a background child holding stdout, and timing runs of the fuzzy guess on 500×500 and 2000×2000 topics.
  - **What I re-checked myself:** A1 (by reading `adapter.go`) and I1 (rule loaded and stored unchanged, `drafts.go:102-109`, `confirm.go:57,94`).
  - **Why reviewers were used:** I wrote T3.1 earlier in this session, which is why it was reviewed by someone else.
- The real `agy` 1.4.12 was run against a scratch schema. It still fails with `Eligibility check failed: ... not currently available in your location`, which gave the only real stream-json line we have.

---

## 1. Verdict

**Re-import (T2.7) works for the simple case and is safe on disk.**
- It runs in one transaction.
- Row counts never go down, and existing files are never touched. R11.2-1 is tested.
- Archived topics come back when they reappear, and concurrent confirms serialize.

**But the topic-identity problems the last report said to fix "before T2.7" are now live:**
- **I1 (High; R11 is now reachable):** adding **one deeper heading** to a roadmap archives every existing topic and creates new ones. Their folders, lessons, answers and status are disconnected from the active topic.
- **Old N1 (High):** confirmed again. It now archives a topic and recreates it under a new folder.
- **I2 (Medium):** removing the earlier of two same-titled topics moves its folder and history onto the later one.
- **R3 (Medium):** now reachable through re-import too.

Nothing real has been imported yet, so fixing all of these together is still cheap.

**The AI adapter (T3.1) matches the §6.1 contract in shape and passes its own tests, but it is not ready to build the job runner on:**
- **A1 (High):** if `agy` leaves any background process holding stdout, a successful run is reported as a timeout after Timeout + 35s. With no timeout set, it hangs forever.
- **A2 (Medium):** `ExtraArgs` can still grant permissions through `--settings`, or turn the sandbox off with `--sandbox=false`.
- **A3–A6 (Medium):** some errors are mapped to the wrong code. A context that is already cancelled is reported as `tool_error`. Loose patterns produce false matches. The timeout rule overrides explicit errors. Parallel runs collide on log folders.

**Process:** P1 happened again, twice.
- T2.7 was built while the "before T2.7" items were open.
- T3.1 was built in this session without reading this report.
- `PROGRESS.md` still has no pointer to the "Recommended order" below (§5).

| Severity | Earlier, still open | New in this round |
|---|---|---|
| Blocker | 1 (B1: partly unblocked, see §4) | 0 |
| High | 3 (H1, H2, old N1, which is now user-visible) | 2 (I1 = R11 escalated, A1) |
| Medium | 11 (M2–M4, N2, N3, S1–S3, R1, R3, R4) | 7 (I2, I3, A2–A6) |
| Low | 30 (L1–L6, L8–L10, N4–N6, S4–S10, R2, R5–R10, R12, R13, R15, R16) | 11 (I4–I8, A7–A12) + spec gaps I9, A13 + process P1 (again) |
| Fixed since last report | none | — |

---

## 2. PROGRESS.md claims vs reality

| Task | Claimed | Actual |
|---|---|---|
| **T1.1** agy feasibility | ✅ | ❌ Still not done (B1). The account is location-blocked on 1.4.12 as well. |
| **T1.5** CLI skeleton | ✅ | ⚠️ H2 and S1 are still open. |
| **T1.8** SQLite store | ✅ | ⚠️ The reader pool is still missing (H1). T2.7 avoids the deadlock (checked). |
| **T2.2 / T2.3** | ✅ | ⚠️ Old N1, N2 and N3 are still open. Re-import now matches on these keys (I1, I2). |
| **T2.5** confirm | ✅ | ⚠️ R3, R4 and R6 are still open. R3 can now also happen through re-import. |
| **T2.7** re-import | ✅ | ⚠️ The acceptance criteria are met and R11.2-1 is tested. I1 (High), I2 and I3 are open. |
| **Phase 2 complete** | ✅ | ⚠️ Spec gap I9: `/roadmaps`, `/roadmaps/{id}` (revision history, "ورود نسخه جدید") and topic archive/unarchive (§7) don't exist. |
| **T3.1** adapter | ✅, with "real run open" noted | ⚠️ The required real manual run has not happened (B1). PROGRESS is honest about that, but the checkbox should stay unticked. A1 (High) and A2–A6 are open. It says adapter-owned flags are blocked, but `--sandbox` is not (A2). |

---

## 3. New findings

### Process

**P1 (again). Review findings were not read before building on top of them.**
- **What happened:**
  - The last report's §5.1 said to fix old N1, R3, R6, R11 and R4 *before T2.7*. T2.7 was built anyway, and three of those findings are now reachable through it: I1 (R11), old N1 and R3.
  - T3.1 was built in this session after reading only `PROGRESS.md`. B1 was settled in chat: the user approved building against a fake `agy` first. But the report itself was never opened.
- **Cause:** unchanged. The continuity protocol in `AGENTS.md` and `PROGRESS.md` only points at `PROGRESS.md`.
- **Fix:**
  - Add a step to the continuity protocol in both files: "read `review-report.md` §5 Recommended order".
  - Add a `PROGRESS.md` row listing open High findings.

### High

**I1. A deeper heading in a new revision archives every recorded topic.** *(This is R11 becoming reachable. Confirmed by probe, and the code was re-read.)*
- **Input:**
  - v1 is `# Go / ## Basics / ### Types / ### Loops`.
  - v2 adds `#### zero values` under Types.
- **What happens:**
  - The re-import draft starts with the stored rule `{"kind":"deepest"}`, which now resolves to level 4.
  - The diff shows 1 new (zero values), 2 gone (Types, Loops) and 0 unchanged.
  - After Confirm with the default ticks, `01-types` and `02-loops` are archived and `03-zero-values` is active. Loops has no H4 under it, so it disappears from the selection completely.
  - The only safeguard is the user noticing and switching the rule to level 3.
- **Where:** `internal/services/roadmaps/drafts.go:102-109` (rule loaded unchanged), `confirm.go:57,81,94` (stored unchanged)
- **Fix:**
  - At confirm, store the resolved `{"kind":"level","level":N}`.
  - Migrate existing rows from `roadmap_revisions.parsed_tree`.
  - Optionally, warn in the preview when the stored level and the resolved level differ.

**A1. A background process holding stdout turns a successful run into a timeout, or a hang.** *(Confirmed by probe and by reading.)*
- **Probe:** the fake `agy` ran `sleep 6 &`, printed a SUCCESS result and exited 0 straight away. With Timeout 500ms, `Run` returned after 5.5s with `timeout: killed after 501ms` and threw away the valid result.
- **Cause:**
  - `readStream` reads stdout until EOF *before* calling `cmd.Wait` (`adapter.go:166-167`). EOF only arrives when every process holding the pipe exits.
  - `WaitDelay` only applies inside `Wait`, which hasn't been called yet.
  - Then `outcome` checks `runCtx.Err()` before looking at the result (`adapter.go:228`).
- **With real defaults:** every such run waits Timeout + 30s + 5s and is reported as `timeout`. With `Timeout=0` it never returns.
- **Why it's likely:** `agy` has a `remote-control` daemon and may start helper processes.
- **Fix** (fits naturally into T3.2):
  - Stop reading once the `result` event arrives, or have `Wait` own the pipe through `cmd.Stdout = writer`.
  - Accept a SUCCESS result before checking `runCtx`.

### Medium

**I2. Removing an earlier duplicate title moves its folder and history onto the later duplicate.** *(Confirmed by probe.)*
- **Input:**
  - v1 has `## G`, then `### x` (outline "- first"), then `### x` (outline "- second").
  - v2 drops the first x.
- **What happens:**
  - The remaining x now gets key `G\nx`, which used to be the first topic's key.
  - `01-x`, the first topic's folder and history, gets outline "- second". `02-x`, the real second topic, is archived.
  - The diff shows "x changed", with an empty explanation, and "x gone". The user can't tell which x is which.
- **Cause:** `#N` keys are positional (by design, T2.3), and `diffTopics` trusts them (`diff.go:72-110`).
- **Fix:** when a `#N` key would match, prefer the candidate with an exact outline match, or at least flag duplicate-title rows in the diff.

**I3. The fuzzy rename guess is quadratic and runs on every render and every htmx tick.** *(Confirmed by measurement.)*
- **Cost:** new × gone × title-length², with allocations on every call. It is recomputed in `renderDraft` on each page load and checkbox toggle.
- **Measured, without `-race`, titles of about 40 runes:**
  - 500 × 500 takes **2.4s per render**.
  - 2000 × 2000 takes **43s**.
- **When it happens:** naturally, when the root heading is renamed (every key changes) and with I1. A 1 MB file allows far more topics than that.
- **Where:** `diff.go:101-147`, `handlers_roadmaps.go:246`
- **Fix:** skip guessing above a pair budget, or for very different lengths. Or compute it once per draft and cache it in the unused `import_drafts.diff` column.

**A2. `ExtraArgs` can get around the forbidden-flag list.** *(Confirmed by probe.)*
- **What gets through:** `buildArgs` accepts all of these:
  - `--settings={"permissions":…}`, or `--settings` followed by its value as the next element. This can grant exactly what the ban on `--dangerously-skip-permissions` exists to prevent, which defeats §6.2's "rejected even via extra_args".
  - `--sandbox=false`, which overrides the adapter's own `--sandbox` (the last flag wins).
  - `--log-file=<any path>`, which writes outside `fsx`.
  - `--remote-control`, `--agent=…`, `--project` and `--new-project`.
- **Where:** `internal/ai/antigravity/args.go:13-33`
- **Fix:** switch to an **allowlist**, for example only `--disable-slash-commands`. A deny-list will fall behind every new `agy` release.

**A3. A context that is already cancelled or expired before `Run` is reported as `tool_error`.** *(Confirmed by probe.)*
- **What happens:** the result is `tool failed: start: context canceled`. The §6.1 contract says cancelling returns `ErrCanceled` or `ErrTimeout`, and `tool_error` is auto-retried once.
- **Side effect:** the schema file and the log folder are written before the cancellation is noticed.
- **Where:** `adapter.go:162-164`
- **Fix:** check `ctx.Err()` at the top of `Run` and when `Start` fails, and map it the same way `outcome` does.

**A4. Error patterns give false positives.** *(Confirmed by probe.)*
- **What happens:** `classify` looks for substrings in the whole of stderr.
  - A crash trace that mentions `quota_client.go` is reported as `rate_limited`.
  - A path containing `please log in` is reported as `auth_required`.
- **Why it matters:** both of those codes are never retried, while the real crash (`tool_error`) would have been retried once.
- **Where:** `stream.go:63-83`, with the text built at `adapter.go:237-240`
- **Fix:**
  - Classify `res.Error` first.
  - Use stderr only when there is no result, and then match only the `error:` line or the first line.

**A5. The "failed after Timeout, so it was a timeout" rule overrides explicit errors.** *(Confirmed by probe.)*
- **What happens:** if `agy` reports `not authenticated`, or a rate limit after its own retries, once `Timeout` has passed, the run is returned as `timeout`.
- **Where:** `adapter.go:234-236`
- **Fix:** run the patterns on `res.Error` first. Apply the timeout rule only when nothing matches, or when `finished_reason` says timeout.

**A6. Parallel runs can collide on the log folder.** *(The failure path is confirmed by reading. The Windows clock part is plausible.)*
- **The cause:** the folder name is `<UTC to the nanosecond>-<task>` (`adapter.go:268-269`). On Windows the wall clock only moves in steps of about 0.5–15.6ms.
- **What happens:** two parallel `evaluate` runs for one job (§6.5 allows up to 3) can get the same folder. The second run's `WriteNew prompt.txt` then fails (`adapter.go:277-278`), so that run fails as `tool_error` before `agy` even starts.
- **Fix:** add a random suffix or the step key, or create the folder with an exclusive mkdir and retry.

### Low

| # | Finding | Status | Location |
|---|---|---|---|
| I4 | The group-archive query archives every group not used by kept *roadmap* topics. A group that still holds an active `suggested` topic is archived anyway. Latent until T6.1. Fix: add `AND NOT EXISTS (… state='active')`. | Confirmed (probe) | `confirm.go:131-135` |
| I5 | Re-import renumbers roadmap topics' `ord` to 1..n but leaves suggested topics' REAL `ord` alone, so accepted suggestions drift away from the topic they belong next to (T6.1). Fix: number in steps of 10, or place suggested topics relative to their source. | Plausible (latent) | `confirm.go:159-161` |
| I6 | A topic the user archived by hand comes back on any re-import that contains it, shown as "بازگردانی" and ticked by default. The archive route doesn't exist yet (I9). Decide whether a manual archive should stick. | Confirmed (probe) | `diff.go`, `confirm.go` |
| I7 | Two drafts of one roadmap, made from the same base, can both be confirmed. The second archives what the first added, though its preview showed nothing gone. The data stays consistent (rev-002 and rev-003). Fix: store the base revision in the draft and re-preview if it changed. | Confirmed (probe) | `confirm.go` |
| I8 | A "changed" row caused only by an outline change has an empty explanation cell. When several guesses tie, the last one wins (`>=`). | Confirmed | `draft.html:39`, `diff.go:122` |
| A7 | Masking misses `"client_secret":"…"` (the `\b` before `secret` fails after `_`), camelCase `accessToken`, escaped JSON inside events (`{\"token\": …}`) and `ghp_` tokens. | Confirmed (probe) | `stream.go:104` |
| A8 | `meta.json` has no token usage, which §6.2 lists. `cli_version` is empty unless `Probe` ran on the same Adapter instance. | Confirmed | `adapter.go:316-327` |
| A9 | `Probe` can hang past its 30s timeout, because `CombinedOutput` has no `WaitDelay` and a child can hold the pipe (plausible). Its note also drops the error (confirmed), so a network failure looks like "not authenticated". | Mixed | `adapter.go:76,93` |
| A10 | There is no way to return `ErrStalled`. When the T3.5 watchdog cancels the context, the caller gets `ErrCanceled`. Fix: map `context.Cause(ctx)` in `outcome`. | Confirmed (design) | `adapter.go:222-230` |
| A11 | All of stderr is kept in memory, then masked and lower-cased in full. Fix: cap it at about 1 MB. | Confirmed (reading) | `adapter.go:154` |
| A12 | Test gaps: no tests for the `agy`-side timeout branch, a context cancelled before `Run`, an empty `OutputSchema`, a nonzero exit with SUCCESS, masking in `events.ndjson`, or parallel runs for one job. Also, under `-race` each fake-`agy` child sleeps 1s on exit (`GORACE atexit_sleep_ms`), so the package takes 11s instead of 0.5s. Fix: set `GORACE=atexit_sleep_ms=0` for the child. | Confirmed (measured) | `adapter_test.go` |

### Spec gaps

- **I9:** PROGRESS marks Phase 2 complete, but R2.5's routes from §7 are missing:
  - `/roadmaps` and `/roadmaps/{id}`, which hold the revision history and the "ورود نسخه جدید" import-new-version button.
  - `POST /topics/{id}/archive` and `/unarchive`.

  Re-import is offered only as a picker on `/roadmaps/new` (`server.go:23-34`).
- **A13:** some T3.1 claims are ahead of the code:
  - The real manual run is missing.
  - `--sandbox` is described as adapter-owned but isn't blocked.
  - §6.2's rejection of unknown model names is deferred to T3.9.
  - The success envelope, the `structured_output` field name, the `init` and `step_update` event names, and the `agy models` output format are all assumptions (RK1). The code marks them with `shortcut:` comments.
  - A `structured_output` sent as a JSON *string* would be passed through as a string.
- **Following the spec, but worth knowing:** moving a topic to another group changes its key. The probe archived `01-x` and created `03-x`. This matches T2.3 and RK8 ("accepted"), but the user loses history.

### Checked and found correct

**T2.7:**
- **Upload form:**
  - The `roadmap_id` picker works, and so does `?roadmap_id=` preselection.
  - A nonexistent, non-numeric or ≤0 id gives 422.
  - No new routes were added, so the existing CSRF middleware covers everything.
- **Draft and preview:**
  - The draft starts with the roadmap's stored rule.
  - The diff sits inside `draft-form`, so htmx refreshes it together with the rule and ticks.
  - The RK8 warning is shown, and titles and guesses are escaped inside `<bdi>`.
  - The guess is Levenshtein on runes over `title_norm`, with a threshold of ≥ 0.6, and it is only a suggestion.
- **Confirm runs in one transaction:**
  - **Revision file:** the rev number is `MAX(rev_no)+1`, computed inside the transaction. `rev-NNN.md` is written last with `WriteNew` and removed if the commit fails.
  - **Rows updated:** `select_rule` is updated, and groups are upserted with `ON CONFLICT … state='active'`.
  - **Same-key topics** are updated in place (title, title_norm, outline, ord, group, `source_rev_id`, state). `dir_name` and `stable_key` are never changed, so the UNIQUE constraints can't fire.
  - **Archived topics that reappear** are reactivated. This was checked with a v1→v2→v1 round trip.
  - **Only `origin='roadmap'` topics are archived**, and nothing is deleted.
- **Consistency and locking:**
  - The diff and Confirm agree: both use `keptTopics` on the same choice, and Confirm recomputes it inside the transaction.
  - **H1 is not triggered:** inside `WithTx` only `tx` is used, and `Diff` and `CreateReimport` use `Writer()` outside any transaction.
  - Concurrent re-import confirms serialize (rev-002, then rev-003).
- **Rules:**
  - The R11.2-1 test checks that row counts in the four tables never go down, and that every file under `topics/` and `roadmaps/` keeps its hash.
  - No CGO, and all file access goes through `fsx`.

**T3.1:**
- **Building the command:**
  - Arguments are passed as an array, with no shell.
  - The base command matches §6.2, and `cwd` is `WorkDir`, which must be absolute.
  - The model can't start with `-`, and the effort level is checked against `agy`'s allowed values.
  - `--print-timeout` uses Go duration syntax, which the real `agy` accepts.
- **Forbidden-flag matching:**
  - It handles `-x`, `--x`, `=value`, surrounding whitespace and two-element forms.
  - Any element that contains `dangerously-skip-permissions` is rejected.
- **Error mapping:**
  - `ai.Code` matches the §6.1 table.
  - Parent cancel gives `ErrCanceled`, a parent deadline gives `ErrTimeout`, and our own kill gives `ErrTimeout`.
  - A nonzero exit with a SUCCESS result gives `tool_error`, and SUCCESS with no structured output gives `invalid_output`.
- **Process handling:**
  - `ProcessState.ExitCode()` is safe when `ProcessState` is nil.
  - `bufio.Reader.ReadBytes` has no line-length limit.
  - A nil events channel is handled.
  - A send to a slow consumer gives up when the context ends, so it can't deadlock.
- **Logs:**
  - Paths have no colons and stay under 220 characters.
  - Files are written only through `fsx` (`MkdirAll`, `WriteNew`, an O_EXCL `OpenFile`), and they are masked.
  - A failure while writing the logs doesn't hide the run's real error.
- **Schema and Probe:**
  - `writeSchema` writes through `fsx` into the sandbox, which is not a protected user file.
  - `Probe` reports `Authenticated=false` for an ineligible account, and its check matches the real `--help` text.
- **Tests and dependencies:**
  - The fake `agy` (the test binary itself) should also work on Windows.
  - The timing checks are generous, with a 10s upper bound.
  - `internal/ai` doesn't import `store` or `domain`.

---

## 4. Earlier findings: status

| # | Finding | Status |
|---|---|---|
| **B1** | `agy` unusable (account region-blocked). | **Partly unblocked.** The user approved building Phase 3 against a fake `agy` with contract tests. It is still blocked on 1.4.12, so the T3.1 real run and any real use can't happen. |
| **H1** | One DB connection; a read via `db.Writer()` inside `WithTx` deadlocks. | Open, not triggered by T2.7. The Phase 3 job runner will be the heaviest user, so fix it or add a guard before T3.4. |
| **H2** | `DefaultConfigPath()` is never consulted with no `--config` flag. | Open, unchanged (`cmd/hub/main.go:134`). |
| **old N1** | A topic's key changes when a same-named sibling group gains a topic. | **Worse, user-visible.** Probe: `# A / ## Basics (no H3) / ## Basics / ### t1`, then v2 adds `### t0` under the first Basics. After Confirm, `01-t1` is archived and `03-t1` is created, losing its lessons, answers and status. |
| N2 / N3 / N6 | Dropped content, `Normalize` gaps, titles over 200 characters. | Open. Re-import now applies them to existing topics too. |
| **R1** | The Back button can replace `/topics` with a bare fragment. | Open. |
| **R3** | `dir_name` is checked only against the DB. | Open, **newly reachable through re-import.** Probe: a leftover `topics/02-y/answers/q01.go`, and the re-import's new topic `y` got `dir_name=02-y` (`confirm.go:148,173`). |
| **R4** | Confirm reads the choice outside the transaction. | Open (`confirm.go:28-31`). |
| R5 / R8 / R13 | Slug cap in runes, `.md` file name, empty folder after a failed commit. | Open, for new imports only. Re-import reuses the slug and folder. |
| **R6** | One `%02d` counter shared across all roadmaps. | Open, **more visible:** new topics in a re-import get global numbers (a roadmap's 3rd topic can be `03-y` after `02-z`, or `37-x`). |
| **R11** | `select_rule` stores `deepest`, not the level it resolved to. | **Escalated to I1 (High).** |
| R12 | Double-clicking Confirm shows an error. | Open (`handlers_roadmaps.go:225-226`). |
| S1 | `hub serve` exits 0 on a failed start. | Open, unchanged. |
| S2 / S3 / S4 | htmx drops errors; focus is lost; a fast tick can be dropped. | Open. S4 combined with R4 still lets a dropped untick be imported. |
| L9 | `gofmt`. | Open: the same 6 older files. The new files are clean. |
| M2–M4, N4, N5, S5–S10, L1–L6, L8, L10, R2, R7, R9, R10, R14–R16 | See the earlier reports. | Open, unchanged by these commits. |

---

## 5. Recommended order

1. **Topic identity, as one change, before importing any real roadmap.** No real data exists yet, so this is still the cheapest it will ever be.
   - I1/R11: store the resolved level, and warn when the resolved level changes.
   - Old N1: keys must not depend on whether a sibling group has topics.
   - I2: prefer an outline match for duplicate titles.
   - R3 and R6: check the disk for `dir_name`, and choose `%03d` or per-roadmap numbering.
   - R4: read the choice inside the transaction.
   - Fold old N2 and N3 into the same migration.
2. **Adapter fixes before T3.4 builds on them.** This is one small commit, or part of T3.2:
   - A1: read until the result arrives and let `Wait` own the pipe. This sits naturally next to the T3.2 tree kill.
   - A3: check the context before and at `Start`.
   - A4/A5: classify `res.Error` first, and narrow the stderr matching.
   - A2: switch to an allowlist.
   - A6: make log folders unique.
   - A10: map `context.Cause`.
3. **I3:** cap the fuzzy guess, before anyone imports a large roadmap.
4. **H1:** a reader pool, or a guard against queries outside the transaction, before T3.4.
5. **Small htmx fixes, together:** R1, R2, S2, S3/S4 and R12.
6. **H2** (default config path) and **S1** (exit code).
7. **Process (P1):**
   - Add "read `review-report.md` §5" to the continuity protocol in `AGENTS.md` and `PROGRESS.md`.
   - Untick T3.1 until the real run happens.
   - Note I9 under Phase 2.
8. **Low items** (I4–I8, A7–A12 and the older L, N, S and R items) whenever their file is being changed anyway.

---

*Not covered:*
- Nothing was run in a real browser or on real Windows. That rules out checking the Job Object behaviour, the clock resolution behind A6, and 8.3 short paths.
- A successful real `agy` reply has never been seen, so the success envelope, the `agy models` output and how `agy` ends a run when its own `--print-timeout` fires are all unverified. Whether real `agy` starts background children (A1) is also unverified.
- No failure was injected mid-transaction in a re-import specifically. The structure is the same as T2.5's tested all-or-nothing path, and was checked by reading only.
- A rev-file collision after a DB restore was not probed. An existing `rev-00N.md` would make `WriteNew` fail and block re-import.
- Performance was not measured under `-race`, apart from A12.
