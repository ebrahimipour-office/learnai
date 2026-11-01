# Review Report: Work Completed So Far

**Date:** 2026-11-01
**Scope:** everything up to `09a10b4`. That is Phase 1 (T1.1–T1.9) and T2.1–T2.4, plus two commits new since the last review:
- `b293fdb`: T2.5, transactional import confirm.
- `09a10b4`: T2.6, the topic list with Persian search.

**Earlier reports:** this replaces the review up to `8d91bf4` (commit `397815e`). Full details of older findings are in `git show 397815e:review-report.md` (S-series, M/L/N/H series) and `git show 25a314f:review-report.md` (older detail). Only `services/roadmaps`, `httpserver`, `ui`, `cmd/hub` and the new `services/topics` changed since then. Every earlier finding in other packages is still open as described.

**Method:**
- `go vet ./...`, `go test -race -count=1 ./...`, `CGO_ENABLED=0 GOOS=windows go build ./...`, `CGO_ENABLED=0 go test -count=1 ./...`, `gofmt -l`. All pass, except the 6 older files flagged by `gofmt` (L9).
- A separate reviewer who didn't write `b293fdb` or `09a10b4` read them against the design doc. That covered the T2.5/T2.6 rows, the `/topics` page and the confirm route in §7, §3.4 (`dir_name` never changes) and §8. It confirmed findings with throwaway probe tests (since deleted), including a seeded run of 500 topics, 1500 exercise sets and 15000 attempts.
- I re-checked the most serious new finding (R1) against the vendored htmx 2.0.4 source. R3 and R4 can be read directly in `confirm.go`. I also re-checked H2 and ran the 500-topic timing test under `-race`.
- During implementation, a smoke run on the real `hub serve` binary did upload → confirm → `303 /topics` → Persian search. It found `rev-001.md` on disk.

---

## 1. Verdict

**The main flows are correct.**
- Confirm is all-or-nothing. A failure mid-way leaves no rows and no file, and a double confirm imports exactly once.
- The topic list shows the right tree, score, filter and search results. It renders 500 topics with a p95 of about 17ms (164ms under `-race`) against a 300ms budget.

**But T2.5 was built before its stated prerequisites.**
- The last report said N1/N2 (stable keys, dropped content) **must be fixed before T2.5**, because T2.5 stores keys permanently. That warning was not acted on (P1).
- Stable keys, `title_norm` and outlines are now persisted. Fixing N1/N2/N3 later now means a data migration if real imports exist. For now, nothing real has been imported, so the cost is still low.
- **Do this before importing a real roadmap.**

**New defects worth fixing soon:**
- **R1:** the Back button can break the `/topics` page.
- **R3:** a topic folder name can collide with a folder already on disk.
- **R4:** a topic unticked just before Confirm can still be imported, permanently.

| Severity | Earlier, still open | New in this round |
|---|---|---|
| Blocker | 1 (B1) | 0 |
| High | 3 (H1, H2, N1). N1 is now harder to fix. | 0 |
| Medium | 8 (M2–M4, N2, N3, S1–S3) | 3 (R1, R3, R4) |
| Low | 19 (L1–L6, L8–L10, N4–N6, S4–S10) | 13 (R2, R5–R13, R15, R16) + process note P1 |
| Fixed since last report | none | — |

---

## 2. PROGRESS.md claims vs reality

| Task | Claimed | Actual |
|---|---|---|
| **T1.1** agy feasibility | ✅ | ❌ Not done (B1). Unchanged. |
| **T1.5** CLI skeleton | ✅ | ⚠️ H2 and S1 are still open. |
| **T1.8** SQLite store | ✅ | ⚠️ The reader pool is still missing (H1). T2.5 avoided the deadlock by reading only through the transaction. |
| **T2.2 / T2.3** | ✅ | ⚠️ N1/N2/N3 are still open, and their output is now persisted (P1). |
| **T2.4** import preview | ✅ | ⚠️ S2 (§7.1 htmx error fragments) and N6 are still open. |
| **T2.5** confirm | ✅ | ⚠️ Every acceptance criterion is met and tested. R3 (`dir_name` vs disk) and R4 (stale choice) are open. |
| **T2.6** topic list | ✅ | ⚠️ Every acceptance criterion is met, including 500 topics under 300ms. R1 (Back button) is open. The running-job indicator is deferred, and PROGRESS.md already says so (R14). |

---

## 3. New findings (`b293fdb`, `09a10b4`)

