// Copy buttons (T4.5, CR-47, T9.11): CSP forbids inline handlers, so one delegated listener.
// data-copy buttons copy their path; .copy-code buttons copy the code block right after them.
// T10.11: print buttons share this listener.
document.addEventListener("click", async (ev) => {
  if (ev.target.closest("button[data-print]")) return window.print();
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
addCodeButtons(document);
document.body.addEventListener("htmx:afterSwap", (ev) => addCodeButtons(ev.detail.target));

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
