import type { ImplTip, PoolsView } from "./types";

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
  for (const f of v.forks) if (f.groups.length > 1) for (const g of f.groups) for (const id of g.pools) out.add(id);
  return out;
}
