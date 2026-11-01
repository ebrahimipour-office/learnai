# Review Report: Work Completed So Far

**Date:** 2026-11-01
**Scope:** everything up to `3e00838`. That is Phase 1, Phase 2 and T3.1–T3.3. Two commits are new since the last review:
- `44f0fdb`: T3.2, `procgroup` (kills the whole process tree).
- `3e00838`: T3.3, `prompt.Composer`, the per-job sandbox and the fake adapter.

**Earlier reports:** this replaces the review up to `e2c4fc4` (commit `8a07973`). Full details of the I-series and A-series findings are in `git show 8a07973:review-report.md`. Older series are in `git show d794ec7:review-report.md` (R-series and P1), `git show 397815e:review-report.md` (S, M, L, N and H series) and `git show 25a314f:review-report.md`.
- Only `internal/ai` (`antigravity`, the new `procgroup`, `prompt` and `fake`) and `internal/fsx/root.go` (new `RemoveAll`) changed.
- Every other package is untouched, so every earlier finding in them is still open as described.

**Naming:** new findings are **G1–G9** (`procgroup`, T3.2) and **C1–C10** (composer and sandbox, T3.3).

**Method:**
- All of these pass at `3e00838`: `go vet ./...`, `go test -race -count=1 ./...`, `CGO_ENABLED=0 go test -count=1 ./...`, `CGO_ENABLED=0 GOOS=windows go build ./...`, `GOOS=windows go vet ./internal/...`, and `GOOS=windows go test -c` for `procgroup` and `prompt`. The only exception is `gofmt -l`, which still flags the same 6 older files (L9).
- I wrote both commits in this session. So two separate reviewers, neither of whom wrote the code, each read one commit against the design doc:
  - T3.2 against the T3.2 row, §6.2, §6.5 and RK6.
  - T3.3 against the T3.3 row, §6.1–§6.3, §6.7, D5, the §3 directory layout and R5.4.
- **How findings were confirmed:** with throwaway probe tests in isolated worktrees, all since deleted. These included:
  - a fake `agy` whose background child holds stdout, with and without `setsid`;
  - a grandchild that needs 500ms to clean up after SIGTERM;
  - a 200-iteration cancel loop under `-race`;
  - a symlinked `.app/work`.