### Process

**P1. T2.5 was implemented while the last report's "fix before T2.5" items were open.**
- **Which items:** N1 (keys change when a same-named sibling group gains a topic), N2 (dropped content) and N3 (`Normalize` gaps).
- **What is now persisted:**
  - `groups.stable_key` and `topics.stable_key`. T2.7 will match on them.
  - `topics.title_norm`. Search uses it.
  - `topics.outline`. Content dropped by N2 is missing from it.
- **Recovery:** the full source survives in `rev-001.md` and `roadmap_revisions.parsed_tree`, so everything can be recomputed.
- **Cause:** the implementing session didn't read `review-report.md`. The continuity protocol in `AGENTS.md`/`PROGRESS.md` only points to `PROGRESS.md`.
- **Fix:**
  - Fix N1/N2/N3 now, before any real import and before T2.7.
  - Add a pointer to `review-report.md`'s "Recommended order" in `PROGRESS.md`, so blockers aren't missed again.

### Medium

**R1. The Back button can replace the `/topics` page with a bare results fragment.** *(Confirmed from the htmx 2.0.4 source; not run in a browser.)*

- **What happens:**
  - Live search uses `hx-push-url`, so every 300ms pause in typing adds a history entry.
  - On Back, if htmx's history cache misses, it re-requests the URL with `HX-Request: true` and `HX-History-Restore-Request: true`. It then puts the response in place of the whole `<body>` (verified in `internal/ui/static/htmx.min.js`).
  - The handler only checks `HX-Request`, so it returns the `topic-results` fragment.
- **Result:** the header, heading, search form and import link disappear.
- **When the cache misses:**
  - After 10 entries (`historyCacheSize:10`).
  - When localStorage is full, which comes quickly with a 500-topic page.
  - Always, when localStorage is blocked.
- **Where:** `internal/httpserver/handlers_topics.go:39-42`, `internal/ui/templates/topics.html:7`
- **Fix:**
  - Render the full layout when `HX-History-Restore-Request: true`.
  - Use `hx-replace-url` instead of `hx-push-url`, so Back doesn't step through every partial query.
  - Add `Vary: HX-Request`.

**R3. A topic's `dir_name` is checked only against the database, never against the disk.** *(Confirmed with a probe.)*

- **The gap:** `freeRoadmapSlug` checks the disk, but topic folder numbering does not.
- **Probe:** a folder `topics/01-types/answers/v1/q01-x.go` was left over from an earlier `app.db`. A new import gave a topic `dir_name=01-types`.
- **Result:** since `dir_name` never changes (§3.4, I6), that topic's first generation either fails forever on `WriteNew` or mixes new files with old answers.
- **Second problem:** the number is `COUNT(*)` of topics, which is gap-free only while no topic row is ever deleted.
- **Where:** `internal/services/roadmaps/confirm.go:117-131`
- **Fix:** inside the transaction, skip numbers whose `topics/<dir>` exists on disk or in the DB (`MAX` + probe), and drop the reliance on `COUNT(*)`.

**R4. Confirm reads the draft's choice outside the transaction, so a late untick can be lost.** *(Plausible: a timing race, not reproduced.)*

- **The code:** `Confirm` loads the draft and its `Choice` before `WithTx`. Inside the transaction it only deletes the draft by id.
- **Scenario:** the user unticks a topic, which sends an htmx POST to `/selection`. That handler re-parses up to 1 MB. The user clicks Confirm right away, and the confirm reads the old choice first.
- **Result:** the unticked topic is imported, and topics are never deleted.
- **Same outcome when:** the selection POST fails silently (S2), or the click is dropped (S4).
- **Where:** `confirm.go:26-31` with `drafts.go:122`
- **Fix:** read `selection` and `content` inside the transaction. For example, `DELETE … RETURNING selection, content`, then compute the selection from what was returned.

### Low

