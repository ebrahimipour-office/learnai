// Copy buttons (T4.5, CR-47, T9.11): CSP forbids inline handlers, so one delegated listener.
// data-copy buttons copy their path; .copy-code buttons copy the code block right after them.
// T10.11: print buttons share this listener. T10.14: so does open/close-all for topic groups.
document.addEventListener("click", async (ev) => {
  if (ev.target.closest("button[data-print]")) return window.print();
  if (ev.target.closest("button[data-toggle-groups]")) {
    const groups = [...document.querySelectorAll("details[data-group]")];
    const open = groups.some((d) => !d.open);
    for (const d of groups) d.open = open; // each fires "toggle", which saves it
    return;
  }
  const btn = ev.target.closest("button[data-copy], button.copy-code");
  if (!btn) return;
  const code = btn.dataset.copy === undefined ? btn.nextElementSibling?.querySelector("code") : null;
  btn.dataset.label ??= btn.textContent;
  clearTimeout(btn._copyTimeout);
  try {
    await navigator.clipboard.writeText(code ? code.textContent : btn.dataset.copy);
    btn.textContent = "کپی شد";
  } catch {
    // clipboard blocked: select the text so Ctrl+C works
    if (code) getSelection().selectAllChildren(code);
    else btn.parentElement?.querySelector("input")?.select();
    btn.textContent = "Ctrl+C";
  }
  btn._copyTimeout = setTimeout(() => {
    btn.textContent = btn.dataset.label;
  }, 1500);
});

// T9.11: a copy button before each rendered code block, on load and after htmx swaps.
function addCodeButtons(root) {
  for (const pre of root.querySelectorAll(".md pre")) {
    if (pre.previousElementSibling?.classList.contains("copy-code")) continue;
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "copy-code";
    btn.textContent = "کپی";
    btn.setAttribute("aria-label", "کپی کد");
    pre.before(btn);
  }
}
// T10.16: a topic opened from a search (?q=) marks the matches in its text and scrolls to the first.
// Text nodes only, never innerHTML; runs before the copy buttons so their labels are never marked.
// fold mirrors the server's search folding (roadmap.Normalize: Arabic ي/ى/ك, Persian and Arabic digits)
// with one-for-one replacements, so offsets in the folded text are offsets in the original (T12.1).
const fold = (s) =>
  s
    .toLowerCase()
    .replace(/[يى]/g, "ی")
    .replace(/ك/g, "ک")
    .replace(/[۰-۹٠-٩]/g, (d) => String(d.charCodeAt(0) % 16));
function highlight(root, q) {
  q = q && fold(q.trim());
  if (!q) return null;
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  const hits = [];
  for (let n; (n = walker.nextNode()); ) if (fold(n.nodeValue).includes(q)) hits.push(n);
  let first = null;
  for (let n of hits) {
    // shortcut: assumes toLowerCase keeps length (true for Persian and ASCII), rare Unicode may shift the mark
    for (let i; (i = fold(n.nodeValue).indexOf(q)) >= 0; ) {
      const hit = n.splitText(i);
      n = hit.splitText(q.length);
      const mark = document.createElement("mark");
      mark.textContent = hit.nodeValue;
      hit.replaceWith(mark);
      first ??= mark;
    }
  }
  return first;
}
const articles = document.querySelectorAll("article.md");
if (articles.length) {
  const q = new URLSearchParams(location.search).get("q");
  let first = null;
  for (const a of articles) first = highlight(a, q) || first;
  first?.scrollIntoView({ block: "center" });
}
addCodeButtons(document);

// T10.16: "/" search, "[" / "]" previous/next topic, "?" help. ev.code is the physical key, so it also works on a Persian layout.
document.addEventListener("keydown", (ev) => {
  if (ev.ctrlKey || ev.metaKey || ev.altKey || ev.target.closest?.("input, textarea, select, [contenteditable], dialog")) return;
  let act;
  if (ev.code === "Slash" && ev.shiftKey) act = () => document.getElementById("shortcuts")?.showModal();
  else if (ev.code === "Slash") {
    act = () => {
      const s = document.querySelector("input[type=search]");
      if (s) s.focus();
      else location.href = "/topics";
    };
  } else if (ev.code === "BracketLeft") act = () => document.querySelector('a[rel="prev"]')?.click();
  else if (ev.code === "BracketRight") act = () => document.querySelector('a[rel="next"]')?.click();
  if (!act) return;
  ev.preventDefault(); // keep "/" out of the field it focuses
  act();
});
// T10.14: topic groups the user closed stay closed (per browser; storage may be blocked).
function closedGroups() {
  try {
    return new Set(JSON.parse(localStorage.getItem("closedGroups")) || []);
  } catch {
    return new Set();
  }
}
function restoreGroups(root) {
  const closed = closedGroups();
  for (const d of root.querySelectorAll("details[data-group]")) if (closed.has(d.dataset.group)) d.open = false;
}
restoreGroups(document);
// "toggle" does not bubble: listen in the capture phase.
document.addEventListener("toggle", (ev) => {
  const id = ev.target.dataset?.group;
  if (!id) return;
  const closed = closedGroups();
  if (ev.target.open) closed.delete(id);
  else closed.add(id);
  try {
    localStorage.setItem("closedGroups", JSON.stringify([...closed]));
  } catch {}
}, true);
document.body.addEventListener("htmx:afterSwap", (ev) => {
  addCodeButtons(ev.detail.target);
  restoreGroups(document); // the search swaps #topic-results outerHTML, so target is the removed node
});

// CR-20: htmx drops non-swapped errors silently; surface them, and reload on a stale CSRF token (server restart).
function htmxFailed(ev) {
  const xhr = ev.detail.xhr;
  if (xhr && xhr.status === 403 && xhr.getResponseHeader("X-CSRF-Stale")) {
    location.reload();
    return;
  }
  const flash = document.getElementById("flash");
  if (!flash) return;
  flash.textContent = xhr && xhr.status
    ? "درخواست ناموفق بود (کد " + xhr.status + "). صفحه را تازه کنید و دوباره تلاش کنید."
    : "ارتباط با سرور برقرار نشد. مطمئن شوید برنامه در حال اجراست.";
  flash.hidden = false;
}
document.body.addEventListener("htmx:responseError", htmxFailed);
document.body.addEventListener("htmx:sendError", htmxFailed);
// hide a stale alert once a later request succeeds
document.body.addEventListener("htmx:afterRequest", (ev) => {
  if (ev.detail.successful) document.getElementById("flash")?.setAttribute("hidden", "");
});

// CR-46: Draft form: trigger htmx live update only when selection controls (rule select or checkboxes) change,
// not on text input blur/change which caused focus loss and raced merge buttons.
document.addEventListener("change", (ev) => {
  const form = ev.target.closest("#draft-form");
  if (!form) return;
  if (ev.target.matches("select, input[type='checkbox']")) {
    if (window.htmx) {
      htmx.trigger(form, "selection-change");
    } else {
      form.dispatchEvent(new CustomEvent("selection-change", { bubbles: true }));
    }
  }
});


// T9.5: a long absolute path shows only "/home/…"; scroll each path field to its end so the file name is visible.
for (const input of document.querySelectorAll(".path input")) input.scrollLeft = input.scrollWidth;
