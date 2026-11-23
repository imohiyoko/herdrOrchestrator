(function () {
  "use strict";
  // /docs/view の ```mermaid を図にする。securityLevel strict(ラベル内の HTML・クリックの JS は無効)
  if (!window.mermaid) return;
  try {
    window.mermaid.initialize({startOnLoad: false, securityLevel: "strict"});
    window.mermaid.run({querySelector: "pre.mermaid"}).catch(function () {});
  } catch (x) {}
})();
