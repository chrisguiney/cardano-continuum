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
  /** call is what the attempt did with the contract: lock, claim, release or refund; empty when not recorded. */
  call: string;
  runs: ScriptRun[];
}

/** decisionScripts reads the validator and runs from a Submitted decision's body, or null for one without a script. */
export function decisionScripts(d: Decision): DecisionScripts | null {
  if (d.kind !== "Submitted" || typeof d.body !== "object" || d.body === null) return null;
  const b = d.body as { validator?: unknown; call?: unknown; scripts?: unknown };
  if (typeof b.validator !== "string" || b.validator === "") return null;
  const call = typeof b.call === "string" ? b.call : "";
  return { validator: b.validator, call, runs: Array.isArray(b.scripts) ? (b.scripts as ScriptRun[]) : [] };
}

/** Pipeline is a script at each phase of its compilation (internal/contract Pipeline). */
export interface Pipeline {
  uplc: string;
  flat: string;
  flat_size: number;
  wrapped: string;
  wrapped_size: number;
  tag: number;
  hash: string;
}

export interface SourceFile {
  path: string;
  text: string;
}

/** ContractDetail is /api/contracts/{name}: a validator's source and the compiler's output. */
export interface ContractDetail {
  name: string;
  toolchain: string;
  params?: string[];
  sources: SourceFile[];
  pipeline: Pipeline;
  instances: number[];
}

/** InstanceDetail is /api/contracts/{name}/instances/{n}: the validator applied to a deployed contract's nonce. */
export interface InstanceDetail {
  name: string;
  pipeline: Pipeline;
}

const reInstance = /^([a-z][a-z0-9_]{0,63})(?:#(\d{1,18}))?$/;

/** contractPath is the page of a validator or of an instance ("vesting#3"), or null for a name that is neither. */
export function contractPath(name: string): string | null {
  const m = reInstance.exec(name);
  if (!m) return null;
  return m[2] === undefined ? `/contracts/${m[1]}` : `/contracts/${m[1]}/${m[2]}`;
}

export interface PhaseRow {
  phase: string;
  /** form is what the phase holds. */
  form: string;
  size: number;
  unit: "bytes" | "characters";
  /** change says what the step from the previous phase did. */
  change: string;
}

const bytesText = (n: number) => `${n.toLocaleString("en-US")} ${n === 1 ? "byte" : "bytes"}`;

/** phaseSummary lists the four phases of p with their sizes; source is the validator's Aiken module. */
export function phaseSummary(p: Pipeline, source: string): PhaseRow[] {
  const header = p.wrapped_size - p.flat_size;
  return [
    { phase: "Aiken source", form: "Typed source text", size: new TextEncoder().encode(source).length, unit: "bytes", change: "What the author wrote, with types, comments and tests." },
    { phase: "UPLC", form: "Untyped Plutus Core, as text", size: p.uplc.length, unit: "characters", change: "The compiler checked the types and dropped them with the comments and tests, leaving a lambda-calculus term over builtins. Each variable is named for its lambda here; on chain it is a number." },
    { phase: "Flat bytes", form: "Flat-encoded program", size: p.flat_size, unit: "bytes", change: `The term tree is packed into ${bytesText(p.flat_size)}: tags and variable numbers take a few bits each, with no names or whitespace.` },
    { phase: "On-chain bytes", form: "CBOR byte string around the flat bytes", size: p.wrapped_size, unit: "bytes", change: `CBOR wraps the flat bytes in a byte-string header, adding ${bytesText(header)}. These are the bytes a witness set carries; the hash covers them after a language tag.` },
  ];
}
