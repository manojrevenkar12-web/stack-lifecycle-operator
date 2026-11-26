export function num(x, digits = 1) {
  if (x === null || x === undefined || Number.isNaN(x)) return "–";
  const s = x.toFixed(digits);
  return s.endsWith(".0") ? s.slice(0, -2) : s;
}

export function el(tag, attrs = {}, children = []) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === "class") node.className = v;
    else if (k === "text") node.textContent = v;
    else if (k.startsWith("on")) node.addEventListener(k.slice(2), v);
    else node.setAttribute(k, v);
  }
  for (const c of [].concat(children)) if (c) node.append(c);
  return node;
}

export const svgNS = "http://www.w3.org/2000/svg";

export function svg(tag, attrs = {}) {
  const node = document.createElementNS(svgNS, tag);
  for (const [k, v] of Object.entries(attrs)) node.setAttribute(k, v);
  return node;
}

export function reducedMotion() {
  return window.matchMedia("(prefers-reduced-motion: reduce)").matches;
}
