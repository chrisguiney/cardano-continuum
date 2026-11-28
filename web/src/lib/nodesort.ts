import type { NodeRow } from "./types";

/** NodeSortKey is a Nodes table column that can be sorted. */
export type NodeSortKey = "name" | "impl" | "tier" | "phase" | "slot_lag" | "forged" | "verified";

function value(n: NodeRow, key: NodeSortKey): string | number | null {
  switch (key) {
    case "slot_lag":
      return n.slot_lag;
    case "forged":
      return n.forged;
    case "verified":
      return n.verified;
    default:
      return n[key];
  }
}

/** sortNodes orders rows by one column; rows with an unknown value (null) come last either way. It returns a new array. */
export function sortNodes(rows: NodeRow[], key: NodeSortKey, desc: boolean): NodeRow[] {
  return [...rows].sort((a, b) => {
    const x = value(a, key);
    const y = value(b, key);
    if (x === null || y === null) return x === y ? 0 : x === null ? 1 : -1;
    const c = typeof x === "number" && typeof y === "number" ? x - y : String(x).localeCompare(String(y));
    return desc ? -c : c;
  });
}
