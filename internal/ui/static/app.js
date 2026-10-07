// ynr's local dashboard: charts from data-chart, folding in the trace waterfall, and a bounded
// live tail. The only script of ynr's own (ADR-005).
(function () {
  "use strict";

  function charts(root) {
    if (!window.echarts) return;
    root.querySelectorAll("[data-chart]").forEach(function (el) {
      var dark = window.matchMedia("(prefers-color-scheme: dark)").matches;
      var c = echarts.getInstanceByDom(el) || echarts.init(el, dark ? "dark" : null, { renderer: "svg" });
      var option = JSON.parse(el.getAttribute("data-chart"));
      option.backgroundColor = "transparent";
      c.setOption(option, true);
    });
  }

  // Clicking a span folds or unfolds the spans beneath it: the rows that follow while deeper.
  function fold(row) {
    var depth = +row.dataset.depth;
    var folding = !row.classList.contains("folded");
    row.classList.toggle("folded", folding);
    for (var r = row.nextElementSibling; r && +r.dataset.depth > depth; r = r.nextElementSibling) {
      r.hidden = folding;
      if (!folding) r.classList.remove("folded");
    }
  }

  document.addEventListener("click", function (e) {
    var row = e.target.closest && e.target.closest(".span");
    if (row) fold(row);
  });
  document.addEventListener("keydown", function (e) {
    if ((e.key === "Enter" || e.key === " ") && e.target.classList && e.target.classList.contains("span")) {
      e.preventDefault();
      fold(e.target);
    }
  });

  // The live tail keeps its newest 500 rows.
  document.addEventListener("htmx:sseMessage", function () {
    var body = document.getElementById("tail");
    while (body && body.rows.length > 500) body.deleteRow(-1);
  });

  document.addEventListener("DOMContentLoaded", function () { charts(document); });
  document.addEventListener("htmx:afterSwap", function (e) { charts(e.target); });
  window.addEventListener("resize", function () {
    document.querySelectorAll("[data-chart]").forEach(function (el) {
      var c = window.echarts && echarts.getInstanceByDom(el);
      if (c) c.resize();
    });
  });
})();
