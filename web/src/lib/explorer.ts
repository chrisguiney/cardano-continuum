import type { BlockRow, Limits, Placed } from "./extypes";

export const BLOCK_PAGE = 25;

/** blocksPath is the list endpoint for a page, optionally limited to one pool (hex id). */
export function blocksPath(offset: number, pool: string): string {
  const q = new URLSearchParams({ limit: String(BLOCK_PAGE), offset: String(offset) });
  if (/^[0-9a-f]{56}$/.test(pool)) q.set("pool", pool);
  return `/api/explorer/blocks?${q.toString()}`;
}

export const blockLink = (hash: string) => `/explorer/blocks/${hash}`;
export const txLink = (hash: string) => `/explorer/txs/${hash}`;
export const addrLink = (addr: string) => `/explorer/addresses/${addr}`;
export const poolLink = (id: string) => `/explorer/pools/${id}`;
export const epochLink = (n: number) => `/explorer/epochs/${n}`;
export const txCborPath = (hash: string) => `/api/explorer/txs/${hash}/cbor`;

export interface Usage {
  pct: number;
  text: string;
}

/** usage is how much of a per-block limit was used, as a percentage clamped for drawing. */
export function usage(used: number, limit: number): Usage {
  if (limit <= 0) return { pct: 0, text: used.toLocaleString("en-US") };
  const pct = (used / limit) * 100;
  const shown = pct > 0 && pct < 0.1 ? "<0.1" : pct.toFixed(pct < 10 ? 1 : 0);
  return { pct: Math.min(100, pct), text: `${shown}%` };
}

/** exUnitsText is a block's memory and step use against the limits, for a tooltip. */
export function exUnitsText(b: Pick<BlockRow, "ex_mem" | "ex_steps">, l: Limits): string {
  return `memory ${b.ex_mem.toLocaleString("en-US")} of ${l.max_mem.toLocaleString("en-US")}, steps ${b.ex_steps.toLocaleString("en-US")} of ${l.max_steps.toLocaleString("en-US")}`;
}

/** forkLines names each pool that has a different block at the height. */
export function forkLines(height: number, pools: Placed[]): string[] {
  return pools.filter((p) => p.state === "fork").map((p) => `${p.pool} is on a different block at height ${height}`);
}

/** stateText is the plain-language state of a pool against a block. */
export function stateText(s: Placed["state"]): string {
  switch (s) {
    case "on":
      return "has this block";
    case "fork":
      return "different block at this height";
    case "behind":
      return "has not reached this height";
    default:
      return "height is older than its window";
  }
}

/** lovelaceText is lovelace as ADA with the unit, keeping all six decimals only when they matter. */
export function lovelaceText(l: number): string {
  const whole = Math.floor(l / 1_000_000);
  const frac = l % 1_000_000;
  const f = frac === 0 ? "" : "." + String(frac).padStart(6, "0").replace(/0+$/, "");
  return `${whole.toLocaleString("en-US")}${f} ADA`;
}

const KB = 1024;
export function bytesText(n: number): string {
  return n < KB ? `${n} B` : `${(n / KB).toFixed(1)} KiB`;
}

/** metadataText renders metadata as indented JSON. */
export function metadataText(m: Record<string, unknown> | undefined): string {
  return m ? JSON.stringify(m, null, 2) : "";
}

/** cborDataUrl is a download link for hex CBOR, so the raw bytes can be saved without a second request. */
export function cborBlobParts(hex: string): Uint8Array {
  const out = new Uint8Array(Math.floor(hex.length / 2));
  for (let i = 0; i < out.length; i++) out[i] = parseInt(hex.slice(i * 2, i * 2 + 2), 16);
  return out;
}
