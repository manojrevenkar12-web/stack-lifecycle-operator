import { renderCauses } from "./features/causes/causes.js";
import { renderExample } from "./features/example/example.js";
import { renderHeadline } from "./features/headline/headline.js";
import { renderMetrics, renderTable } from "./features/metrics/metrics.js";
import { el } from "./lib/format.js";
import { createTooltip } from "./lib/tooltip.js";

const DATA_URL = "data/results.json";
const app = document.querySelector("[data-app]");
const tooltip = createTooltip(document.body);
const state = { data: null, split: null, view: "chart" };

function setStatus(status, message = "") {
  app.dataset.status = status;
  const box = document.querySelector("[data-status-box]");
  if (status === "error") {
    box.replaceChildren(
      el("h2", { text: "The results could not be loaded" }),
      el("p", { text: message }),
      el("p", { text: "Serve the dashboard folder over HTTP (python -m http.server) so the page can read data/results.json." }),
      el("button", { class: "button", type: "button", text: "Try again", onclick: load }),
    );
  } else if (status === "empty") {
    box.replaceChildren(
      el("h2", { text: "No lab results yet" }),
      el("p", {}, ["Run ", el("code", { text: "go run ./cmd/lab sim --split dev" }), " and ", el("code", { text: "go run ./cmd/lab report" }), " from the repository root."]),
    );
  } else {
    box.replaceChildren();
  }
}

async function load() {
  setStatus("loading");
  try {
    const res = await fetch(DATA_URL, { cache: "no-store" });
    if (!res.ok) throw new Error(`${DATA_URL}: HTTP ${res.status}`);
    const data = await res.json();
    const splits = ["heldout", "dev"].filter((s) => data[s] && data[s].designs && data[s].designs.length);
    if (splits.length === 0) {
      setStatus("empty");
      return;
    }
    state.data = data;
    state.split = state.split && splits.includes(state.split) ? state.split : splits[0];
    renderSplitControl(splits);
    render();
    setStatus("ready");
  } catch (err) {
    setStatus("error", err.message);
  }
}

function renderSplitControl(splits) {
  const root = document.querySelector("[data-split]");
  const label = { heldout: "Held-out seeds", dev: "Dev seeds" };
  root.replaceChildren(
    ...splits.map((s) =>
      el("button", {
        type: "button",
        class: "segment",
        "aria-pressed": String(s === state.split),
        text: label[s],
        onclick: () => {
          if (state.split === s) return;
          state.split = s;
          renderSplitControl(splits);
          render();
        },
      }),
    ),
  );
}

function render() {
  const result = state.data[state.split];
  const shortHash = result.config_sha256.slice(0, 12);
  document.querySelector("[data-scope]").textContent =
    `${result.seeds.length} seeds × ${result.sites_per_seed} sites upgraded to ${result.target} · seeds ${result.seeds[0]}–${result.seeds.at(-1)} · config/lab.yaml ${shortHash}`;
  const mix = Object.entries(result.start_mix).map(([v, n]) => `${n} on ${v}`).join(", ");
  document.querySelector("[data-mix]").textContent = `Fleet before the campaign (first seed): ${mix}.`;
  app.classList.remove("is-entering");
  void app.offsetWidth; // restart the entrance animation for the new split
  app.classList.add("is-entering");
  renderHeadline(document.querySelector("[data-headline]"), result);
  const metrics = state.data.metrics;
  const panels = document.querySelector("[data-metrics]");
  if (state.view === "table") renderTable(panels, result, metrics);
  else renderMetrics(panels, result, metrics, tooltip);
  renderCauses(document.querySelector("[data-causes]"), result);
  renderExample(document.querySelector("[data-example]"), result);
}

document.querySelector("[data-view]").addEventListener("click", (ev) => {
  const btn = ev.target.closest("button[data-value]");
  if (!btn || !state.data) return;
  state.view = btn.dataset.value;
  for (const b of ev.currentTarget.querySelectorAll("button")) b.setAttribute("aria-pressed", String(b === btn));
  render();
});

document.querySelector("[data-theme-toggle]").addEventListener("click", () => {
  const root = document.documentElement;
  const dark = root.dataset.theme ? root.dataset.theme === "dark" : window.matchMedia("(prefers-color-scheme: dark)").matches;
  root.dataset.theme = dark ? "light" : "dark";
});

const params = new URLSearchParams(location.search);
if (params.get("theme")) document.documentElement.dataset.theme = params.get("theme");
if (params.get("split")) state.split = params.get("split");
load();
