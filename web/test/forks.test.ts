import { describe, expect, it } from "vitest";
import { forkLine, forkedImpls, forkedNodes } from "../src/lib/forks";
import type { NodesView } from "../src/lib/types";

const tip = (impl: string, slot: number, hash: string) => ({ impl, slot, hash, nodes: 1 });

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

  const view = (over: Partial<NodesView>): NodesView => ({
    nodes: [
      { name: "a", impl: "dingo", fork: false },
      { name: "b", impl: "haskell", fork: false },
      { name: "c", impl: "haskell", fork: true },
    ] as NodesView["nodes"],
    tips: [],
    forks: [],
    topology: { mode: "mesh", nodes: [], edges: [] },
    ...over,
  });

  it("marks nodes of a forked implementation and any node the server flagged", () => {
    const v = view({ tips: [tip("dingo", 5, "aa"), tip("haskell", 5, "bb")] });
    expect([...forkedNodes(v)].sort()).toEqual(["a", "b", "c"]);
  });

  it("keeps the server's own node and group marks when tips agree", () => {
    const v = view({ tips: [tip("dingo", 5, "aa"), tip("haskell", 5, "aa")], forks: [{ slot: 5, groups: [{ hash: "x", nodes: ["a"] }, { hash: "y", nodes: ["b"] }] }] });
    expect([...forkedNodes(v)].sort()).toEqual(["a", "b", "c"]);
  });

  it("marks only the lagging nodes of a behind fork", () => {
    const v = view({ forks: [{ slot: 0, reason: "behind", groups: [{ hash: "x", nodes: ["b"] }, { hash: "y", nodes: ["a"] }] }] });
    expect([...forkedNodes(v)].sort()).toEqual(["b", "c"]);
  });

  it("describes a diverged fork by height and depth, and a behind one", () => {
    const f = { slot: 90, height: 9, depth: 3, reason: "diverged" as const, groups: [{ hash: "x", nodes: ["p0"] }, { hash: "y", nodes: ["p3"] }] };
    expect(forkLine(f)).toBe("p0 vs p3 hold different blocks at height 9, 3 blocks deep");
    expect(forkLine({ slot: 0, reason: "behind", groups: [{ hash: "x", nodes: ["p3"] }, { hash: "y", nodes: ["p0"] }] })).toBe(
      "p3 behind the tip by more than k blocks",
    );
  });
});
