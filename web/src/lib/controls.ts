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
const reScenario = /^[a-z][a-z0-9_-]{0,63}$/;

/** newRequestId makes the idempotency key; a retried confirm reuses its body, so it is logged once. */
export function newRequestId(): string {
  const rand = new Uint8Array(8);
  crypto.getRandomValues(rand);
  return `ui-${Date.now().toString(36)}-${[...rand].map((b) => b.toString(16).padStart(2, "0")).join("")}`;
}

export interface ControlInput {
  k?: string;
  weights?: string;
  scenario?: string;
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
      const scenario = input.scenario ?? "";
      if (!reScenario.test(scenario)) return { ok: false, error: "scenario must be lowercase letters, digits, - or _" };
      return { ok: true, body: { request_id, op, scenario } };
    }
    case "set_weights":
      return buildWeights(request_id, input.weights ?? "");
  }
}

function buildWeights(request_id: string, text: string): Built {
  const weights: Record<string, number> = {};
  const parts = text.split(",").map((p) => p.trim()).filter(Boolean);
  if (parts.length === 0 || parts.length > MAX_WEIGHTS) return { ok: false, error: `give 1..${MAX_WEIGHTS} name=value pairs` };
  for (const p of parts) {
    const [name = "", raw = ""] = p.split("=").map((s) => s.trim());
    const v = Number(raw);
    if (!reWeightKey.test(name)) return { ok: false, error: `weight name "${name}" must be lowercase letters, digits or _` };
    if (raw === "" || !Number.isFinite(v) || v < 0 || v > MAX_WEIGHT) return { ok: false, error: `weight "${name}" must be in 0..${MAX_WEIGHT}` };
    weights[name] = v;
  }
  return { ok: true, body: { request_id, op: "set_weights", weights } };
}

/** postControl sends a built body. A 200 is a duplicate of an earlier request and is still success. */
export async function postControl(body: ControlBody): Promise<Receipt> {
  const resp = await fetch("/api/control", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  const data = (await resp.json().catch(() => ({}))) as Receipt & { error?: string };
  if (resp.status !== 200 && resp.status !== 202) throw new Error(data.error ?? `HTTP ${resp.status}`);
  return data;
}
