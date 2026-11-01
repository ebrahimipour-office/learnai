# Review Report: Work Completed So Far

**Date:** 2026-11-01
**Scope:** everything up to `8d91bf4`. That is Phase 1 (T1.1–T1.9), T2.1–T2.3, plus two commits new since the last review:
- `f1caabc`: `hub serve` now starts the HTTP server.
- `8d91bf4`: T2.4, the import draft preview, including a `Referrer-Policy` fix.

**Earlier reports:** this replaces the review up to `9cd42c8` (commit `25a314f`). Full details of older findings are in `git show 25a314f:review-report.md`. No code under `store`, `roadmap`, `config`, `fsx` or `schema` has changed since then (`git diff 9cd42c8 8d91bf4` on those directories is empty), so every earlier finding in them is still open as described.

**Method:**
- `go vet ./...`; `go test -race -count=1 ./...`; `CGO_ENABLED=0 GOOS=windows go build ./...`; `CGO_ENABLED=0 go test ./...`; `gofmt -l`.
- A separate reviewer who didn't write `f1caabc`/`8d91bf4` read them against the design doc (T2.4 row, §7 routes, §8 security). It confirmed findings with throwaway probe tests (since deleted) and a 3000-document property fuzz of the preview tree.
- I re-ran the most serious new finding (S1) myself.
- A manual browser run of T2.4 (Playwright/Chromium) was done during implementation. It is how the `Origin: null` bug was found.

---

## 1. Verdict

**The new work is functionally correct.**
- Upload, preview, rule switching and ticks all work in a real browser.
- Nothing is written to `roadmaps`/`groups`/`topics` or the `roadmaps/` folder before confirm. A test checks this, and the real database from the browser run confirmed it.
- All checks pass, including the race detector and the Windows build without CGO.
- The `Referrer-Policy` fix was independently confirmed against the Fetch spec. Before it, every POST from a real browser was rejected.

**The weaknesses are in error paths, not the main flow:**
- `hub serve` exits with code 0 when it can't start (S1).
- Under htmx, error responses are silently dropped (S2).
- Keyboard focus is lost after every tick (S3).

**Still blocking the roadmap:**
1. **N1 and N2 (stable keys / dropped content) must be fixed before T2.5.** T2.4 only stores keys in temporary drafts, so it was safe to build. T2.5 writes them permanently.
2. **H1 (database deadlock) will hit T2.5 directly:** T2.5 is the first code that reads inside `WithTx`.
3. **B1 (no working AI backend) still blocks Phase 3.**

| Severity | Earlier, still open | New in this round |
|---|---|---|
| Blocker | 1 (B1) | 0 |
| High | 3 (H1, H2, N1) | 0 |
| Medium | 5 (M2, M3, M4, N2, N3) | 3 (S1–S3) |
| Low | 12 (L1–L6, L8–L10, N4–N6) | 7 (S4–S10) |
| Fixed since last report | M1 (`serve`), L7 for form uploads | — |

---

## 2. PROGRESS.md claims vs reality

| Task | Claimed | Actual |
|---|---|---|
| **T1.1** agy feasibility | ✅ | ❌ Not done (B1). Unchanged. |
| **T1.5** CLI skeleton | ✅ (now says `serve` is complete) | ⚠️ `serve` works, but the config is still never loaded (H2), and a failed start exits 0 (S1). |
| **T1.8** SQLite store | ✅ | ⚠️ The reader pool is missing (H1). Unchanged. |
| **T2.1** parser | ✅ | ✅ |
| **T2.2 / T2.3** | ✅ | ⚠️ N1/N2/N3 are still open. |
| **T2.4** import preview | ✅ | ⚠️ Every acceptance criterion is met. Two spec gaps remain: §7.1 htmx error fragments (S2) and the 200-character title check that the last report asked T2.4 to add (N6). |

---

## 3. New findings (`f1caabc`, `8d91bf4`)

### Medium

**S1. `hub serve` exits with code 0 when the server fails, and logs "Listening" before the port is bound.** *(Confirmed, and re-run by me.)*

- **Scenario:** port 8787 is already taken. The log shows `Listening`, then `bind: address already in use`, and the exit code is 0. A failure in `NewSecurityManager` also exits 0.
- **Why it matters:** `hub doctor`, scripts or a service manager would think the server started.
- **Where:** `cmd/hub/main.go:191`, `cmd/hub/main.go:202-209`
- **Fix:**
  1. Call `net.Listen` first, then log, then `srv.Serve(ln)`.
  2. Have `runServe` return an error, and have `main` call `os.Exit(1)`. The deferred unlock and DB close still run inside `runServe`.

**S2. Under htmx, error responses are never shown.** *(Confirmed from the htmx 2.0.4 source; not run in a browser.)*

