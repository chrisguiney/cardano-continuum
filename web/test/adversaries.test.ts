import { describe, expect, it } from "vitest";
import { costBar, EMPTY_STATE, honestImpact, outcomeCounts } from "../src/lib/adversaries";
import { parseRoute } from "../src/lib/router";
import type { AdversaryRow, AdversaryView } from "../src/lib/types";

const adv: AdversaryRow = {
  id: 3, name: "adversary-dust-flood-3", profile: "adversary-dust-flood", attack: "dust-flood", agenda: "disrupt", budget: 5e9,
  cost: { spent: 40e6, ceiling: 300e6, epoch: 1, epoch_spent: 150e6, by_epoch: [{ epoch: 1, spent: 150e6 }] },
  metric: { name: "utxo_growth", value: 58, control: 8 },
  attempted: 4, landed: 2, detected: 1, rejected: 1, recent: [], findings: [],
};

describe("adversary cost bar", () => {
  it("shows this epoch's spend against the per-epoch ceiling", () => {
    expect(costBar(adv.cost)).toEqual({ pct: 50, over: false, label: "150.00 ADA of 300.00 ADA in epoch 1" });
  });
  it("caps the bar and flags a spend at the ceiling", () => {
    const b = costBar({ ...adv.cost, epoch_spent: 400e6 });
    expect(b.pct).toBe(100);
    expect(b.over).toBe(true);
  });
  it("copes with an unknown ceiling", () => {
    const b = costBar({ ...adv.cost, ceiling: 0 });
    expect(b.pct).toBe(0);
    expect(b.label).toMatch(/ceiling unknown/);
  });
});

describe("adversary outcomes and impact", () => {
  it("counts attempted, landed, detected and rejected", () => {
    expect(outcomeCounts(adv)).toEqual([["Attempted", 4], ["Landed", 2], ["Detected", 1], ["Rejected", 1]]);
  });
  it("picks the honest-side metrics, with deltas only against a control", () => {
    const view: AdversaryView = {
      has_control: true, adversaries: [adv],
      impact: [
        { metric: "honest_failure_rate", value: 0.2, control: 0.1, delta: 0.1 },
        { metric: "attack_steps", value: 4, control: 0, delta: 4 },
        { metric: "utxo_growth", value: 58, control: 8, delta: 50 },
      ],
    };
    expect(honestImpact(view).map((r) => r.metric)).toEqual(["honest_failure_rate", "utxo_growth"]);
    expect(honestImpact({ ...view, has_control: false, impact: view.impact.map((r) => ({ ...r, control: null, delta: null })) })[0]!.delta).toBeNull();
  });
});

describe("empty state", () => {
  it("explains how to enable adversaries, on a fake run and with make up", () => {
    const text = EMPTY_STATE.join(" ");
    expect(text).toMatch(/continuum run --fake --attack/);
    expect(text).toMatch(/make up/);
    expect(text).toMatch(/ATTACKS=none/);
    expect(text).toMatch(/exunits-block-gap/);
    expect(text).not.toMatch(/make up does not/);
  });
  it("routes /adversaries", () => {
    expect(parseRoute("/adversaries")).toEqual({ name: "adversaries" });
  });
});
