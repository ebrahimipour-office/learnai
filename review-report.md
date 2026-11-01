# Review Report: Work Completed So Far

**Date:** 2026-11-01
**Scope:** all commits up to `68268c0`: Phase 1 (T1.1–T1.9), the security/durability fixes (`966b630`), T2.1 (`cb1b66a`), and the `agy` test run.
**Method:**
- Read all Go code, the SQL migration, JSON schemas and skill files.
- Compared them with `technical-design-document.md` and the claims in `PROGRESS.md`.
- Ran `go vet`, then `go test -race -count=1 ./...`.
- Wrote throwaway probe tests to confirm suspected bugs.
- Made one real headless `agy` call.

---

## 1. Verdict

The foundation is mostly sound. The security approach is right: file access goes through `os.Root`, protected files are created with `O_EXCL`, and the server checks loopback/Host/CSRF/CSP. Every package passes `go vet` and the race detector. **However, `PROGRESS.md` overstates progress.** Three tasks marked done are incomplete (T1.1, T1.5, T1.8). There is **one blocker** (the AI backend can't be used) and there are **two high-severity bugs**: a database deadlock and a config file that is never loaded.

| | Count |
|---|---|
| Blocker | 1 |
| High | 2 |
| Medium | 4 open, 2 fixed during this session |
| Low | 10 |

---

## 2. PROGRESS.md claims vs reality

| Task | Claimed | Actual | Gap |
|---|---|---|---|
| **T1.1** agy feasibility | ✅ done | ❌ **not done** | Only `agy --version` was run (see `environment-audit-report.md`). The required `docs/feasibility-report.md` and ADR don't exist. A real call fails: the account is blocked in this location. |
| **T1.5** CLI skeleton | ✅ done | ⚠️ partial | `serve` starts no HTTP server. `config.toml` is never loaded (H3). The `Makefile` from the spec is missing. |
| **T1.6** fsx | ✅ 8 tests | ✅ 10 tests | One issue (M4) in answer extraction. |
| **T1.7** HTTP security | ✅ 7 tests | ✅ 9 tests | The body-limit bug was fixed in `966b630`. |
| **T1.8** SQLite store | ✅ done | ⚠️ partial | The read-only reader pool required by the design (§4.1) is missing, which causes H2. |
| **T1.9** schemas | ✅ done | ✅ mostly | Minor gaps in `CrossCheckResult` (L1). |
| **T2.1** parser | ✅ done | ✅ done | Two documented limits (L8). |

---

## 3. Findings (most severe first)

### Blocker

**B1. The AI backend (`agy`) can't be used from this account.**
A real headless call to `agy` 1.4.7 fails the eligibility check before any AI work happens: *"not currently available in your location"*. It fails the same way for every model tried (`gemini-3.8-flash-medium`, `claude-sonnet-4-6`). Phases 3–5 depend on it.
→ **Decision needed:** use an eligible account or location, or write a different adapter. The `ai.Adapter` interface and the "direct API adapter" fallback (RK1) are already in the design.

The failed run also showed facts the T3.1 parser needs:
- Events use the key `event`, not `type`.
- The fields sit inside a `result` object: `status`, `error`, `response`, `finished_reason`.
- The exit code is unreliable. `stream-json` exited with 1, while `json` output seemed to exit with 0 for the same error. The adapter must check `status`.

### High

**H1. Database deadlock: a plain read inside a transaction hangs forever.** *(Confirmed with a probe test.)*
`store.Open` creates a single `*sql.DB` with `SetMaxOpenConns(1)`. While `WithTx` holds the only connection, any query through `Writer()` (or any second goroutine) waits forever. The probe confirmed it: `db.Writer().QueryRow("SELECT 1")` inside `WithTx` never returned.
- **Where:** `internal/store/db.go:39`
- **Why it matters:** Phase 3 runs jobs that write while htmx polls for progress. A single forgotten `tx` argument freezes the app.
- **Fix:** add the read-only reader pool the design specifies (§4.1, line 265): a second `sql.DB` with `mode=ro` and several connections.

**H2. `config.toml` is never loaded.**
`main.go` passes the `--config` flag value, which defaults to `""`, to `config.Load`. `Load` doesn't fall back to `DefaultConfigPath()`, which nothing calls. All user settings in `%APPDATA%\LearningHub\config.toml` are silently ignored. Also, an explicit `--config` path that doesn't exist silently falls back to the defaults instead of failing.
- **Where:** `cmd/hub/main.go:138`, `internal/config/config.go:128-137`
- **Fix:** when the flag is empty, use `DefaultConfigPath()`. When an explicit path is missing, return an error.

### Medium

**M1. `hub serve` doesn't serve.**
It opens the database and waits for a signal. There is no `http.Server`, the security middleware isn't attached, and there's no `ReadHeaderTimeout` or `IdleTimeout` (design §8). This must be done before T2.4.
- **Where:** `cmd/hub/main.go:136-188`

**M2. The forbidden-flag list is incomplete for the installed `agy`.**
Config validation blocks only `--dangerously-skip-permissions`. `agy` 1.4.7 also has `--mode auto|accept-edits`, which can approve tool calls automatically and bypass the permission model the design relies on (D5).
- **Where:** `internal/config/config.go:160-164`
- **Fix:** also reject `--mode`. Better still, use an allow-list in T3.1's argument builder.

**M3. `hub doctor` reports success when the AI backend is unusable.**
It checks only `agy --version`, so it printed OK for an account that can't make a single call. Its write test also goes to the current directory with `os.WriteFile`, bypassing `fsx.Root` and not testing the configured base directory.
- **Where:** `cmd/hub/main.go:60-92`
- **Fix:** make a tiny real call, such as a one-word prompt with `--output-format json`, and check `status`.

**M4. `ExtractAnswerContent` alters the student's answer.**
It runs `strings.TrimSpace` on the text after the marker. This removes leading indentation (for example, Python code whose first line is indented) and trailing newlines. That conflicts with the requirement that reports reproduce the answer byte for byte (T5.4). It can also make hashes disagree with the file on disk.
- **Where:** `internal/fsx/answertpl.go:79-88`
- **Fix:** remove only the single line break after the marker line.

**Fixed during this session (`966b630`):**
- **M5.** Body size limit applied after CSRF form parsing. Form posts bypassed the 64KB/1MB limits. A regression test was added.
- **M6.** `WriteNew`/`WriteAtomic` ignored errors from `Sync` and `Close`. A protected file could be left cut short and could never be rewritten.

### Low

| # | Finding | Location |
|---|---|---|
| L1 | `CrossCheckResult` accepts duplicate exercise IDs (q01 twice with q02 missing still passes), and it doesn't check `answered_count`/`total_count`. | `internal/schema/crosscheck.go:131` |
| L2 | `ValidateRelPath` counts the 220-character limit in bytes, so Persian paths hit it at about 110 characters. The error message says "characters". | `internal/fsx/safename.go:115` |
| L3 | Dead or duplicated code: an empty `if` block in `SanitizeSlug`, the unused `validSegmentRegex`, the extension list defined twice (fsx and config), and `ensureInitialized` called twice per validation. | `safename.go:47,59`; `config.go:85`; `validate.go` |
| L4 | The Origin allow-list includes port-less origins and is missing `[::1]`. Config accepts any loopback port or `localhost`, while the rules say `127.0.0.1:8787` only. | `middleware.go:79`; `config.go:155` |
| L5 | The SQLite connection string is built from an unescaped path, so `?`, `#` or `%` in the base directory break it. Not tested on Windows. | `internal/store/db.go:32` |
| L6 | `WriteAtomic` doesn't flush the directory to disk after the rename, so on Linux the rename may not survive a power loss. This doesn't apply to Windows. | `internal/fsx/writenew.go` |
| L7 | An oversized form now returns 403 "invalid CSRF token" instead of 413 (a side effect of the M5 fix): correct outcome, misleading message. | `middleware.go` |
| L8 | Parser: YAML front matter (`---` / `title: x` / `---`) is parsed as a heading. GFM tables (named in risk RK7) aren't parsed. | `internal/roadmap/parse.go` |
| L9 | Upkeep: 6 Phase 1 files aren't formatted by `gofmt`. `GEMINI.md` is an exact copy of `AGENTS.md`. PROGRESS.md points to a Windows base directory, and its "last commit" field is out of date. | — |
| L10 | `hub migrate` doesn't take the single-instance lock, so it can run while `serve` is running. | `cmd/hub/main.go:96` |

---

## 4. What's verified as solid

- **File safety:** `os.Root` confinement plus `ValidateRelPath` (rejects backslashes, colons, `..`, Windows reserved names and control characters). `WriteNew` uses `O_EXCL`, so it never overwrites.
- **AI output can't choose paths:** the manifest schema limits `slug`, `ext` and `file_name` to strict patterns (`^q[0-9]{2,3}-[a-z0-9-]+\.[a-z0-9]{1,8}$`).
- **Database rules:** partial unique indexes enforce one active job per scope (`uq_jobs_active_scope`) and one current lesson or exercise set. CHECK constraints cover every status column.
- **Scoring:** the cross-check formula matches the design: `sum(w·s)/sum(w)`, with unanswered = 0.
- **HTTP:** constant-time CSRF comparison, a Host allow-list against DNS rebinding, strict CSP, Fetch Metadata checks.
- **Prompt safety:** the skill template says plainly that inputs are data, not instructions.
- **Process lock:** a single-instance `flock` exits with a clear message.
- **Tests:** all packages pass with `-race -count=1`.

---

## 5. Recommended order

1. **Decide the AI backend (B1).** This blocks Phases 3–5. Phase 2 can go on meanwhile.
2. **H2 (config loading) and H1 (reader pool).** Both are small. Fix them before more database code builds on the single connection.
3. **M4 (answer extraction).** Fix before Phase 4 uses it.
4. **M1 (`serve`).** Needed before T2.4.
5. **M2 and M3.** Fold them into T3.1 (argument builder) and T3.9 (doctor).
6. **Low items** whenever their file is being changed anyway.
7. **Correct PROGRESS.md:** reopen T1.1, and mark T1.5 and T1.8 as partial.

---

*Not covered:* nothing was run on Windows (the target platform). Item L5 and the Windows Job Object design are unverified. The design document was compared only for the parts covering finished tasks.
