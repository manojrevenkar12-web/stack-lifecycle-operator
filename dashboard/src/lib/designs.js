// Each design keeps its colour wherever it appears (colour follows the entity, never its rank).
export const DESIGNS = {
  operator: { short: "Operator", color: "var(--series-operator)" },
  scripted: { short: "Scripted runbook", color: "var(--series-scripted)" },
  helm_atomic: { short: "helm --atomic", color: "var(--series-helm)" },
  operator_no_preflight: { short: "Operator, no preflight", color: "var(--series-nopreflight)" },
  operator_no_backup: { short: "Operator, no backup", color: "var(--series-nobackup)" },
};

// Display order: the operator first, then what it replaces, then the ablations.
export const ORDER = ["operator", "scripted", "helm_atomic", "operator_no_preflight", "operator_no_backup"];

export function ordered(designs) {
  return ORDER.map((key) => designs.find((d) => d.design === key)).filter(Boolean);
}
