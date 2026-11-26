import { el } from "./format.js";

export function createTooltip(host) {
  const node = el("div", { class: "tooltip", role: "status", "aria-live": "polite" });
  host.append(node);
  const place = (ev) => {
    const r = ev.currentTarget?.getBoundingClientRect?.() || { left: 0, top: 0, width: 0 };
    const x = ev.clientX ?? r.left + r.width / 2;
    const y = ev.clientY ?? r.top;
    const w = node.offsetWidth;
    node.style.transform = `translate(${Math.min(Math.max(8, x + 14), window.innerWidth - w - 8)}px, ${y + 14}px)`;
  };
  return {
    show(ev, lines) {
      node.replaceChildren(
        ...lines.filter(Boolean).map(([text, color, sub]) =>
          el("div", { class: "tooltip__line" }, [
            color ? el("span", { class: "swatch", style: `background:${color}` }) : null,
            el("span", { class: color ? "tooltip__name" : "tooltip__value", text }),
            sub ? el("span", { class: "tooltip__sub", text: ` ${sub}` }) : null,
          ]),
        ),
      );
      node.classList.add("tooltip--on");
      place(ev);
    },
    move: place,
    hide() {
      node.classList.remove("tooltip--on");
    },
  };
}
