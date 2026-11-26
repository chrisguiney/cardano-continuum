import { describe, expect, it } from "vitest";
import { reproBadge } from "../src/lib/findings";

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
