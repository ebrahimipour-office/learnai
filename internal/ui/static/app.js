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