- **What I re-checked myself:** C1 (by my own probe: the user's answer file was deleted and the sandbox written into `topics/42`), A1 (by reading `adapter.go:166-169`), and G1 and G3 (by reading `adapter.go:169` and `procgroup_unix.go:30-45`).
- Real Windows and a real `agy` run were not available (B1).

---

## 1. Verdict

**Both commits do what their T-row asks, and their tests prove it.**
- **T3.2:** cancel kills a child and a grandchild that ignores SIGTERM in about 3s, against a 5s limit. Both tests fail if the kill reaches only the child.
- **T3.3:** the sandbox holds exactly the allowed files, built from byte copies. Real file hashes are unchanged after the `injection` scenario. The security rules appear in `-p` and in `SKILL.md`, and the prompt stays under 4 KB.

**But the AI layer is not ready for T3.4 to build on:**
- **A1 (High), still open:** T3.2 was the place the last report said to fix it, and it wasn't fixed. A valid result is reported as `timeout` whenever a background process holds `agy`'s stdout. With `Timeout=0` the run hangs until the parent context ends. A child started with `setsid` holds the run hostage even past the group kill.
- **C1 (High), new:** if `.app/work` is a symlink to a folder inside the base directory, `Compose` deletes that folder (user answers included) and writes the sandbox into it. `os.Root` only blocks links that leave the root. Planting that link is exactly what a prompt-injected agent with its working folder inside the base directory could do. This turns D5's "copies only" into deletion of user files.
- **G1, G2, C2, C3 (Medium):**
  - G1: on cancel, grandchildren get no SIGTERM grace.
  - G2: a terminal hangup now leaves `agy` running.
  - C2: `fsx.Root.RemoveAll` can delete `topics/`.
  - C3: one sandbox per job, although one job makes several AI calls.

**Process:** P1 happened a third time. T3.2 and T3.3 were both built after reading only `PROGRESS.md`. None of the last report's process fixes were applied:
- there is no pointer to this report in `AGENTS.md` or `PROGRESS.md`;
- T3.1 is still ticked;
- T3.2 was ticked with its manual Windows test still open (G8).

| Severity | Earlier, still open | New in this round |
|---|---|---|
| Blocker | 1 (B1, partly unblocked) | 0 |
| High | 5 (H1, H2, old N1, I1, A1) | 1 (C1) |
| Medium | 18 (M2–M4, N2, N3, S1–S3, R1, R3, R4, I2, I3, A2–A6) | 5 (G1, G2, C2, C3, C4) |
| Low | 41 (L1–L6, L8–L10, N4–N6, S4–S10, R2, R5–R10, R12, R13, R15, R16, I4–I8, A7–A12) | 13 (G3–G9, C5–C10) + process P1 (third time) |
| Fixed since last report | none | — |

---

## 2. PROGRESS.md claims vs reality

| Task | Claimed | Actual |
|---|---|---|
| **T3.1** adapter | ✅, with the real run noted as open | ⚠️ Unchanged since the last report. The checkbox should stay unticked until the real run happens (B1). A1–A12 are all still open. |
| **T3.2** procgroup | ✅, with the manual Windows test noted as open | ⚠️ The automated criteria are met on Linux. The **required** manual Windows test isn't done, and "no orphan after program exit" has no Unix test (G8). Its note only says a *hard crash* orphans `agy` on Unix, but a terminal hangup does too (G2). |
| **T3.3** composer | ✅ | ⚠️ The acceptance criteria are met as written. "Real file hashes unchanged after injection" holds, but the fake can't attempt `../` writes or plant links (`os.Root` confines it). So the test proves "Compose copies, it doesn't link", not "an injected agent can't reach real files". C1 shows a concrete way through. |

---

## 3. New findings

### Process

**P1 (third time). The review report was not read before building.**
- **What happened:**
  - The last report's §5 item 2 said to fix the adapter findings (A1, A3–A6, A10) before T3.4, and named T3.2 as the natural place for A1.
  - T3.2 and T3.3 were both built in one session after reading only `PROGRESS.md`. A1 is still open, and T3.3 adds to a layer whose known defects are unfixed.
- **Cause:** unchanged. The continuity protocol in `AGENTS.md` and `PROGRESS.md` only points at `PROGRESS.md`.
- **Fix:** the same as before, and now urgent. Add "read `review-report.md` §5 Recommended order" as step 1b of the continuity protocol in both files. Add a `PROGRESS.md` row listing open High findings.

### High

**C1. A symlinked `.app/work` makes Compose delete a user folder and write the sandbox into it.** *(Confirmed by two separate probes.)*
- **Input:** `.app/work` is a relative symlink to `../topics`, and `topics/42/answers/v1/keep.go` exists. Then `Compose(JobID 42)` runs.
- **What happens:**
  - `Cleanup` runs `RemoveAll(".app/work/42")`, which follows the link and deletes `topics/42`, the user's answer included.
  - The sandbox (`AGENTS.md`, `SKILL.md`, `inputs/`, `profile.md`) is then written into `topics/42`.
  - The returned `WorkDir` still says `.app/work/42`, so the AI's working folder is really a user folder.
  - My probe: `Compose` returned no error, `keep.go` was gone, and `topics/42` held only the sandbox files.
- **Why the guard misses it:** `os.Root` only refuses symlinks that point *outside* the root, and this one stays inside it. A Windows junction does the same.
- **Who can plant the link:** anything that can write to the base directory, including an agent that was prompt-injected in an earlier job, because its working folder is inside the base directory. If `.app/work/42` itself is the link, only the link is removed, which is harmless.
- **Where:** `internal/ai/prompt/composer.go:84-92,110`, `internal/fsx/root.go:168-179`
- **Fix:**
  - Lstat `.app` and `.app/work` and refuse anything that isn't a real directory.
  - Open a dedicated `os.Root` on `.app/work` and do the removal and the writes through it, using only `<id>`.
  - Add the probe as a test.

**A1 (still open, re-confirmed after T3.2). A background process holding stdout turns a successful run into a timeout, or a hang.**
- **Probe at `44f0fdb`:** the fake `agy` starts `sleep 6` sharing its stdout, prints a SUCCESS result and exits 0.
  - **Timeout 500ms:** `Run` returns after 510ms with `timeout: killed after 501ms`, although the result had been parsed. This used to take 5.5s; the group kill now ends it sooner, but it's still reported as a timeout.
  - **Timeout 0:** `Run` returns only when the parent context ends. With no parent deadline it hangs forever.
  - **Timeout 20s:** it succeeds, but only after 6s, the lifetime of the helper. A long-lived helper means Timeout + 30s and then a timeout.
  - **Helper started with `setsid`, Timeout 500ms:** `Run` returns after 6s with a timeout error. The helper is outside the process group, so `procgroup` can't kill it.
- **Cause:** unchanged.
  - `readStream` reads to EOF before `cmd.Wait` (`adapter.go:167-168`).
  - `group.Close()` comes after `Wait` (`adapter.go:169`), so it can't free the pipe.
  - `outcome` checks `runCtx` before looking at the result (`adapter.go:230`).
- **Fix:** about 6 lines.
  - Pass `agy` a plain `os.Pipe()` writer as `cmd.Stdout` and close our copy after start.
  - Run `cmd.Wait(); group.Close()` in a goroutine, so process exit (not pipe EOF) ends the run.
  - Also stop reading once the `result` event arrives, which covers `setsid` helpers.
  - Accept a SUCCESS result before checking `runCtx`.
  - Add the fake-`agy` "background child" case, with and without `setsid`, as a test.

### Medium

**G1. `Close()` right after `Wait` removes the 3s SIGTERM grace for grandchildren.** *(Confirmed by probe and by reading.)*
- **Input:** cancel a run where `agy` exits promptly on SIGTERM, and its child needs about 500ms to clean up.
- **What happens:**
  - `Wait` returns as soon as `agy` exits, and the adapter calls `group.Close()` (`adapter.go:169`).
  - `release` sends SIGKILL to the group at once (`procgroup_unix.go:42-45`), so the grandchild never finishes its cleanup.
  - The control run without `Close` did finish it. §6.5 requires SIGTERM, then SIGKILL after 3s.
- **Fix:** if `Kill` already ran, let the pending timer deliver the SIGKILL instead of sending it in `release`. For leftovers after a normal exit, send SIGTERM, then SIGKILL 3s later.

**G2. `Setpgid` takes `agy` off the terminal's signal path, so a hangup leaves it running.** *(Confirmed by reading; not probed.)*
- **Input:** close the terminal running `hub serve` (SIGHUP), or press Ctrl+C before T3.4's shutdown path exists.
- **What happens:**
  - `agy` is no longer in the terminal's foreground process group, so the terminal's SIGINT and SIGHUP never reach it.
  - `cmd/hub/main.go:176` only traps Interrupt and SIGTERM, so SIGHUP kills `hub` with no cleanup, and `agy`'s tree keeps running.
  - Before T3.2 a hangup reached `agy` too, so this is a regression against "no orphan after exit/shutdown".
- **Fix:**
  - Add SIGHUP (and SIGQUIT) to `NotifyContext`.
  - Record a T3.4 requirement: on shutdown, cancel every run context and wait for every `Run`, `Close` included, to return before `main` exits.

**C2. `fsx.Root.RemoveAll` is a general recursive delete.** *(Confirmed by probe.)*
- **Input:** `r.RemoveAll("topics")`.
- **What happens:** the whole `topics/` tree is deleted with no error.
  - The only guard is a code comment (`root.go:167`).
  - R5.4 (TDD around line 1223) allows only internal cleanup of `.app/work`, `.app/staging` and expired logs.
  - The new function has no test.
- **Fix:** refuse any path not strictly under `.app/work/`, `.app/staging/` or `.app/logs/`. Or remove it from `fsx.Root` and keep it private to the sandbox code from C1. Add a negative test.

**C3. The sandbox is keyed by job only, but one job makes several AI calls.** *(Confirmed by probe for calls in sequence; plausible for parallel calls.)*
- **Input:** `Compose(A)` with `answer.go`, then `Compose(B)` for the same job 42 with `answer.py`.
- **What happens:**
  - A's folder loses `inputs/answer.go` and now holds B's `answer.py`, while A's `InputFiles` still lists `answer.go`.
  - D10 and T5.2 make one AI call per exercise, and the adapter itself expects retries and parallel evaluation (`adapter.go:269`).
  - With parallel calls, a second `Compose` wipes the sandbox of an agent that's still running.
- **Where:** `composer.go:50` (`WorkRel`) and `:84-87`
- **Fix:** key the sandbox per call, for example `.app/work/<job>/<seq>`. Or write down, and enforce, that the calls within one job run strictly one after another.

**C4. The frontmatter parser misreads valid YAML, so the wrong skill version can be recorded.** *(Confirmed by probe.)*
- **Cases:**
  - A nested key overwrites the top-level one: `output_schema:` followed by `  name: lesson` gives Name `lesson`.
  - Quotes and comments are kept: `version: "1.0.0" # bump` gives Version `"1.0.0" # bump`.
  - An empty frontmatter (`---\n---\n`) followed by a `---` rule in the body makes the parser read name and version from body text. The probe got `name=injected`, `version=9`.
  - `version: banana` is accepted, though §6.3 says semver.
  - A file that ends right after the closing `---` with no newline is rejected.
- **Why it matters:** §6.3 records the skill version in `jobs`, `manifest.json` and `result.json` so outputs can be traced. Users are expected to edit skills.
- **Where:** `composer.go:116-131`
- **Fix:** read only top-level keys (lines with no leading whitespace), strip quotes and `#` comments, check semver, and accept a closing `---` at end of file.

### Low

| # | Finding | Status | Location |
|---|---|---|---|
| G3 | Every `Close` calls `Kill`, which arms a new SIGKILL timer for `-pgid` 3s later, even after a successful run. The timer is never stopped. If the pid is reused within 3s by a new group leader, for example another parallel `procgroup.Start`, an unrelated group is killed. Unlikely with a large `pid_max`; more plausible with 32768 or macOS's 99999. Fix: keep the timer, arm it only on the cancel path, and stop it when the group is gone. | Confirmed (reading) | `procgroup_unix.go:35` |
| G4 | `Start(exec.Command(...))` always fails ("command with a non-nil Cancel was not created with CommandContext"), though the doc implies any command works. Fix: state that CommandContext is required. | Confirmed (probe) | `procgroup.go:18` |
| G5 | `Start` silently overrides the caller's settings. A caller's `Cancel` is replaced. `Setsid=true` makes the start fail with EPERM. A non-zero `Pgid` means `Kill(-pid)` gets ESRCH, which is mapped to nil, so nothing is killed and no error is reported. Fix: return an error if `Setsid`, `Pgid` or `Cancel` is already set. | Confirmed (probe) | `procgroup_unix.go:17-22` |
| G6 | Windows: errors from `OpenProcess` and `AssignProcessToJobObject` are discarded. If the assignment fails, cancel quietly falls back to killing only the child, the exact RK6 risk. Fix: make `Start` fail, or at least log it. | Confirmed (reading) | `procgroup_windows.go:40-48` |
| G7 | Windows: `Process.Kill` right after `TerminateJobObject` can get ERROR_ACCESS_DENIED, which `ignoreDone` doesn't catch. The adapter hides it because `outcome` checks the contexts first. Fix: skip `Process.Kill` when `TerminateJobObject` succeeded. | Plausible | `procgroup_windows.go:62` |
| G8 | T3.2 is ticked while its required manual Windows test is open, and "no orphan after exit" has no Unix test. Same pattern as T3.1 and B1. | Confirmed | `PROGRESS.md` |
| G9 | Test gaps: no adapter-level test that cancelling `Run` kills `agy`'s grandchild, no A1 case, no SIGTERM-grace test (G1 passes the suite), and nothing runs on Windows. If `go test` is killed by its timeout, the helper processes sleep for an hour. | Confirmed | `procgroup_test.go` |
| C5 | `Compose` accepts an empty `Schema`, but the security rules promise `output.schema.json`. The adapter then skips `--json-schema` (`adapter.go:135`), so nothing enforces the output shape. A nil `Agents` or `Profile` silently becomes an empty file. Fix: reject a missing schema and an empty `AGENTS.md`. | Confirmed (probe) | `composer.go:54-105` |
| C6 | The answer whitelist is looser than §6.3's single `answer<ext>`: `{answer, answer.go, answer.py}` are accepted together, and `answer.GO` plus `answer.go` collide on Windows with an unclear error. No bypass through map keys, unicode or path segments. Fix: allow at most one answer, and lowercase the extension. | Confirmed (probe) | `composer.go:32,59-63` |
| C7 | `skills/answer-evaluator/SKILL.md` (lines 7, 19) names `inputs/answer`, but the composer writes `inputs/answer<ext>`, so the skill's own injection rule names a file that doesn't exist. The frontmatter `inputs` list is never checked against the files provided. | Confirmed (reading) | `skills/answer-evaluator/SKILL.md`, `composer.go` |
| C8 | The fake adapter reports an expired context as `canceled`, not `timeout`, and never sends events. T3.5's timeout tests need the difference. Fix: map `context.DeadlineExceeded` to `ErrTimeout`. | Confirmed (probe) | `fake.go:32-34` |
| C9 | If the `MkdirAll` at `composer.go:88-90` fails, no cleanup runs, and a `Cleanup` error is ignored at `:93`. A prompt of exactly 4096 bytes passes (`>`), though the spec says "shorter than 4 KB". Fix: use `>=`. | Confirmed (reading) | `composer.go:79,88-93` |
| C10 | Lean: the test's `mapsEqual` duplicates Go's `maps.Equal` (−12 lines). The `inputs` list is built with a second loop over the files map (−5 lines). | Confirmed | `composer_test.go`, `composer.go:63-72` |

### Spec gaps

- **D5 rests on unverified `agy` behaviour.** The sandbox's parent folders are the user's workspace, with no operating-system confinement. Whether an injected agent can write outside its working folder depends on `agy`'s headless permissions or `--sandbox`, which have never been seen working (B1). Whether `agy` walks up from its working folder and loads the base directory's real `AGENTS.md` or `.agent/` skills is also unknown.
- **The §6.3 frontmatter fields** `inputs` and `output_schema` are parsed but unused.
- **Reading the real files** (`AGENTS.md`, `profile.md`, `.agent/<skill>/SKILL.md`) and building `task.json` are deferred to the T3.6 handlers. PROGRESS says so.

### Checked and found correct

**T3.2:**
- **Cancel timing:** `cmd.Process` is set before exec's context-watch goroutine starts, so `Cancel` can't run before the process exists. A 200-iteration loop under `-race`, with near-instant deadlines, showed no races and left no processes.
- **Group signals:**
  - SIGTERM to `-pgid` before `Wait` is safe, because the unreaped leader keeps the pid reserved.
  - After the leader is reaped, Linux keeps the pid reserved while it's still anyone's group id.
  - Mapping ESRCH to nil can't hide a real failure before `Wait`.
- **Kill timing:** the 3s SIGKILL beats the 5s `WaitDelay` `Process.Kill`.
- **Start failures:** a failed start cleans up on both platforms. A context that's already cancelled starts nothing.
- **Windows, by reading:**
  - The struct layout and `unsafe.Sizeof` are right, and so are the access rights needed for assignment.
  - The job handle isn't inheritable, so a `hub` crash closes it and KILL_ON_JOB_CLOSE fires.
  - Every use of `job` is under the mutex, and `Close` is safe to call twice.
  - Nested jobs are fine on Windows 8 and later.
- **Build and tests:**
  - No CGO; `x/sys` is pure Go, correctly moved to a direct dependency, and builds for linux, windows and darwin.
  - Both tests fail if the kill reaches only the child. They run in about 3s against a 5s budget, so they aren't flaky.

**T3.3:**
- **Paths:** negative and zero job ids stay inside `.app/work`, and there's no way to bypass the whitelist through map keys, unicode or path segments.
- **Copies only:** inputs are bytes, so `Compose` can't link real files. The injection test would catch a hard link (the write goes through and changes the hash) and a link that escapes the folder (the fake's `os.Root` refuses it).
- **No half-built sandboxes:** every check runs before any file is touched, so a rejected `Compose` leaves nothing behind. A crashed run's leftovers are wiped. A `.app/work/<id>` symlink is removed as a link only.
- **Skill handling:** the security rules land right after the frontmatter and in `-p`. BOM and CRLF are handled.
- **Prompt size:** byte length is a safe upper bound for Windows' UTF-16 limit (1500 Persian characters still fit).
- **Windows paths:** `WorkDir` is built correctly from `BaseDir` plus `FromSlash`.
- **Schema file:** the adapter writes `output.schema.json` (atomically) and the composer never does, which keeps the split clean.

