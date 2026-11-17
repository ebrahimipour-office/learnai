// T9.14: manual theme choice, cycling auto → light → dark. Loaded in <head> without defer so
// data-theme is on <html> before first paint (no flash); "auto" removes it and follows the system.
(() => {
  const root = document.documentElement;
  const names = { auto: "خودکار", light: "روشن", dark: "تاریک" };
  const next = { auto: "light", light: "dark", dark: "auto" };
  let mode = "auto";
  try { mode = localStorage.getItem("theme") || "auto"; } catch {} // storage blocked: stay on auto
  if (!names[mode]) mode = "auto";
  const apply = () => {
    if (mode === "auto") root.removeAttribute("data-theme");
    else root.setAttribute("data-theme", mode);
    const btn = document.querySelector("button[data-theme-toggle]");
    if (btn) btn.textContent = "تم: " + names[mode];
  };
  apply();
  document.addEventListener("DOMContentLoaded", apply);
  document.addEventListener("click", (ev) => {
    if (!ev.target.closest?.("button[data-theme-toggle]")) return;
    mode = next[mode];
    try {
      if (mode === "auto") localStorage.removeItem("theme");
      else localStorage.setItem("theme", mode);
    } catch {}
    apply();
  });
})();
