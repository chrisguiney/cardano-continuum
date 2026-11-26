import { describe, expect, it } from "vitest";
import { forkLine, forkedImpls, forkedPools } from "../src/lib/forks";
import type { PoolsView } from "../src/lib/types";

const tip = (impl: string, slot: number, hash: string) => ({ impl, slot, hash, pools: 1 });

describe("fork detection from per-implementation tips", () => {
  it("sees no fork when implementations agree", () => {
    expect(forkedImpls([tip("dingo", 10, "aa"), tip("haskell", 10, "aa")])).toEqual([]);
  });

  it("flags implementations that disagree on the hash at one slot", () => {
    expect(forkedImpls([tip("dingo", 10, "aa"), tip("haskell", 10, "bb"), tip("amaru", 10, "aa")])).toEqual(["amaru", "dingo", "haskell"]);
  });

  it("treats different slots as lag, not a fork", () => {
    expect(forkedImpls([tip("dingo", 11, "aa"), tip("haskell", 10, "bb")])).toEqual([]);
  });

  it("handles one implementation and none", () => {
    expect(forkedImpls([tip("dingo", 1, "aa")])).toEqual([]);
    expect(forkedImpls([])).toEqual([]);
  });

  const view = (over: Partial<PoolsView>): PoolsView => ({
    pools: [
      { id: "a", impl: "dingo", fork: false },
      { id: "b", impl: "haskell", fork: false },
      { id: "c", impl: "haskell", fork: true },
    ] as PoolsView["pools"],
    tips: [],
    forks: [],
    topology: { mode: "mesh", nodes: [], edges: [] },
    ...over,
  });

  it("marks pools of a forked implementation and any pool the server flagged", () => {
    const v = view({ tips: [tip("dingo", 5, "aa"), tip("haskell", 5, "bb")] });
    expect([...forkedPools(v)].sort()).toEqual(["a", "b", "c"]);
  });

  it("keeps the server's own pool and group marks when tips agree", () => {
    const v = view({ tips: [tip("dingo", 5, "aa"), tip("haskell", 5, "aa")], forks: [{ slot: 5, groups: [{ hash: "x", pools: ["a"] }, { hash: "y", pools: ["b"] }] }] });
    expect([...forkedPools(v)].sort()).toEqual(["a", "b", "c"]);
  });

  it("marks only the lagging pools of a behind fork", () => {
    const v = view({ forks: [{ slot: 0, reason: "behind", groups: [{ hash: "x", pools: ["b"] }, { hash: "y", pools: ["a"] }] }] });
    expect([...forkedPools(v)].sort()).toEqual(["b", "c"]);
  });

  it("describes a diverged fork by height and depth, and a behind one", () => {
    const f = { slot: 90, height: 9, depth: 3, reason: "diverged" as const, groups: [{ hash: "x", pools: ["p0"] }, { hash: "y", pools: ["p3"] }] };
    expect(forkLine(f)).toBe("p0 vs p3 hold different blocks at height 9, 3 blocks deep");
    expect(forkLine({ slot: 0, reason: "behind", groups: [{ hash: "x", pools: ["p3"] }, { hash: "y", pools: ["p0"] }] })).toBe(
      "p3 behind the tip by more than k blocks",
    );
  });
});
