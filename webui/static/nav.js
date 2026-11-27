(function () {
  "use strict";
  // 縦タブ(サイドバー)の開閉。状態はブラウザ(localStorage)に覚える。<head> で読み、描く前にクラスを付ける。
  var KEY = "herdr-nav-collapsed", root = document.documentElement;
  function get() { try { return localStorage.getItem(KEY) === "1"; } catch (e) { return false; } }
  function set(v) { try { localStorage.setItem(KEY, v ? "1" : "0"); } catch (e) {} }
  function show(v) {
    root.classList.toggle("navc", v);
    var b = document.getElementById("navtoggle");
    if (b) {
      b.textContent = v ? "»" : "«";
      b.title = v ? "タブを開く" : "タブを畳む";
      b.setAttribute("aria-expanded", v ? "false" : "true");
    }
  }
  show(get());
  document.addEventListener("DOMContentLoaded", function () {
    var b = document.getElementById("navtoggle");
    if (!b) return;
    b.hidden = false;
    show(get());
    b.addEventListener("click", function () { var v = !get(); set(v); show(v); });
  });
})();
