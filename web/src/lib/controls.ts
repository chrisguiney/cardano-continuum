import type { Receipt } from "./types";

export type Op = "pause" | "resume" | "set_k" | "set_weights" | "inject";

export interface ControlBody {
  request_id: string;
  op: Op;
  k?: number;
  weights?: Record<string, number>;
  scenario?: string;
}

export type Built = { ok: true; body: ControlBody } | { ok: false; error: string };

// Bounds mirror internal/httpapi/control.go; the server stays the authority.
const MAX_K = 1000;
const MAX_WEIGHT = 1000;
const MAX_WEIGHTS = 64;
const reWeightKey = /^[a-z][a-z0-9_]{0,31}$/;
const reScenario = /^[a-z][a-z0-9_-]{0,63}(:[A-Za-z0-9._/:@-]{1,255})?$/;

/** newRequestId makes the idempotency key; a retried confirm reuses its body, so it is logged once. */
export function newRequestId(): string {
  const rand = new Uint8Array(8);
  crypto.getRandomValues(rand);
  return `ui-${Date.now().toString(36)}-${[...rand].map((b) => b.toString(16).padStart(2, "0")).join("")}`;
}

export interface ControlInput {
  k?: string;
  /** weights holds one form value per intent kind; an empty one is left out. */
  weights?: Record<string, string>;
  scenario?: string;
  /** arg is the scenario's argument, joined to it with a colon. */
  arg?: string;
}

export function buildControl(op: Op, input: ControlInput): Built {
  const request_id = newRequestId();
  switch (op) {
    case "pause":
    case "resume":
      return { ok: true, body: { request_id, op } };
    case "set_k": {
      const k = Number(input.k);
      if (!Number.isInteger(k) || k < 1 || k > MAX_K) return { ok: false, error: `k must be an integer in 1..${MAX_K}` };
      return { ok: true, body: { request_id, op, k } };
    }
    case "inject": {
      const arg = (input.arg ?? "").trim();
      const scenario = arg ? `${input.scenario ?? ""}:${arg}` : (input.scenario ?? "");
      if (!reScenario.test(scenario)) return { ok: false, error: "pick a scenario; an argument may hold letters, digits and . _ / : @ - only" };
      return { ok: true, body: { request_id, op, scenario } };
    }
    case "set_weights":
      return buildWeights(request_id, input.weights ?? {});
  }
}

function buildWeights(request_id: string, form: Record<string, string>): Built {
  const weights: Record<string, number> = {};
  for (const [name, text] of Object.entries(form)) {
    const raw = text.trim();
    if (raw === "") continue;
    const v = Number(raw);
    if (!reWeightKey.test(name)) return { ok: false, error: `weight name "${name}" must be lowercase letters, digits or _` };
    if (!Number.isFinite(v) || v < 0 || v > MAX_WEIGHT) return { ok: false, error: `weight "${name}" must be a number in 0..${MAX_WEIGHT}` };
    weights[name] = v;
  }
  const n = Object.keys(weights).length;
  if (n === 0 || n > MAX_WEIGHTS) return { ok: false, error: `set 1..${MAX_WEIGHTS} weights` };
  return { ok: true, body: { request_id, op: "set_weights", weights } };
}

/** postControl sends a built body. A 200 is a duplicate of an earlier request and is still success. */
export async function postControl(body: ControlBody): Promise<Receipt> {
  const resp = await fetch("/api/control", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  const data = (await resp.json().catch(() => ({}))) as Receipt & { error?: string };
  if (resp.status !== 200 && resp.status !== 202) throw new Error(data.error ?? `HTTP ${resp.status}`);
  return data;
}
