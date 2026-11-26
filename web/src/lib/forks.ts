import type { Fork, ImplTip, PoolsView } from "./types";

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
 * forkedPools is every pool to mark: the pools of a forked implementation,
 * the pools in the server's disagreeing fork groups, and any pool the
 * server flagged itself.
 */
export function forkedPools(v: PoolsView): Set<string> {
  const impls = new Set(forkedImpls(v.tips));
  const out = new Set<string>();
  for (const p of v.pools) if (p.fork || impls.has(p.impl)) out.add(p.id);
  for (const f of v.forks) {
    if (f.groups.length < 2) continue;
    // A behind fork names the lagging pools first; the leaders are fine.
    const groups = f.reason === "behind" ? f.groups.slice(0, 1) : f.groups;
    for (const g of groups) for (const id of g.pools) out.add(id);
  }
  return out;
}

/**
 * forkLine describes one server-reported fork for the banner: the pools on
 * each branch and where they differ.
 */
export function forkLine(f: Fork): string {
  const sides = f.groups.map((g) => g.pools.join(", ")).join(" vs ");
  if (f.reason === "behind") return `${f.groups[0]?.pools.join(", ") ?? ""} behind the tip by more than k blocks`;
  const where = f.height ? `height ${f.height}` : `slot ${f.slot}`;
  const depth = f.depth && f.depth > 1 ? `, ${f.depth} blocks deep` : "";
  return `${sides} hold different blocks at ${where}${depth}`;
}
