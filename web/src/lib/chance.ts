import type { Score } from "./types";

/**
 * clamp mirrors finite in internal/agenda/agenda.go: a negative, NaN or
 * infinite number counts as zero.
 */
export function clamp(x: number): number {
  return Number.isFinite(x) && x > 0 ? x : 0;
}

/**
 * chances returns each score's probability of being drawn: clamp(value) times
 * its recorded weight (1 when the record has none) over the sum, as Choose
 * computes it. All zeros, or a sum that overflows, mean Choose picked idle and
 * every entry gets 0.
 */
export function chances(scores: Score[]): number[] {
  const eff = scores.map((s) => clamp(s.value) * clamp(s.weight ?? 1));
  const total = eff.reduce((a, b) => a + b, 0);
  if (total <= 0 || !Number.isFinite(total)) return eff.map(() => 0);
  return eff.map((e) => e / total);
}

/** percent formats a probability with one decimal; zero is an em dash. */
export function percent(p: number): string {
  return p > 0 ? `${(p * 100).toFixed(1)}%` : "—";
}
