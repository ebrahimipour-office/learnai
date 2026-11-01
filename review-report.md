# Review Report: Work Completed So Far

**Date:** 2026-11-01
**Scope:** everything up to `9cd42c8`: Phase 1 (T1.1–T1.9), the fixes in `966b630`, T2.1 (`cb1b66a`), T2.2 (`be92d25`) and T2.3 (`9cd42c8`).
**Earlier report:** this replaces the review of work up to `68268c0` (commit `01ce546`, still in git history). The code it reviewed has **not changed** since, so its findings were re-checked and carried over below.

**Method:**
- `go vet ./...`; `go test -race -count=1 ./...`.
- A first-time check for the Windows target: `CGO_ENABLED=0 GOOS=windows go build ./...` and `CGO_ENABLED=0 go test ./...`.
- A separate reviewer (who didn't write T2.2/T2.3) read both against the design doc and used throwaway probe tests to confirm findings.
- I re-ran the two most serious findings (N1, N2) myself.
- `PROGRESS.md` was compared with the actual code.

---

## 1. Verdict

The foundation is still sound. Everything passes vet and the race detector. **The Windows build without CGO compiles cleanly**, which is new: it was never checked before. The CGO-free rule holds.

**T2.2 and T2.3 work, but they have one high-severity bug and one design gap, both in topic identity and selection.** Duplicate group titles can make a topic's stable key change even though the topic itself wasn't edited (N1). The default "deepest heading" rule silently drops whole branches of the roadmap (N2). Both should be fixed before T2.4/T2.5 store anything, because once keys are in the database, changing the key formula means migrating stored data.

**None of the earlier findings has been fixed yet.** The blocker (B1) and the two high-severity issues (H1 database deadlock, H2 config never loaded) are still open. `PROGRESS.md` still marks T1.1 and T1.8 as done.

| Severity | Earlier (still open) | New (T2.2/T2.3) |
|---|---|---|
| Blocker | 1 | 0 |
| High | 2 | 1 |
| Medium | 4 | 2 |
| Low | 10 | 3 |

---

## 2. PROGRESS.md claims vs reality

| Task | Claimed | Actual |
|---|---|---|
| **T1.1** agy feasibility | ✅ done (mentions `agy 1.1.12`) | ❌ **not done.** Only `--version` was ever checked, and the installed version is 1.4.7. A real call fails because the account is region-blocked (B1). `docs/feasibility-report.md` doesn't exist. |
| **T1.5** CLI skeleton | ✅ with a ⚠️ note | ⚠️ partial: `serve` starts no HTTP server (M1), and the config is never loaded (H2). |
| **T1.8** SQLite store | ✅ done | ⚠️ partial: the read-only reader pool from design §4.1 is missing, which causes H1. |
| **T2.1** parser | ✅ | ✅ |
| **T2.2** topic selection | ✅ | ⚠️ works, but has N2 (dropped content) and N1 (via groups). |
| **T2.3** stable key | ✅ | ⚠️ meets the spec's list, but has N1 and N3. |

---

## 3. New findings (T2.2 / T2.3)

### High

**N1. A topic's key changes when a same-named sibling group gains a topic.** *(Confirmed, and re-run by me.)*
Groups are only created when they contain a selected topic. So which duplicate group gets the `#2` suffix depends on which of its same-named siblings happen to have topics.

- **Example:**
  - `# A / ## Intro / ## Intro / ### T` → T gets key `514241916ecb9c3e`.
  - Add `### U` under the *first* Intro → T gets key `de8ab63e09ebc194`.
- **Why it matters:** T wasn't touched, but T2.7 (re-import) will see it as removed plus new. The old row is archived and the student's progress is orphaned. The first Intro also takes over the second one's old group key.
- **Where:** `internal/roadmap/select.go:67-82`, `internal/roadmap/stablekey.go:46-56`
- **Fix:** number duplicates across **all** sibling headings in the parsed tree, not only the ones that were selected. For example, compute every heading's path once before selecting.

### Medium

**N2. Selection silently drops content.** *(Confirmed, and re-run by me.)*

- **(a) "Deepest" uses one level for the whole file.** `# R / ## Git / - branching / ## Docker / ### Images` gives one topic, "Images". "Git" and its items appear nowhere: not as a topic, not as a group, not in an outline. Skipped levels behave the same way: with `# A / ### B / ## C`, the deepest rule keeps only B, and level 2 keeps only C.
- **(b) Content directly under a group heading is lost.** With `# A / - intro item / ## B` and level 2, "intro item" disappears. In the test fixture, "Resources / - a book" is dropped under level 3.
- **Tests:** `TestSelectDeepestDefault` *asserts* the dropped branches, so the current tests protect this behaviour.
- **Spec:** A9 says "topic = deepest heading level". A literal reading allows this, but the likely intent is "the lowest heading in each branch". This needs **your decision.** Options:
  1. Make "deepest" mean leaf headings per branch.
  2. Keep the global level, and have Select return the dropped nodes so T2.4 can show them.
- **Where:** `select.go:87-95`, `select.go:105-106`, `maxHeadingLevel` at `select.go:119`

**N3. `Normalize` misses invisible characters common in pasted Persian text.** *(Confirmed.)*

- **Not handled:**
  - ZWJ U+200D, ZWSP U+200B
  - LRM/RLM U+200E/F, ALM U+061C, bidi controls U+202A-E / U+2066-9
  - a BOM in the middle of text
  - Arabic diacritics (harakat, tanwin, shadda) and superscript alef U+0670
  - `هٔ` vs `ۀ`
- **Example:** `"‏abc"` ≠ `"abc"`.
- **Why it matters:** an invisible-only edit changes the key, so T2.7 archives and re-adds the topic. It also weakens `title_norm` search (T2.6).
- **Spec:** the spec's required list is met. This is a robustness gap, not a spec violation. There is no UNIQUE-constraint risk, because duplicates are numbered after normalizing.
- **Where:** `stablekey.go:15-34`
- **Fix:** drop Unicode format characters (category Cf) and Arabic marks U+064B–U+065F and U+0670.

### Low

| # | Finding | Location |
|---|---|---|
| N4 | A heading made only of ZWNJ or kashida normalizes to `""`. Its children's keys then match top-level titles (`# ‌ / ## X` gives X the same key as a top-level X). All such groups share one key. Rare input. Fix: drop these headings in Parse, or use a non-empty placeholder. | `select.go:61-65`, `stablekey.go:48` |
| N5 | The `#N` suffix shares a namespace with literal titles. Reordering `X, X, X#2` vs `X, X#2, X` moves the key between the literal "X#2" and the second "X". Fix: use a separator that can't appear in a title (e.g. `\x00`) for the suffix. | `stablekey.go:52-54` |
| N6 | Titles have no length cap. `manifest.schema.json` allows at most 200 characters for a topic title, so a long heading passes import and only fails later at manifest validation (Phase 4). Check it in the T2.4 preview. | `select.go` (Topic.Title) |

### Test gaps

- Duplicate numbering is tested for topics only, never for groups. That's exactly where N1 lives.
- `TestStableKeyDiffersByPath` checks topic keys only, not group keys.
- There are no tests for skipped heading levels or for content under group headings (N2).
- The comment about the Items rule handling items promoted from an empty parent has no test. A probe showed it works correctly.

---

## 4. Earlier findings: status (all still open)

All details are in the earlier report (`git show 01ce546:review-report.md`).

| # | Finding | Status |
|---|---|---|
| **B1** | `agy` unusable: the account is region-blocked for all models. Blocks Phases 3–5. | Open. Decision needed (another account, or a direct-API adapter). |
| **H1** | Database deadlock: `SetMaxOpenConns(1)`, and a read inside `WithTx` hangs forever. The reader pool is missing. `internal/store/db.go:39` | Open |
| **H2** | `config.toml` is never loaded: an empty `--config` doesn't fall back to `DefaultConfigPath()`, and a missing explicit path is silently ignored. `cmd/hub/main.go:138`, `internal/config/config.go:128` | Open |
| M1 | `hub serve` starts no HTTP server, and the middleware isn't wired in. **Blocks T2.4.** | Open |
| M2 | The forbidden-flag list misses `agy --mode auto/accept-edits`. | Open |
| M3 | `hub doctor` reports OK when `agy` can't make a call; its write test bypasses `fsx.Root`. | Open |
| M4 | `ExtractAnswerContent` trims whitespace, so the student's answer isn't kept byte for byte. `internal/fsx/answertpl.go:79-88` | Open |
| L1–L10 | Duplicate exercise IDs in `CrossCheckResult`; the path length limit counted in bytes; dead code; Origin list; DSN not escaped; no directory flush; 403 instead of 413; front matter parsed as a heading; `gofmt` on 6 files; `migrate` without the instance lock. | All open. `gofmt -l` still lists the same 6 files. |

---

## 5. What's verified as solid

- **Windows build:** `CGO_ENABLED=0 GOOS=windows go build ./...` succeeds. The instance lock uses `gofrs/flock`, which works on Windows. *(New.)*
- **Tests:** all packages pass `go test -race -count=1`, and also pass with CGO turned off. `go vet` is clean.
- **T2.2/T2.3 basics** (confirmed by the separate reviewer):
  - Output is deterministic and in document order.
  - A parent group always comes before its children, and the indexes are correct.
  - Keys match `^[0-9a-f]{16}$`.
  - No UNIQUE-constraint violation is possible within one import.
  - Invalid rules return errors.
  - Outline nesting is correct.
  - The test strings really contain the characters they claim (decomposed é, ZWNJ, kashida, Persian and Arabic digits).
- **Phase 1 strengths are unchanged:** `os.Root` confinement, `O_EXCL` file creation, strict path patterns for AI output, partial unique indexes on the database, the scoring formula, Host/Origin/CSRF/CSP checks.

---

## 6. Recommended order

1. **Fix N1 and decide N2 now**, before T2.4/T2.5 store keys. Changing the key formula after topics are in the database means migrating data. Fold N3 into the same change: it's a few lines in `Normalize`, and changing it later would also change keys.
2. **Decide the AI backend (B1).** Phase 2 can continue in the meantime.
3. **H2 (config) and H1 (reader pool).** Both are small. Do them before more database code builds on the single connection.
4. **M1 (`serve`).** Required before T2.4.
5. **M4** before Phase 4. **M2 and M3** inside T3.1 and T3.9.
6. **Correct `PROGRESS.md`:** reopen T1.1, mark T1.8 as partial, and mark T2.2/T2.3 as needing the N1/N2 follow-up.
7. **Low items** whenever their file is being changed anyway.

---

*Not covered:* the app was not run on Windows. Only cross-compilation was checked, so L5 (DSN escaping) and the planned Windows Job Object work are still unverified at runtime. The design document was compared only for the parts covering finished tasks.
