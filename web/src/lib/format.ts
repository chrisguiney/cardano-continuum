const LOVELACE = 1_000_000;

/** ada renders lovelace as ADA, compacting past a million ADA. */
export function ada(lovelace: number): string {
  const a = lovelace / LOVELACE;
  if (a >= 1_000_000_000) return `${(a / 1_000_000_000).toFixed(2)}B ADA`;
  if (a >= 1_000_000) return `${(a / 1_000_000).toFixed(2)}M ADA`;
  return `${a.toLocaleString("en-US", { minimumFractionDigits: 2, maximumFractionDigits: 2 })} ADA`;
}

export function count(n: number): string {
  return n.toLocaleString("en-US");
}

/** simTime renders simulated milliseconds as h:mm:ss. */
export function simTime(ms: number): string {
  const s = Math.floor(ms / 1000);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${h}:${pad(m)}:${pad(s % 60)}`;
}

export function short(hash: string, n = 8): string {
  return hash.length > n ? `${hash.slice(0, n)}…` : hash;
}
