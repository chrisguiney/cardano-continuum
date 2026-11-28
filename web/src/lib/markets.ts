import type { MarketFlow, MarketPoint, MarketsView, Position } from "./types";

/** currencies lists the currencies the quotes carry, in code order. */
export function currencies(qs: MarketPoint[]): string[] {
  const out = new Set<string>();
  for (const q of qs) for (const c of Object.keys(q.mids)) out.add(c);
  return [...out].sort();
}

/** exchanges lists the exchanges quoting a currency, in name order. */
export function exchanges(qs: MarketPoint[], currency: string): string[] {
  const out = new Set<string>();
  for (const q of qs) {
    for (const k of Object.keys(q.venues)) {
      const [x, c] = k.split("/");
      if (c === currency && x) out.add(x);
    }
  }
  return [...out].sort();
}

/** Series is one line of a chart: its label and its (time, value) points. */
export interface Series {
  label: string;
  points: [number, number][];
}

/** indexed is each currency's mid rebased to 100 at its first quote, so currencies of any size share one axis. */
export function indexed(qs: MarketPoint[], codes: string[]): Series[] {
  return codes.map((c) => {
    const first = qs.find((q) => q.mids[c] !== undefined)?.mids[c];
    const points: [number, number][] = [];
    for (const q of qs) {
      const m = q.mids[c];
      if (m !== undefined && first) points.push([q.at, (100 * m) / first]);
    }
    return { label: c, points };
  });
}

/** bids is each exchange's bid for ADA in a currency over time. */
export function bids(qs: MarketPoint[], currency: string): Series[] {
  return exchanges(qs, currency).map((x) => ({
    label: x,
    points: qs.flatMap((q): [number, number][] => {
      const v = q.venues[`${x}/${currency}`];
      return v ? [[q.at, v[0]]] : [];
    }),
  }));
}

/** FlowRow is one exchange's settled buys and sells. */
export interface FlowRow {
  exchange: string;
  buys: number;
  sells: number;
  bought: number;
  sold: number;
}

/** flowRows totals the flows by exchange, in name order. */
export function flowRows(fs: MarketFlow[]): FlowRow[] {
  const by = new Map<string, FlowRow>();
  for (const f of fs) {
    const r = by.get(f.exchange) ?? { exchange: f.exchange, buys: 0, sells: 0, bought: 0, sold: 0 };
    if (f.side === "buy") {
      r.buys += f.trades;
      r.bought += f.lovelace;
    } else {
      r.sells += f.trades;
      r.sold += f.lovelace;
    }
    by.set(f.exchange, r);
  }
  return [...by.values()].sort((a, b) => a.exchange.localeCompare(b.exchange));
}

/** Box is a chart's data range. */
export interface Box {
  x0: number;
  x1: number;
  y0: number;
  y1: number;
}

/** bounds is the range of every series' points, widened so a flat line still has height. */
export function bounds(ss: Series[]): Box | null {
  const pts = ss.flatMap((s) => s.points);
  if (pts.length === 0) return null;
  const xs = pts.map((p) => p[0]);
  const ys = pts.map((p) => p[1]);
  let y0 = Math.min(...ys);
  let y1 = Math.max(...ys);
  if (y1 - y0 < 1e-9 * Math.max(1, Math.abs(y1))) {
    y0 -= 1;
    y1 += 1;
  }
  return { x0: Math.min(...xs), x1: Math.max(...xs), y0, y1 };
}

/** linePath is the SVG path of points in box, drawn into a w by h viewport with y up. */
export function linePath(points: [number, number][], box: Box, w: number, h: number): string {
  const sx = (x: number) => (box.x1 === box.x0 ? 0 : ((x - box.x0) / (box.x1 - box.x0)) * w);
  const sy = (y: number) => h - ((y - box.y0) / (box.y1 - box.y0)) * h;
  return points.map(([x, y], i) => `${i ? "L" : "M"}${sx(x).toFixed(1)},${sy(y).toFixed(1)}`).join("");
}

/** latestMid is the newest mid of a currency, or null. */
export function latestMid(v: MarketsView | null, currency: string): number | null {
  const qs = v?.quotes ?? [];
  for (let i = qs.length - 1; i >= 0; i--) {
    const m = qs[i]!.mids[currency];
    if (m !== undefined) return m;
  }
  return null;
}

/** positionLine says where a decision's market position stood. */
export function positionLine(p: Position): string {
  const sign = (x: number) => `${x >= 0 ? "+" : ""}${(100 * x).toFixed(1)}%`;
  return `In ${p.currency}: ${sign(p.gain)} on cost, ADA ${sign(p.trend)} over the window.`;
}

/** splitShare is the share of the wallets under water in the drawdown that sold, or null when none decided there. */
export function splitShare(hodlers: number, sellers: number): number | null {
  const n = hodlers + sellers;
  return n > 0 ? sellers / n : null;
}

const DASHES = [undefined, "6 3", "2 3", "8 3 2 3", "1 2"];

/** dashFor is the dash pattern of line i: the colours repeat every three lines, so each lap of them gets its own dashes. */
export function dashFor(i: number, colours: number): string | undefined {
  return DASHES[Math.floor(i / colours) % DASHES.length];
}
