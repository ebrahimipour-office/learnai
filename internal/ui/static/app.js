// Copy-path buttons (T4.5): CSP forbids inline handlers, so one delegated listener.
document.addEventListener("click", async (ev) => {
  const btn = ev.target.closest("button[data-copy]");
  if (!btn) return;
  const old = btn.textContent;
  try {
    await navigator.clipboard.writeText(btn.dataset.copy);
    btn.textContent = "کپی شد";
  } catch {
    // clipboard blocked: select the path so Ctrl+C works
    btn.parentElement.querySelector("input")?.select();
    btn.textContent = "Ctrl+C";
  }
  setTimeout(() => { btn.textContent = old; }, 1500);
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
