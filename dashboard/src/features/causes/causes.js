import { DESIGNS, ordered } from "../../lib/designs.js";
import { el } from "../../lib/format.js";

const CAUSE = {
  config: "Config the release refuses (typo, missed rename)",
  migration: "Schema migration job failed",
  rollout: "New pods never became ready",
  health: "Component already degraded",
  "backup-space": "No room for the backup",
  "online-migration": "Writes lost: migration ran with writers up",
  "queues-not-drained": "Messages lost: broker upgraded with queued messages",
};

export function renderCauses(root, result) {
  const designs = ordered(result.designs);
  const keys = Object.keys(CAUSE).filter((k) => designs.some((d) => (d.failure_causes || {})[k]));
  if (keys.length === 0) {
    root.replaceChildren(el("p", { class: "empty", text: "No failures in this split." }));
    return;
  }
  const total = result.seeds.length * result.sites_per_seed;
  const head = el("tr", {}, [el("th", { scope: "col", text: `First thing that went wrong, ${total.toLocaleString("en")} upgrades per design` }), ...designs.map((d) => el("th", { scope: "col" }, [el("span", { class: "swatch", style: `background:${DESIGNS[d.design].color}` }), DESIGNS[d.design].short]))]);
  const rows = keys.map((k) =>
    el("tr", {}, [el("th", { scope: "row", text: CAUSE[k] }), ...designs.map((d) => el("td", { text: String((d.failure_causes || {})[k] || 0) }))]),
  );
  const blocked = el("tr", { class: "data__foot" }, [
    el("th", { scope: "row", text: "Stopped by a check before any change" }),
    ...designs.map((d) => el("td", { text: String(Object.values(d.blocked_checks || {}).reduce((a, b) => a + b, 0)) })),
  ]);
  root.replaceChildren(el("table", { class: "data" }, [el("thead", {}, head), el("tbody", {}, [...rows, blocked])]));
}
