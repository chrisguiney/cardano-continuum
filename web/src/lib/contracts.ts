/** Script runs and the contract registry, as /api/contracts and the explorer serve them. */
import type { TxDetail } from "./extypes";
import type { Decision } from "./types";

export interface Units {
  mem: number;
  steps: number;
}

/** ScriptRun is one redeemer the engine evaluated (internal/contract Run). */
export interface ScriptRun {
  validator?: string;
  instance?: string;
  script: string;
  purpose: string;
  index: number;
  datum?: string;
  datum_json?: string;
  redeemer: string;
  redeemer_json?: string;
  declared: Units;
  used: Units;
  ok: boolean;
  error?: string;
}

export interface UnitStats {
  avg_mem: number;
  avg_steps: number;
  max_mem: number;
  max_steps: number;
}

export interface RecentRun {
  tx: string;
  at: number;
  instance?: string;
  purpose: string;
  ok: boolean;
  used: Units;
  declared: Units;
  error?: string;
}

export interface ValidatorRow {
  name: string;
  module: string;
  hash: string;
  size: number;
  language: string;
  purposes: string[];
  params?: string[];
  datum?: string;
  redeemer?: string;
  instances: number;
  runs: number;
  failures: number;
  used: UnitStats;
  declared: UnitStats;
  reasons: { reason: string; count: number }[];
  last: RecentRun[];
}

export interface ContractsView {
  toolchain: string;
  validators: ValidatorRow[];
}

/** ScriptRow is one redeemer of a transaction, with what the engine measured when it evaluated it. */
export interface ScriptRow {
  purpose: string;
  index: number;
  validator: string;
  script: string;
  declared: Units;
  used?: Units;
  ok?: boolean;
  error?: string;
  datum?: string;
  redeemer?: string;
}

/** unitsText renders execution units as "1,234 mem / 5,678 steps". */
export function unitsText(u: Units): string {
  return `${u.mem.toLocaleString("en-US")} mem / ${u.steps.toLocaleString("en-US")} steps`;
}

/**
 * scriptRows lists a transaction's redeemers from the chain, joined by
 * purpose and index with the engine's evaluation of the same transaction
 * when the run submitted it. Used units come only from that evaluation.
 */
export function scriptRows(tx: TxDetail): ScriptRow[] {
  const runs = tx.continuum?.scripts ?? [];
  return tx.redeemers.map((r) => {
    const purpose = r.purpose ?? r.tag;
    const run = runs.find((x) => x.purpose === purpose && x.index === r.index);
    const row: ScriptRow = {
      purpose, index: r.index, validator: run?.instance || run?.validator || r.validator || "", script: r.script ?? run?.script ?? "",
      declared: { mem: r.mem, steps: r.steps }, datum: r.datum_json ?? run?.datum_json ?? r.datum_hex, redeemer: r.data_json ?? run?.redeemer_json ?? r.data_hex,
    };
    if (run) {
      row.used = run.used;
      row.ok = run.ok;
      if (run.error) row.error = run.error;
    }
    return row;
  });
}

/** DecisionScripts is what a script call's Submitted record says it ran. */
export interface DecisionScripts {
  validator: string;
  runs: ScriptRun[];
}

/** decisionScripts reads the validator and runs from a Submitted decision's body, or null for one without a script. */
export function decisionScripts(d: Decision): DecisionScripts | null {
  if (d.kind !== "Submitted" || typeof d.body !== "object" || d.body === null) return null;
  const b = d.body as { validator?: unknown; scripts?: unknown };
  if (typeof b.validator !== "string" || b.validator === "") return null;
  return { validator: b.validator, runs: Array.isArray(b.scripts) ? (b.scripts as ScriptRun[]) : [] };
}
