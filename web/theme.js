// Runs before the first paint so the stored theme does not flash.
(function () {
  try {
    var t = localStorage.getItem("pk95mon-theme");
    if (t === "light" || t === "dark") document.documentElement.setAttribute("data-theme", t);
  } catch (e) { /* without storage the system setting applies */ }
})();
