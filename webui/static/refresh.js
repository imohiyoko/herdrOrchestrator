(function () {
  "use strict";
  var m = document.querySelector('meta[name="herdr-refresh"]');
  var sec = m ? parseInt(m.getAttribute("content"), 10) : 0;
  if (!(sec > 0)) return;
  var due = Date.now() + sec * 1000, timer = null;
  function arm() {
    clearTimeout(timer);
    if (document.hidden) return;
    timer = setTimeout(function () { if (!document.hidden) location.replace(location.href); }, Math.max(0, due - Date.now()));
  }
  document.addEventListener("visibilitychange", arm);
  arm();
})();
