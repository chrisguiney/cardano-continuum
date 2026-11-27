import { describe, expect, it } from "vitest";
import { foldDecisions, type FeedRow } from "../src/lib/feed";
import type { Decision } from "../src/lib/types";

const d = (id: number, intent: string, actor = id, at = id * 1000): Decision => ({ id, at, kind: "decided", actor, intent });

describe("foldDecisions", () => {
  it("groups consecutive idle decisions into one row", () => {
    const rows = foldDecisions([], [d(1, "idle", 1), d(2, "idle", 2), d(3, "idle", 1)], 10, "group");
    expect(rows).toHaveLength(1);
    const r = rows[0]!;
    expect(r.type).toBe("idle");
    if (r.type !== "idle") return;
    expect(r.count).toBe(3);
    expect(r.actors).toBe(2);
    expect(r.from).toBe(1000);
    expect(r.to).toBe(3000);
  });

  it("starts a new idle group after a real intent, newest first", () => {
    let rows: FeedRow[] = foldDecisions([], [d(1, "idle"), d(2, "send")], 10, "group");
    rows = foldDecisions(rows, [d(3, "idle"), d(4, "idle")], 10, "group");
    expect(rows.map((r) => (r.type === "idle" ? `idle×${r.count}` : r.d.intent))).toEqual(["idle×2", "send", "idle×1"]);
  });

  it("extends the newest idle group across batches", () => {
    let rows = foldDecisions([], [d(1, "idle")], 10, "group");
    rows = foldDecisions(rows, [d(2, "idle")], 10, "group");
    expect(rows).toHaveLength(1);
    expect(rows[0]!.type === "idle" && rows[0]!.count).toBe(2);
  });

  it("hides idle decisions but counts them", () => {
    const rows = foldDecisions([], [d(1, "idle"), d(2, "vote"), d(3, "idle")], 10, "hide");
    expect(rows.map((r) => r.type)).toEqual(["decision"]);
  });

  it("shows every idle decision when asked", () => {
    const rows = foldDecisions([], [d(1, "idle"), d(2, "idle")], 10, "show");
    expect(rows.map((r) => r.type)).toEqual(["decision", "decision"]);
  });

  it("keeps the newest rows within the cap", () => {
    const batch = [1, 2, 3, 4, 5].map((i) => d(i, "send"));
    const rows = foldDecisions([], batch, 3, "group");
    expect(rows.map((r) => (r.type === "decision" ? r.d.id : 0))).toEqual([5, 4, 3]);
  });
});