---

## 4. Earlier findings: status

| # | Finding | Status |
|---|---|---|
| **B1** | `agy` unusable (account region-blocked). | Partly unblocked (building against a fake `agy`, as before). Still blocked on 1.4.12. It now also blocks the T3.2 manual test and the D5 confinement question. |
| **H1** | One DB connection; a read via `db.Writer()` inside `WithTx` deadlocks. | Open. **Next task is T3.4**, the heaviest user, so it's now due. |
| **H2** | `DefaultConfigPath()` is never consulted with no `--config` flag. | Open, unchanged. |
| **old N1 / I1** | Topic keys change when a sibling group gains a topic, or when a deeper heading appears. | Open (High). No real roadmap has been imported yet. |
| **A1** | A background child holding stdout makes success look like a timeout, or a hang. | **Still open after T3.2.** Re-confirmed and extended (the `setsid` case); see §3. |
| **A2** | `ExtraArgs` deny-list bypass (`--settings`, `--sandbox=false`, …). | Open. Matters more now that D5 depends on `agy`'s own confinement. |
| **A3–A6, A10** | Wrong error codes for a cancelled context; loose patterns; the timeout rule overrides explicit errors; log folders collide; no `ErrStalled`. | Open, unchanged. A6 and C3 share a cause: per-job naming where it should be per call. |
| A7–A9, A11, A12 | Masking gaps, `meta.json` usage, `Probe` hang, unbounded stderr, test gaps. | Open. Under `-race` the antigravity package still takes 10.9s (A12). |
| I2–I9 | Re-import findings. | Open, unchanged. |
| R1, R3, R4, R6, R11→I1, R12, S1–S4, L9, … | See `git show 8a07973:review-report.md` §4. | Open, unchanged by these commits. |