| # | Finding | Status | Location |
|---|---|---|---|
| R2 | After Back, the results match the older query but the search box shows the first-render value. htmx snapshots `innerHTML`, which keeps the `value` *attribute*, and the form sits outside the swapped region. Fix: put the form inside the swap, or update the input out-of-band. | Confirmed (source) | `topics.html:6-15` |
| R5 | The roadmap slug is capped at 64 *runes*, not bytes. A file name of 60 four-byte letters fails every confirm with "path exceeds 220", shown as a generic 500. A 64-character Persian name works. Fix: cap by bytes (about 120), or fall back to `roadmap`. | Confirmed (probe) | `confirm.go:162` |
| R6 | `%02d` with one counter shared across all roadmaps. From topic 100 on, folders sort `10-`, `100-`, `11-`. A second roadmap starts at, say, `37-`. Decide `%03d`, or per-roadmap numbering, **before real data**, because `dir_name` never changes. | Confirmed | `confirm.go:131` |
| R7 | Search can't match a query typed with no space where the title has a half-space: `نوعها` doesn't find `نوع‌ها`. Queries with a space or a ZWNJ do match. Fix: also compare with spaces stripped on both sides. | Confirmed (probe) | `services/topics/list.go:40,76` |
| R8 | A file named exactly `.md` gives an empty roadmap title, so the list shows an empty `<h2>`. Fix: fall back to the first H1, or the slug. | Confirmed (probe) | `confirm.go:61` |
| R9 | `List` runs three separate queries with no transaction. A Confirm committing between the group and topic queries shows new topics flat under their roadmap until reload. A read transaction is not an option, because `_txlock=immediate` takes the write lock. Fix: fetch groups in the same query. | Plausible | `list.go:40-79` |
| R10 | `printf "%.0f"` rounds half to even: 60.5 shows as 60, 88.5 as 88. Scores have 2 decimals (T5.3). Fix: round half up, or show decimals. | Confirmed | `topics.html:40` |
| R11 | `select_rule` stores `{"kind":"deepest"}`, not the level it resolved to. In T2.7, a new revision with a deeper heading would change which level counts as a topic, so every existing topic would look gone and be archived. Fix: store the resolved `{"kind":"level","level":N}`. Recoverable from `parsed_tree`. | Plausible (T2.7) | `confirm.go:57` |
| R12 | Double-clicking Confirm shows the second response, "draft not found / already registered", instead of the topic list. The data is safe. Fix: on `ErrDraftNotFound` after a confirm, redirect to `/topics`, or disable the button on submit. | Confirmed | `handlers_roadmaps.go:203-205`, `draft.html:5-9` |
| R13 | If the commit fails, `rev-001.md` is removed but the empty `roadmaps/<slug>/` folder stays, so the retry gets `slug-2`. Cosmetic. | Confirmed (reading) | `confirm.go:151-153` |
| R14 | §7.2 lists a running-job indicator on `/topics`; it is absent. This is deliberate: there are no jobs until Phase 3, and PROGRESS.md notes it for T3.8. | Spec gap, tracked | `topics.html` |
| R15 | Test gaps: the 500-topic test has no exercise sets or attempts, so the "latest attempt" part of the query isn't exercised at scale. The reviewer's probe with 15000 attempts took about 15ms. No tests cover `HX-History-Restore-Request`, a topic folder already on disk, or concurrent confirms. | Confirmed | `server_test.go`, `confirm_test.go` |
| R16 | The 500-topic timing test measures p95 of about 164ms under `-race`, against its 300ms limit. On a slower or loaded machine it can fail with no code change. Fix: skip the timing assertion when the race detector is on, or under `testing.Short()`, and keep the count check. | Confirmed (measured) | `server_test.go:284-287` |

### Checked and found correct

- **Atomicity:**
  - The draft delete and every insert run in one immediate transaction, under the store mutex.
  - `rev-001.md` is written last with `WriteNew` (create-only) and removed if the commit fails.
  - If the request is cancelled, everything rolls back and the file is removed. The draft survives a failed confirm.
  - Tested: a failure mid-way (a DB trigger on the second topic) and a file write failure. Both leave zero rows and no `roadmaps/` folder.
- **Double or concurrent confirm:** the draft DELETE runs first and checks rows affected, so exactly one import happens and the other gets 404.
- **What gets recorded:**
  - Excluded topics are skipped, and groups without topics are skipped. Parents are inserted before children.
  - Stable keys are unique within a selection (`#N` suffixes), so the per-roadmap UNIQUE constraints can't fire on a valid draft.
- **Revision file:** `rev-001.md` is byte-exact, including NUL bytes, and its sha256 matches.
- **Folders and names:**
  - The roadmap slug is unique in the DB and on disk. It is lowercase, so case-insensitive file systems are fine, and reserved Windows names fall back to `roadmap`.
  - A 64-character Persian name works. `topics/` is not created at import, as the spec says.
