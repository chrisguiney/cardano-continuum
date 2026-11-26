// Okabe-Ito, which stays distinguishable under the common colour-vision
// deficiencies; the drawing also varies the node shape, so colour is never
// the only cue.
const PALETTE = ["#0072b2", "#e69f00", "#009e73", "#cc79a7", "#56b4e9", "#d55e00", "#f0e442"];

export function implColors(impls: string[]): Map<string, string> {
  const sorted = [...new Set(impls)].sort();
  return new Map(sorted.map((impl, i) => [impl, PALETTE[i % PALETTE.length]!]));
}
