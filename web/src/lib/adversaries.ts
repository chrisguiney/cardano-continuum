import { ada } from "./format";
import type { AdversaryCost, AdversaryRow, AdversaryView, ImpactRow } from "./types";

export interface Bar {
  pct: number;
  over: boolean;
  label: string;
}

/** costBar is the latest epoch's spend against the per-epoch ceiling; the ceiling binds each epoch, so the bar does not use the run's total. */
export function costBar(c: AdversaryCost): Bar {
  if (c.ceiling <= 0) return { pct: 0, over: false, label: `${ada(c.epoch_spent)} in epoch ${c.epoch} (ceiling unknown)` };
  const pct = Math.min(100, Math.round((c.epoch_spent / c.ceiling) * 100));
  return { pct, over: c.epoch_spent >= c.ceiling, label: `${ada(c.epoch_spent)} of ${ada(c.ceiling)} in epoch ${c.epoch}` };
}

export function outcomeCounts(a: AdversaryRow): [string, number][] {
  return [["Attempted", a.attempted], ["Landed", a.landed], ["Detected", a.detected], ["Rejected", a.rejected]];
}

const HONEST = ["honest_failure_rate", "honest_retries", "honest_latency_p50_s", "honest_latency_p99_s", "utxo_growth", "honest_value_grabbed_ada", "node_rejections", "fatal_findings"];

/** honestImpact keeps the metrics that say what the attack did to honest actors and the node. */
export function honestImpact(v: AdversaryView): ImpactRow[] {
  return v.impact.filter((r) => HONEST.includes(r.metric));
}

export const EMPTY_STATE = [
  "No adversaries in this run.",
  "Start a run with attacks: continuum run --fake --attack dust-flood,utxo-contention,malformed-cbor,front-run,double-spend (devnet only; one adversary actor per attack).",
  "For a delta against a control, run the same seed without --attack and serve the attack run with continuum serve --control CONTROL/events.log.",
  "make up runs every tier-1 attack by default; this run was deployed with ATTACKS=none or a list without them. Redeploy (make down, then make up) to change it.",
  "exunits-block-gap is opt-in: make up ATTACKS=dust-flood,exunits-block-gap also renders the genesis it needs (per-transaction ExUnits above per-block).",
];
