import { describe, expect, it } from "vitest";
import { bids, bounds, currencies, flowRows, indexed, latestMid, linePath, positionLine, splitShare } from "../src/lib/markets";
import { navFor } from "../src/lib/nav";
import { parseRoute } from "../src/lib/router";
import type { MarketPoint, MarketsView } from "../src/lib/types";

const q = (at: number, usd: number, eur: number): MarketPoint => ({
  at, ada: usd, mids: { USD: usd, EUR: eur }, venues: { "north/USD": [usd * 0.99, usd * 1.01], "summit/EUR": [eur * 0.98, eur * 1.02] },
});

describe("markets view", () => {
  it("routes /markets and lists it in the menu", () => {
    expect(parseRoute("/markets")).toEqual({ name: "markets" });
    expect(navFor({ controls: false, explorer: false }).map((n) => n[0])).toContain("/markets");
  });

  it("rebases each currency to 100 at its first quote", () => {
    const qs = [q(1, 0.5, 0.45), q(2, 1, 0.9)];
    expect(currencies(qs)).toEqual(["EUR", "USD"]);
    const s = indexed(qs, ["USD"]);
    expect(s[0]!.points).toEqual([[1, 100], [2, 200]]);
  });

  it("lists each exchange's bid in a currency", () => {
    const s = bids([q(1, 1, 1)], "USD");
    expect(s.map((x) => x.label)).toEqual(["north"]);
    expect(s[0]!.points[0]![1]).toBeCloseTo(0.99);
  });

  it("totals flows by exchange", () => {
    const rows = flowRows([
      { exchange: "north", currency: "USD", side: "buy", trades: 2, lovelace: 10, fiat: 5 },
      { exchange: "north", currency: "EUR", side: "sell", trades: 1, lovelace: 4, fiat: 2 },
      { exchange: "harbor", currency: "JPY", side: "buy", trades: 1, lovelace: 3, fiat: 1 },
    ]);
    expect(rows).toEqual([
      { exchange: "harbor", buys: 1, sells: 0, bought: 3, sold: 0 },
      { exchange: "north", buys: 2, sells: 1, bought: 10, sold: 4 },
    ]);
  });

  it("draws a line inside its viewport and keeps a flat one drawable", () => {
    const s = [{ label: "a", points: [[0, 1], [10, 3]] as [number, number][] }];
    const b = bounds(s)!;
    expect(linePath(s[0]!.points, b, 100, 50)).toBe("M0.0,50.0L100.0,0.0");
    const flat = bounds([{ label: "f", points: [[0, 2], [1, 2]] }])!;
    expect(flat.y1).toBeGreaterThan(flat.y0);
    expect(bounds([])).toBeNull();
  });

  it("reads the latest mid, the position and the sell share", () => {
    const v: MarketsView = { quotes: [q(1, 0.5, 0.4), q(2, 0.6, 0.5)], flows: [], births: {}, drawdown: { peak_at: 0, trough_at: 0, high: 0, low: 0, depth: 0, hodlers: 3, sellers: 1 } };
    expect(latestMid(v, "EUR")).toBe(0.5);
    expect(latestMid(v, "JPY")).toBeNull();
    expect(positionLine({ currency: "EUR", gain: -0.2, trend: 0.015 })).toBe("In EUR: -20.0% on cost, ADA +1.5% over the window.");
    expect(splitShare(3, 1)).toBe(0.25);
    expect(splitShare(0, 0)).toBeNull();
  });
});
