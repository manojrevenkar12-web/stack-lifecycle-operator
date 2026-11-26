import { DESIGNS, ordered } from "../../lib/designs.js";
import { el, num } from "../../lib/format.js";

// Small multiples: one panel per metric, one bar per design, each on its own axis from zero.
// Two measures never share an axis.

function niceMax(x) {
  if (x <= 0) return 1;
  const exp = 10 ** Math.floor(Math.log10(x));
  const f = x / exp;
  const step = f <= 1 ? 1 : f <= 2 ? 2 : f <= 2.5 ? 2.5 : f <= 5 ? 5 : 10;
  return step * exp;
}

export function renderMetrics(root, result, metrics, tooltip) {
  const designs = ordered(result.designs);
  const panels = metrics.map((m, pi) => {
    const max = niceMax(Math.max(...designs.map((d) => d.metrics[m.key].mean + d.metrics[m.key].ci95)));
    const unit = m.unit === "per 100" ? "per 100 site upgrades" : m.unit;
    const ticks = [0, max / 2, max];
    const rows = designs.map((d, ri) => {
      const s = d.metrics[m.key];
      const share = s.mean / max;
      const lo = Math.max(0, (s.mean - s.ci95) / max);
      const hi = Math.min(1, (s.mean + s.ci95) / max);
      const meta = DESIGNS[d.design];
      const extra = m.key === "downtime" ? `worst 5% of upgrades: ${num(d.downtime_p95_minutes)} min` : "";
      const row = el(
        "div",
        {
          class: d.design === "operator" ? "row row--hero" : "row",
          tabindex: "0",
          role: "listitem",
          "aria-label": `${meta.short}: ${num(s.mean)} ± ${num(s.ci95)} ${unit}`,
          style: `--i:${pi * 2 + ri}`,
        },
        [
          el("span", { class: "row__label" }, [
            el("span", { class: "swatch", style: `background:${meta.color}` }),
            el("span", { text: meta.short }),
          ]),
          el("span", { class: "row__track" }, [
            el("span", { class: "row__bar", style: `--share:${share};background:${meta.color}` }),
            s.ci95 > 0
              ? el("span", { class: "row__whisker", style: `--lo:${lo};--hi:${hi}`, "aria-hidden": "true" })
              : null,
          ]),
          el("span", { class: "row__value", text: num(s.mean) }),
        ],
      );
      const show = (ev) =>
        tooltip.show(ev, [
          [meta.short, meta.color],
          [`${num(s.mean)} ± ${num(s.ci95)}`, null, unit],
          extra ? [extra] : null,
        ]);
      row.addEventListener("pointerenter", show);
      row.addEventListener("pointermove", (ev) => tooltip.move(ev));
      row.addEventListener("pointerleave", () => tooltip.hide());
      row.addEventListener("focus", show);
      row.addEventListener("blur", () => tooltip.hide());
      return row;
    });
    return el("article", { class: "panel" }, [
      el("header", { class: "panel__head" }, [
        el("h3", { class: "panel__title", text: m.label }),
        el("p", { class: "panel__sub", text: m.better === "context" ? `${unit} · read next to failed steps` : `${unit} · ${m.better} is better` }),
      ]),
      el("div", { class: "panel__plot" }, [
        el(
          "div",
          { class: "grid", "aria-hidden": "true" },
          ticks.map((t) => el("span", { class: "grid__line", style: `--at:${t / max}` }, [el("span", { class: "grid__tick", text: num(t) })])),
        ),
        el("div", { class: "rows", role: "list" }, rows),
      ]),
    ]);
  });
  root.replaceChildren(...panels);
}

export function renderTable(root, result, metrics) {
  const designs = ordered(result.designs);
  const head = el("tr", {}, [el("th", { scope: "col", text: "Per 100 site upgrades" }), ...designs.map((d) => el("th", { scope: "col", text: DESIGNS[d.design].short }))]);
  const body = metrics.map((m) =>
    el("tr", {}, [
      el("th", { scope: "row", text: m.unit === "per 100" ? m.label : `${m.label} (${m.unit})` }),
      ...designs.map((d) => el("td", { text: `${num(d.metrics[m.key].mean)} ± ${num(d.metrics[m.key].ci95)}` })),
    ]),
  );
  root.replaceChildren(el("table", { class: "data" }, [el("thead", {}, head), el("tbody", {}, body)]));
}
