# Browser Test Report: Learning Hub

- **Date:** 2026-11-09
- **Target:** the running instance at `http://127.0.0.1:8787` (the user's real data, 5 topics)
- **Browser:** Google Chrome 155.0.8059.84, headless, driven by Playwright (`playwright-core` 1.48.2)
- **Profiles:** desktop 1280×900 light, mobile 390×844 dark
- **Mode:** read-only. Every reachable GET page was crawled. No AI jobs were started and no forms were submitted. The only POSTs were security probes, and the server rejected all of them.

## Summary

| Area | Result |
|---|---|
| Pages crawled | 40 unique URLs × 2 profiles = 80 loads, **all HTTP 200** |
| JS errors / CSP violations | **0** (one console error: `favicon.ico` 404) |
| Horizontal page scroll | **none** at 1280px or 390px |
| Forms with CSRF token | **16/16** POST forms |
| Security headers / Host / Origin / CSRF | **pass** |
| Error paths (unknown ids, traversal) | **pass** (404, no leaks) |
| Visual bugs found | **2** (lesson markdown rendering) |
| Page load (networkidle) | ~530–700 ms per page |

## Findings

### 1. Code blocks: lines with Persian comments jump to the right edge (Medium, visual)
In `/topics/1/lesson`, each line in a `pre` that contains a Persian comment (for example `// اعلان در سطح پکیج`) is right-aligned and reordered. That line ends up detached from the Go code around it, which is still left-aligned. Code with comments is hard to read.
- **Cause:** `internal/ui/static/app.css:159`: `pre, code { direction: ltr; unicode-bidi: plaintext; }`. `plaintext` picks a direction for each line from its first strong character. Comment lines start with `//`, which is neutral, followed by Persian, so the browser makes them RTL.
- **Suggested fix:** use `unicode-bidi: isolate` (or `embed`) on `pre` so that every line stays LTR. Inline `code` can keep `plaintext`.

### 2. Ordered-list numbers drift to the far left in RTL lessons (Low–Medium, visual)
In the "Integer types" section of `/topics/1/lesson`, the markers `1.`, `2.`, `3.` are drawn at the left edge of the card, while their item text is on the right. This looks like the list's `dir="auto"` (from `render.dirTransformer`) or its padding resolving to LTR. I did not confirm the root cause.

### 3. Two `<h1>` elements on lesson pages (Low, a11y)
`/topics/{id}` and `/topics/{id}/lesson` have two H1s: the page title and the lesson's own `# …` heading. Screen-reader outlines show two top-level headings. Option: shift the headings of rendered markdown down one level.

### 4. `favicon.ico` returns 404 (Low, cosmetic)
This puts a console error on the first page load. It is already listed as known in PROGRESS T7.10. A small embedded icon or `<link rel="icon" href="data:,">` would remove it.

### 5. Mobile tab bar hides the last tabs without a visual hint (Low, UX)
At 390px only 4 of the 6 tabs fit ("Reports" and "Suggestions" are off-screen). The bar scrolls horizontally by design, but nothing shows that more tabs exist. Option: fade the edge, or wrap the tabs.

### 6. File input shows English native text (Info)
On `/roadmaps/new`, the button reads "Choose File / No file chosen" in the Persian UI. This text comes from the browser's own file control.

## Security probes (curl)

| Probe | Expected | Got |
|---|---|---|
| `Host: evil.com` | 403 | 403 ✅ |
| `Host: localhost:8787` | allowed | 303 → `/topics` ✅ |
| POST `/sync/scan`, same Origin, no CSRF | 403 | 403 ✅ |
| POST `/sync/scan`, `Origin: http://evil.com` | 403 | 403 ✅ |
| POST `/settings/privacy`, bad CSRF token | 403 | 403 ✅ |
| `/static/../hub.db` | no file | 307 → `/hub.db` → 404 ✅ |
| `/static/%2e%2e/go.mod` | 404 | 404 ✅ |
| `/topics/999999`, `/topics/abc`, `/files/999999`, `/jobs/999999/card`, `/roadmaps/drafts/999999`, `/nope` | 404 | 404 ✅ |

Response headers on `/`:
`Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: same-origin`, `Cross-Origin-Opener-Policy: same-origin`, `Cache-Control: no-store` ✅

## Per-page checks (both profiles)
For every page the crawler checked: status code, console errors/warnings, failed requests, horizontal overflow, `html[dir=rtl][lang=fa]`, Vazirmatn font applied, number of H1s, inputs without labels (**0**), buttons without names (**0**), images without alt (**0**), and a CSRF field in each POST form.

Pages covered: `/`, `/topics`, `/sync`, `/settings`, `/roadmaps/new`, and for topics 1–5 the overview plus the `source`, `lesson`, `exercises`, `answers`, `reports` and `suggestions` tabs (also `/topics/1/lesson?v=1`).

## Not tested
- Any flow that writes data or calls AI: roadmap upload/confirm, lesson/exercise/evaluate jobs, retry/cancel, accepting suggestions, sync apply, changing settings. These would change real data on the running instance.
- Draft pages (`/roadmaps/drafts/{id}`): no draft existed.
- Live job cards / progress polling: no job was running.
- Keyboard-only navigation and color contrast measurement.

---

# Browser Test Report: Learning Hub (Session 2 — Windows & Chrome Live Test)

- **Date:** 2026-11-12
- **Environment:** Windows (amd64), Google Chrome (Playwright engine)
- **Target:** `http://127.0.0.1:8787` (`hub.exe serve` with real database and real AI tool probe)
- **Profiles tested:**
  - Desktop: 1280×844 (Light mode & Dark mode via `prefers-color-scheme: dark`)
  - Mobile: 390×844 (Light mode & Dark mode)
- **Scope executed:**
  - Full server startup on Windows loopback (`127.0.0.1:8787`)
  - Live exploration of `/topics`, `/settings`, `/sync`, `/roadmaps/new`, `/roadmaps/drafts/{id}`, `/topics/{id}` (tabs: `lesson`, `source`, `exercises`, `answers`, `reports`, `suggestions`)
  - Live interactive operations:
    - Search & filter on `/topics` with instant debounced htmx query
    - AI tool doctor probe display on `/settings`
    - Upgrading all outdated skills (`chapter-detector` 1.0.0 → 2.0.0, `exercise-generator` 1.1.0 → 1.2.0, `lesson-generator` 1.1.0 → 1.2.0)
    - Full filesystem sync scan via `POST /sync/scan`
    - Markdown roadmap upload (`persian.md`), draft preview generation, and "Continue without AI" (`ادامه بدون AI`) fallback confirmation leading to creation of 6 new topics and chapter sources
  - Visual & BiDi inspection with screenshots at every key interaction

## Summary of Findings

| ID | Severity | Area | Description | Status |
|---|---|---|---|---|
| **B1** | Low (Cosmetic) | Console / Network | `favicon.ico` returns 404, logging an error in the Chrome Developer Tools console. | **Fixed ✅** (R18/CR-59) |
| **B2** | Medium (UX) | Search (`/topics`) | When search returns 0 matches, the UI displays `0 موضوع یافت شد` followed immediately by the large "ورود نقشه راه جدید" button without an informative "no matching results" empty state message. In addition, search queries only match `topics.title_norm` and not group/phase headers. | **Fixed ✅** (R19/CR-60) |
| **B3** | Medium (BiDi / Typography) | RTL Layout across pages | Multiple BiDi isolation issues when mixing English keywords, punctuation, code tokens, and numbers inside RTL text: (1) Outdated skills list reorders English names and colons (`:chapter-detector`); (2) Config tags like `[adapter]` and `--json-schema` invert surrounding punctuation and parentheses; (3) Draft preview heading flips colon on English filename (`persian.md :پیش‌نمایش ورود`); (4) Inline code with neutral operators in lesson/source (e.g. `:=` and `var`) reverses token sequence. | **Fixed ✅** (R20/CR-61) |
| **B4** | Low (CSS / Responsive) | Mobile Viewport (`390px`) | On `/settings`, the AI tool select box and "ذخیره" button wrap onto separate lines awkwardly, and the `<dl>` definition list dt/dd elements have inconsistent alignment on narrow viewports. | **Fixed ✅** (R21/CR-62) |
| **B5** | High (Platform / Windows) | Test suite on Windows | Three Windows-specific compatibility bugs were discovered and fixed during test preparation: (1) `filepath.Dir` in `cancel_test.go:40` generating backslashes rejected by `fsx.ValidateRelPath`; (2) `filepath.FromSlash` required in `exercises_test.go:140,144`; (3) CRLF normalization required in `parse_test.go:49`. | **Fixed ✅** |