- **Cause:** htmx's default `responseHandling` does not swap 4xx/5xx responses, and our `htmx-config` doesn't change that.
- **Scenario:** the draft expires while the page is open, and the user ticks a box. The server returns 404, but nothing changes on screen: the box looks ticked even though nothing was saved. The 422 "invalid rule" fragment is never displayed either.
- **Second problem:** the 404 path renders a full page, which would end up nested inside the form if it were ever swapped in.
- **Spec:** design §7.1 requires an error fragment with `HX-Retarget` for htmx requests.
- **Where:** `internal/httpserver/handlers_roadmaps.go:117-119, 144, 152, 166-171`
- **Fix:** for `HX-Request`:
  - Send 404 with `HX-Redirect: /roadmaps/new`.
  - Return errors as fragments with `HX-Retarget`/`HX-Reswap`, or add a `responseHandling` entry for 422 in the meta config.

**S3. Keyboard focus is lost after every change** (accessibility). *(Confirmed from the htmx source.)*

- **Cause:** the whole form is swapped `outerHTML`. htmx only restores focus to an element that has an `id`, and the `<select>` and checkboxes have none.
- **Scenario:** a keyboard user ticks a box, focus jumps to `<body>`, and they have to tab through the whole tree again. With a 500-topic roadmap that is unusable.
- **Also:** the `aria-live` counter is replaced rather than updated, so screen readers may not announce the new count.
- **Where:** `internal/ui/templates/draft.html:9, 13, 30`
- **Fix:** swap an inner container so the form element survives. Or add stable ids: `id="sel-rule"`, and `id="t-{{.Key}}"` on checkboxes (keys are unique per selection).

### Low

| # | Finding | Status | Location |
|---|---|---|---|
| S4 | Two quick clicks can lose the second one. htmx queues the second request, then drops it because the first response replaced its form. The DB and screen stay consistent, but the click is undone. Same fix as S3, or `hx-sync="this:replace"`. | Plausible | `draft.html:9` |
| S5 | An oversized body sent with the `X-CSRF-Token` header (the htmx path) gets 422 "invalid rule" instead of 413. With the header present the middleware skips parsing, and `FormValue` hides the error. Fix: `r.ParseForm()` in the handler, mapping `MaxBytesError` to 413. | Confirmed | `handlers_roadmaps.go:141`, `middleware.go:117` |
| S6 | With `listen = "[::1]:8787"`, which the config allows, every POST gets 403 because `http://[::1]:port` is missing from the Origin allowlist. This was L4 before; `serve` now makes it reachable. Related: §8.1 asks for an exact `host:port` Host match, but the code ignores the port. Low risk. | Confirmed | `middleware.go:93-98` |
| S7 | A body over 1 MB + 64 KB gets a plain-text English 413 from the middleware instead of the Persian upload page. Go may also close the connection, so the browser shows "connection reset" instead. | Plausible | `middleware.go:115-119` |
| S8 | `SetChoice` stores excluded keys that don't exist in the draft, although its comment says it "validates c against the draft". The handler filters keys correctly (forged and duplicate keys were tested). This matters for T2.5 callers. | Confirmed | `services/roadmaps/drafts.go:130-133` |
| S9 | `config.Limits.MaxRoadmapBytes` exists but is never read; 1 MB is hardcoded in the service and the middleware. Wire it through, or delete the field. | Confirmed | `drafts.go:23`, `middleware.go:69` |
| S10 | Test gaps:<br>• The tick test checks only the count, not which box is unticked or that ticks survive a reload.<br>• No test pins `Referrer-Policy: same-origin`, so the fix can silently regress.<br>• No test for 413 on the htmx path, or for htmx not swapping 4xx.<br>• `server_test.go` is not `gofmt`-clean (one trailing blank line). | Confirmed | `internal/httpserver/server_test.go` |

### Checked and found correct

- **Preview tree (`buildTree`):** a property fuzz of 3000 random documents × 6 rules found 0 mismatches. The fuzz included duplicate titles and skipped heading levels. Order matches `Selection.Topics`, every topic sits under its exact group chain, and each group appears once.
- **Timestamps:** the Go format matches SQLite `strftime('%Y-%m-%dT%H:%M:%fZ')` at a fixed width, so string comparison is safe. The time is converted to UTC, so a `+03:30` clock still expires at 24 hours (checked ±1 minute). Expiry and purge use `>` and `<=` consistently.
- **Selection handler:**
  - Changing the rule resets the ticks.
  - Forged and duplicate keys are ignored, and a missing `shown_rule` means everything is ticked.
  - A bad rule gives 422.
  - Without JavaScript, the form falls back to a 303 redirect.
