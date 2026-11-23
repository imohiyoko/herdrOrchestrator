(function () {
  "use strict";
  var f = document.getElementById("tfilter");
  if (!f) return;
  function go() {
    Array.prototype.forEach.call(f.elements, function (el) {
      if (el.name && el.type !== "checkbox" && el.value === "") el.disabled = true;
    });
    f.submit();
  }
  f.addEventListener("change", function (ev) {
    var t = ev.target;
    if (t && (t.name === "q" || t.name === "days")) return;
    go();
  });
  f.addEventListener("submit", function (ev) { ev.preventDefault(); go(); });
})();
