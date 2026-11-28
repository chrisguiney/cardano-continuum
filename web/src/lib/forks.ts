import type { Fork, ImplTip, NodesView } from "./types";

/**
 * forkedImpls names the implementations whose tips disagree. Two tips
 * conflict only at the same slot with different hashes; a tip at an earlier
 * slot is lag, not a fork.
 */
export function forkedImpls(tips: ImplTip[]): string[] {
  const bySlot = new Map<number, ImplTip[]>();
  for (const t of tips) bySlot.set(t.slot, [...(bySlot.get(t.slot) ?? []), t]);
  const out = new Set<string>();
  for (const group of bySlot.values()) {
    if (new Set(group.map((t) => t.hash)).size > 1) for (const t of group) out.add(t.impl);
  }
  return [...out].sort();
}

/**
 * forkedNodes is every node to mark: the nodes of a forked implementation,
 * the nodes in the server's disagreeing fork groups, and any node the
 * server flagged itself.
 */
export function forkedNodes(v: NodesView): Set<string> {
  const impls = new Set(forkedImpls(v.tips));
  const out = new Set<string>();
  for (const n of v.nodes) if (n.fork || impls.has(n.impl)) out.add(n.name);
  for (const f of v.forks) {
    if (f.groups.length < 2) continue;
    // A behind fork names the lagging nodes first; the leaders are fine.
    const groups = f.reason === "behind" ? f.groups.slice(0, 1) : f.groups;
    for (const g of groups) for (const id of g.nodes) out.add(id);
  }
  return out;
}

/**
 * forkLine describes one server-reported fork for the banner: the nodes on
 * each branch and where they differ.
 */
export function forkLine(f: Fork): string {
  const sides = f.groups.map((g) => g.nodes.join(", ")).join(" vs ");
  if (f.reason === "behind") return `${f.groups[0]?.nodes.join(", ") ?? ""} behind the tip by more than k blocks`;
  const where = f.height ? `height ${f.height}` : `slot ${f.slot}`;
  const depth = f.depth && f.depth > 1 ? `, ${f.depth} blocks deep` : "";
  return `${sides} hold different blocks at ${where}${depth}`;
}