- **Referrer-Policy fix:** the Fetch spec's "append a request Origin header" sets `Origin: null` on non-GET requests under `no-referrer`, for both form POSTs and same-origin XHR. So the bug was real, and `same-origin` is the correct fix.
- **Request bodies:**
  - Multipart is parsed before the CSRF check, and an oversized body maps to 413.
  - Memory is capped at about 1.06 MB.
  - Uploads that spill to temp files are deleted by net/http after the request (0 left, checked on a real `httptest.Server`).
- **Templates:** `hx-headers` renders valid JSON. Titles, outlines and IDs are escaped. The `htmx-config` meta tag is static text.
- **htmx:** `internal/ui/static/htmx.min.js` matches the official 2.0.4 SRI hash, and the downloads from unpkg and jsDelivr were byte-identical.
  - `includeIndicatorStyles:false` avoids a CSP violation.
  - `allowEval:false` is safe, since nothing uses `hx-on` or `js:`.
  - The default `selfRequestsOnly` means the CSRF header is never sent to another origin.
- **Security:**
  - No CORS headers are sent.
  - The Host check runs on GET requests too, and `frame-ancestors 'none'` is set.
  - Draft IDs are 128-bit random.
  - Nothing state-changing is reachable cross-origin.
- **Shutdown:** SIGINT gives a graceful exit with code 0. `ErrServerClosed` is handled correctly.
- **Builds:** `go vet` is clean. `go test -race` passes in every package. The Windows build without CGO and the tests with CGO off both pass.

---

## 4. Earlier findings: status

| # | Finding | Status |
|---|---|---|
| **B1** | `agy` unusable (account region-blocked). Blocks Phases 3–5. | Open: decision needed. |
| **H1** | Database deadlock: one connection, and a read inside `WithTx` hangs. `internal/store/db.go:39` | Open. **T2.5 will be the first code to hit it.** The T2.4 drafts code avoids it by not using `WithTx`. |
| **H2** | `config.toml` is never loaded. `cmd/hub/main.go:134`, `internal/config/config.go:128` | Open. **Now more visible:** `hub serve` creates `app.db` and `.app/` in whatever folder you start it from. |
| **N1** | A topic's key changes when a same-named sibling group gains a topic. | Open. **Must be fixed before T2.5** stores keys. |
| M1 | `serve` started no HTTP server. | ✅ **Fixed** in `f1caabc`. |
| M2 | Forbidden-flag list misses `agy --mode`. | Open (handle in T3.1). |
| M3 | `hub doctor` reports OK when `agy` can't make a call. | Open (handle in T3.9). |
| M4 | `ExtractAnswerContent` trims whitespace, so answers aren't kept byte for byte. | Open (fix before Phase 4). |
| **N2** | The selection rules silently drop content (global "deepest" level; content directly under group headings). | Open. **Needs your decision** before T2.5. The preview now makes this visible but doesn't list what was dropped. |
| N3 | `Normalize` misses invisible and bidi characters and Arabic diacritics. | Open. Fold into the N1 change, since it also changes keys. |
| N4–N5 | Edge cases in empty-title and `#N` key suffixes. | Open. |
| N6 | Titles over 200 characters aren't caught at import. The last report asked the T2.4 preview to check this; it doesn't. | Open. |
| L4 | Origin allowlist. | Open; see S6. |
| L7 | Oversized body got 403 instead of 413. | ✅ **Fixed** for form uploads in `8d91bf4`. Still wrong on the htmx header path (S5). |
| L8 | YAML front matter is parsed as a heading. | Open. **Now user-visible** in the T2.4 preview. |
| L1–L3, L5, L6, L9, L10 | See the earlier report. | Open. L9 (`gofmt`) now covers 7 files (+`server_test.go`). |

---

## 5. Recommended order

1. **Before T2.5:**
   - Fix N1, decide N2, and fold N3 into the same change.
   - Fix H1 (reader pool), because T2.5's transaction needs it.
   - Make S8 drop unknown excluded keys.
2. **Small fixes worth doing together** (about one short commit):
   - S1: exit code and bind before logging.
   - S2: htmx error handling, the remaining §7.1 spec gap.
   - S3/S4: keep the form element; this fixes focus and lost clicks together.
   - S5: 413 on the htmx path.
   - S10: a `Referrer-Policy` regression test and `gofmt`.
3. **H2 (config loading):** small, and now user-visible.
4. **B1:** decide the AI backend before Phase 3.
5. **Correct `PROGRESS.md`:**
   - Reopen T1.1.
   - Mark T1.8 as partial.
   - Note that T2.2/T2.3 need the N1/N2 follow-up, and T2.4 needs S2.
6. **Low items** whenever their file is being changed anyway.

---

*Not covered:*
- S2, S3, S4 and S7 were confirmed by reading the htmx/net/http source, not by driving a browser.
- Preview speed for a full 1 MB roadmap was not measured. Each selection POST parses the content three times.
- The app was not run on Windows. Only cross-compilation was checked.
