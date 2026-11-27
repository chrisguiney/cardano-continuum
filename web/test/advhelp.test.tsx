import { describe, expect, it } from "vitest";
import { renderToStaticMarkup as render } from "react-dom/server";
import { ADV_GLOSSARY, ADV_HELP } from "../src/lib/advhelp";
import { honestImpact } from "../src/lib/adversaries";
import type { AdversaryRow, AdversaryView } from "../src/lib/types";
import { Adversary, Impact } from "../src/views/Adversaries";
import { Help } from "../src/views/Help";

const ATTACKS = ["dust-flood", "utxo-contention", "malformed-cbor", "front-run", "double-spend", "exunits-block-gap"];
const HONEST = ["honest_failure_rate", "honest_retries", "honest_latency_p50_s", "honest_latency_p99_s", "utxo_growth", "honest_value_grabbed_ada", "node_rejections", "fatal_findings"];

const adv = (attack: string, metric: string): AdversaryRow => ({
  id: 3, name: `adversary-${attack}-3`, profile: `adversary-${attack}`, attack, agenda: "disrupt", budget: 5e9,
  cost: { spent: 40e6, ceiling: 300e6, epoch: 1, epoch_spent: 150e6, by_epoch: [{ epoch: 1, spent: 150e6 }] },
  metric: { name: metric, value: 58, control: 8 },
  attempted: 4, landed: 2, detected: 1, rejected: 1,
  recent: [{ seq: 1, at: 1000, intent: 9, mutation: "truncate", cost: 1e6, value: 2e6, txs: 1 }], findings: [],
});

const view: AdversaryView = {
  has_control: true, adversaries: [adv("dust-flood", "utxo_growth")],
  impact: HONEST.map((metric) => ({ metric, value: 1, control: 0, delta: 1 })),
};

/** describedBy is the text a control with this label is described by. */
function describedBy(html: string, label: string): string {
  const m = new RegExp(`aria-label="About ${label}" aria-describedby="([^"]+)"`).exec(html);
  expect(m, `a tip for ${label}`).not.toBeNull();
  const t = new RegExp(`id="${m![1]}" class="tip-text">([^<]*)<`).exec(html);
  expect(t, `text for ${label}`).not.toBeNull();
  return t![1]!.replaceAll("&#x27;", "'");
}

describe("adversary definitions", () => {
  it("defines every impact metric, attack kind and outcome", () => {
    for (const k of [...HONEST, "honest_intents", "findings", "attack_steps", "attack_cost_ada", "attack_landed", "attack_detected", ...ATTACKS, "attempted", "landed", "detected", "rejected", "cost_ceiling", "success_metric", "control", "delta", "this_run"]) {
      expect(ADV_HELP[k]?.help.length ?? 0, k).toBeGreaterThan(30);
    }
  });

  it("says how each honest metric is computed", () => {
    expect(ADV_HELP.honest_failure_rate!.help).toMatch(/released over ended/);
    expect(ADV_HELP.honest_retries!.help).toMatch(/after an intent's first/);
    expect(ADV_HELP.honest_value_grabbed_ada!.help).toMatch(/shared script address/);
    expect(ADV_HELP.utxo_growth!.help).toMatch(/created less the outputs they spent/);
  });

  it("describes every metric the impact table shows, in a tip tied to its row", () => {
    const html = render(<Impact v={view} />);
    for (const r of honestImpact(view)) expect(describedBy(html, ADV_HELP[r.metric]!.label)).toBe(ADV_HELP[r.metric]!.help);
    for (const c of ["This run", "Control baseline", "Delta"]) expect(describedBy(html, c).length).toBeGreaterThan(30);
  });

  it("describes the card's terms: attack, cost ceiling, success metric, outcomes and step columns", () => {
    const html = render(<Adversary a={view.adversaries[0]!} control />);
    for (const l of ["dust-flood", "Cost ceiling", "Success metric", "Attempted", "Landed", "Detected", "Rejected", "Budget", "Mutation", "Cost", "Value grabbed", "Txs"]) {
      expect(describedBy(html, l).length, l).toBeGreaterThan(20);
    }
  });

  it("describes each attack kind", () => {
    for (const k of ATTACKS) {
      const html = render(<Adversary a={adv(k, "utxo_growth")} control />);
      expect(describedBy(html, k)).toBe(ADV_HELP[k]!.help);
    }
  });

  it("gives every tip a unique id", () => {
    const html = render(<><Adversary a={view.adversaries[0]!} control /><Impact v={view} /></>);
    const ids = [...html.matchAll(/ id="(adv-[^"]+)" class="tip-text"/g)].map((m) => m[1]);
    expect(new Set(ids).size).toBe(ids.length);
  });
});

describe("help glossary", () => {
  it("has an Adversaries section with the terms", () => {
    const html = render(<Help />);
    expect(html).toContain(">Adversaries<");
    for (const t of ["Honest failure rate", "Retries", "UTxO growth", "Value grabbed", "Landed", "Detected", "Rejected", "Attempted", "Cost ceiling", "Success metric", "Control baseline", "Delta", ...ATTACKS]) {
      expect(ADV_GLOSSARY.map((g) => g.term), t).toContain(t);
      expect(html, t).toContain(`>${t}<`);
    }
  });
});
