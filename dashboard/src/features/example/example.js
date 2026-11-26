import { DESIGNS, ORDER } from "../../lib/designs.js";
import { el, num } from "../../lib/format.js";

// One site whose 5.0.0 migration fails, upgraded five ways: the clearest view of the difference.
export function renderExample(root, result) {
  const ex = result.example || [];
  if (ex.length === 0) {
    root.replaceChildren(el("p", { class: "empty", text: "No example site in this split." }));
    return;
  }
  const by = Object.fromEntries(ex.map((e) => [e.design, e]));
  root.replaceChildren(
    ...ORDER.filter((k) => by[k]).map((k, i) => {
      const e = by[k];
      const broken = !e.final;
      const steps = e.rollback && e.rollback.length ? e.rollback : [];
      return el("article", { class: broken ? "case case--broken" : "case", style: `--i:${i}` }, [
        el("header", { class: "case__head" }, [
          el("span", { class: "swatch", style: `background:${DESIGNS[k].color}` }),
          el("h3", { class: "case__title", text: DESIGNS[k].short }),
        ]),
        el("p", { class: "case__outcome" }, [
          el("span", { class: broken ? "badge badge--bad" : "badge badge--good", text: broken ? "Broken: manual restore" : `Back on ${e.final}` }),
          el("span", { class: "case__downtime", text: `${num(e.downtime_minutes)} min down` }),
        ]),
        steps.length
          ? el("ol", { class: "steps" }, steps.map((s) => el("li", { class: s.startsWith("ManualRestore") ? "step step--bad" : "step", text: s })))
          : el("p", { class: "case__note", text: k === "scripted" ? "A person diagnoses, then restores by hand." : "No automatic rollback." }),
      ]);
    }),
  );
}