---

## 5. Recommended order

1. **Make the AI layer safe before T3.4 builds on it.** This is one focused commit; most items are a few lines each.
   - **A1:** process exit ends the run (`os.Pipe` plus a goroutine running `Wait` and `group.Close`), stop reading at `result`, and accept SUCCESS before checking `runCtx`.
   - **G1 and G3:** keep the SIGTERM→SIGKILL grace and arm the timer only on cancel.
   - **C1 and C2:** a dedicated root on `.app/work` with an Lstat check, and limit or hide `RemoveAll`.
   - **C3 and A6:** name sandboxes and log folders per call, not per job.
   - **G2:** trap SIGHUP, and note the shutdown requirement in T3.4's row.
   - **A3, A4/A5 and A10:** check the context first, classify `res.Error` first, and map `context.Cause`.
   - **A2:** switch `ExtraArgs` to an allowlist.
2. **H1:** a reader pool, or a guard against queries outside the transaction, before T3.4.
3. **Topic identity, as one change, before importing any real roadmap:** I1/R11, old N1, I2, R3, R6, R4, folding in N2 and N3. Unchanged from the last report.
4. **I3:** cap the fuzzy guess.
5. **Small htmx fixes, together:** R1, R2, S2, S3/S4, R12. Then **H2** and **S1**.
6. **Process (P1):**
   - Add "read `review-report.md` §5" to the continuity protocol in `AGENTS.md` and `PROGRESS.md`.
   - Untick T3.1 and T3.2 until their manual checks are recorded.
   - Add a row listing open High findings.
7. **Low items** (C4–C10, G4–G9 and older L, N, S, R, I and A items) whenever their file is being changed anyway.

---

*Not covered:*
- **Real Windows:** Job Object assignment, `TerminateJobObject` timing, the junction version of C1, case collisions, and file locks blocking cleanup while an `agy` child still holds handles.
- **Real `agy`:** whether it starts helpers that hold stdout or call `setsid` (how often A1 hits), how it confines file access (D5), and whether it loads `AGENTS.md` from parent folders.
- **macOS:** only cross-compiled and vetted.
- **G2:** found by reading; the SIGHUP orphan was not probed.
