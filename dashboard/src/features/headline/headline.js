import { DESIGNS } from "../../lib/designs.js";
import { el, num } from "../../lib/format.js";

// The four numbers a platform owner asks about first, operator against today's process.
const TILES = [
  { key: "broken", title: "Sites left broken", unit: "per 100 upgrades", fewer: "fewer" },
  { key: "data_loss", title: "Data-loss incidents", unit: "per 100 upgrades", fewer: "fewer" },
  { key: "downtime", title: "Downtime per upgrade", unit: "minutes", fewer: "less" },
  { key: "engineer", title: "Engineer time", unit: "hours per 100 upgrades", fewer: "less" },
];

export function renderHeadline(root, result) {
  const by = Object.fromEntries(result.designs.map((d) => [d.design, d]));
  const op = by.operator;
  root.replaceChildren(
    ...TILES.map((t, i) => {
      const value = op.metrics[t.key];
      const base = by.scripted.metrics[t.key].mean;
      const helm = by.helm_atomic.metrics[t.key].mean;
      const delta = base > 0 ? Math.round((1 - value.mean / base) * 100) : 0;
      return el("article", { class: "tile", style: `--i:${i}` }, [
        el("h3", { class: "tile__title", text: t.title }),
        el("p", { class: "tile__value" }, [
          el("span", { class: "tile__number", text: num(value.mean) }),
          el("span", { class: "tile__ci", text: `± ${num(value.ci95)}` }),
        ]),
        el("p", { class: "tile__unit", text: `${DESIGNS.operator.short} · ${t.unit}` }),
        el("p", { class: "tile__compare" }, [
          el("span", { class: delta > 0 ? "tile__delta tile__delta--good" : "tile__delta", text: delta > 0 ? `${delta}% ${t.fewer}` : "no change" }),
          el("span", { text: ` than the scripted runbook (${num(base)}); helm --atomic ${num(helm)}` }),
        ]),
      ]);
    }),
  );
}
