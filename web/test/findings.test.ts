import { describe, expect, it } from "vitest";
import { groupKnown, knownSummary, reproBadge } from "../src/lib/findings";

describe("findings reproducibility badge", () => {
  it("marks a finding from a dirty build as not reproducible", () => {
    const b = reproBadge({ reproducible: false });
    expect(b).toMatchObject({ text: "not reproducible", tone: "warn" });
    expect(b?.title).toMatch(/uncommitted/);
  });

  it("shows nothing for a reproducible finding", () => {
    expect(reproBadge({ reproducible: true })).toBeNull();
  });
});

const row = (id: string, known?: string) => ({ id, known }) as import("../src/lib/types").Finding;

describe("known-issue grouping", () => {
  it("groups explained findings by issue, largest first, and leaves new ones out", () => {
    const g = groupKnown([row("a", "x#2"), row("b"), row("c", "x#1"), row("d", "x#1")]);
    expect(g.map((x) => [x.ref, x.rows.length])).toEqual([["x#1", 2], ["x#2", 1]]);
  });

  it("summarises counts by issue", () => {
    expect(knownSummary({ "x#2": 1, "x#1": 275 })).toBe("x#1: 275, x#2: 1");
    expect(knownSummary(undefined)).toBe("");
  });
});