- **Confirm route:** CSRF is enforced, a missing `confirm=1` gives 409, and success gives 303 to `/topics`. `/` and the header link now go to `/topics`.
- **List query:**
  - The score and "ناقص" tag come from the latest attempt of the *current* exercise set.
  - The status filter is whitelisted, and unknown values are ignored.
  - Search uses `instr` with bound parameters, so there is no LIKE-wildcard issue. Arabic ي/ك, ZWNJ (typed as a space), Persian digits and case all match.
  - Branches with no match are pruned, and order follows the document.
- **htmx:** focus stays in the search box, which is outside the swap target, so S3 does not apply here. Requests queue in order.
- **Templates:** titles and the query are escaped.
- **Project rules:** no CGO, all file access goes through `fsx.Root`, and `WriteNew` is used for the immutable revision file.
- **H1 not triggered:** Confirm reads the draft before `WithTx` and inside it uses only `tx`. List doesn't use `WithTx`, and closes its group rows before the topic query, which matters on a single connection.

---

## 4. Earlier findings: status

| # | Finding | Status |
|---|---|---|
| **B1** | `agy` unusable (account region-blocked). Blocks Phases 3–5. | Open: decision needed. |
| **H1** | One DB connection; a read via `db.Writer()` inside `WithTx` deadlocks. | Open, not triggered (see above). Every new transactional service has to remember this. |
| **H2** | Config loading. | Open, restated: `--config <path>` works (the smoke run used it). With no flag, `DefaultConfigPath()` is never consulted, so `hub serve` uses defaults and the current folder. `cmd/hub/main.go:134`, `config.go:127` |
| **N1** | A topic's key changes when a same-named sibling group gains a topic. | Open, **worse:** keys are now persisted and T2.7 will match on them (P1). |
| N2 | Selection rules silently drop content. | Open, **worse:** the dropped content is missing from the stored `topics.outline` (recoverable from `parsed_tree`). |
| N3 | `Normalize` misses invisible and bidi characters and Arabic diacritics. | Open, **newly reachable:** it now drives search and the stored `title_norm`. A later fix means recomputing `title_norm` and `stable_key`. |
| N6 | Titles over 200 characters aren't caught at import. | Open, **newly reachable:** Confirm stores titles of any length. |
| S1 | `hub serve` exits 0 on a failed start. | Open. The new `OpenRoot` failure path in `main.go` also `return`s, so it exits 0 too. |
| S2 | htmx drops 4xx/5xx responses. | Open. It now also affects live search: a 500 leaves stale results on screen. |
| S3 | Draft form loses focus after each tick. | Open for the draft form. Live search is fine. |
| S4 | A fast second tick can be dropped. | Open, and now it matters more: combined with R4, a dropped untick gets imported. |
| S8 | `SetChoice` stores unknown excluded keys. | Open; harmless for Confirm. |
| L9 | `gofmt`. | Open: 6 older files. The new and touched files are clean. |
| M2–M4, N4, N5, S5–S7, S9, S10, L1–L6, L8, L10 | See the earlier reports. | Open, unchanged by these commits. |

---

## 5. Recommended order

1. **Before importing any real roadmap and before T2.7:**
   - Fix N1, decide N2, and fold N3 into the same change. Keys and `title_norm` are persisted now, so this is the cheapest it will ever be.
   - In the same change, fix R3 and R6 (`dir_name`: check the disk, decide `%03d`/per-roadmap numbering) and R11 (store the resolved rule level). All of them fix values that never change once data exists.
   - Fix R4: read the choice inside the transaction.
2. **Small htmx fixes, together** (about one short commit):
   - R1: full page on `HX-History-Restore-Request`, plus `hx-replace-url` and `Vary`.
   - R2.
   - S2: error fragments, with live search covered too.
   - S3/S4: keep the form element.
   - R12.
3. **H2** (default config path) and **S1** (exit code). Both are small and user-visible.
4. **B1:** decide the AI backend before Phase 3.
5. **PROGRESS.md:**
   - Correct the claims in §2.
   - Add a line pointing at this report's "Recommended order", so the next session reads it (P1).
6. **Low items** (R5, R7–R10, R13, R15, R16 and the older L/N/S items) whenever their file is being changed anyway.

---

*Not covered:*
- Nothing in this round was driven in a real browser. R1, R2 and R4 come from the htmx source and from tracing the code.
- The app was not run on Windows; only cross-compilation was checked.
- Preview and confirm speed for a full 1 MB roadmap was not measured.
