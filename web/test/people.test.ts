import { describe, expect, it } from "vitest";
import { HUMAN_GLOSSARY, HUMAN_HELP } from "../src/lib/humanhelp";
import { navGroups } from "../src/lib/nav";
import { flowCurrencies, flowSeries, money, outShare, ownerLine, savingsRate, walletCounts } from "../src/lib/people";
import { ownerLabel } from "../src/lib/actorcols";
import { parseRoute } from "../src/lib/router";
import type { ActorRow, FiatFlow, HumanRow } from "../src/lib/types";

const row = (id: number, wallets: number, income = 1000, bills = 800): HumanRow => ({
  id, motivation: "hodl", tier: "mid", employed: true, income, bills, fiat: 0, held: 0, share: 0.2, wallets, lovelace: 0, purchases: 0, invested: 0,
});

const flow = (currency: string, at: number, income: number, out = 0): FiatFlow => ({
  currency, at, income, bills: 1, invested: 2, scored: 3, sold: 4, paydays: 2, out,
});

describe("people view", () => {
  it("routes /people and one human, and lists People in the menu", () => {
    expect(parseRoute("/people")).toEqual({ name: "people" });
    expect(parseRoute("/people/12")).toEqual({ name: "people", id: 12 });
    expect(parseRoute("/people/x")).toEqual({ name: "notfound" });
    expect(navGroups({ controls: false, explorer: false }).flatMap((g) => g.items.map((n) => n.path))).toContain("/people");
  });

  it("shows cents as currency units", () => {
    expect(money(123456, "EUR")).toBe("1,234.56 EUR");
    expect(money(-50)).toBe("-0.50");
  });

  it("counts humans by their number of wallets", () => {
    expect(walletCounts([row(1, 1), row(2, 3), row(3, 1)])).toEqual([[1, 2], [3, 1]]);
  });

  it("says what is left of pay after bills, never below zero", () => {
    expect(savingsRate(row(1, 1, 1000, 800))).toBeCloseTo(0.2);
    expect(savingsRate(row(1, 1, 1000, 1200))).toBe(0);
    expect(savingsRate(row(1, 1, 0, 0))).toBe(0);
  });

  it("draws a currency's flows in units and counts its paydays out of work", () => {
    const fs = [flow("USD", 300_000, 500, 1), flow("EUR", 0, 100), flow("USD", 0, 200)];
    expect(flowCurrencies(fs)).toEqual(["EUR", "USD"]);
    const s = flowSeries(fs, "USD");
    expect(s.map((x) => x.label)).toEqual(["pay", "bills paid", "bought from income", "bought by the agendas", "sold"]);
    expect(s[0]!.points).toEqual([[0, 2], [300_000, 5]]);
    expect(outShare(fs, "USD")).toBe(0.25);
    expect(outShare(fs, "JPY")).toBeNull();
  });

  it("writes a decision's owner in one line", () => {
    expect(ownerLine({ human: 4, motivation: "hodl", purpose: "savings", tier: "mid", currency: "EUR", fiat: 124000, held: 20000 }))
      .toBe("Owner: human 4 (hodl, mid tier, EUR) · wallet purpose savings · fiat 1,240.00 EUR (held for open buys 200.00 EUR).");
    expect(ownerLine({ human: 4, motivation: "yield", purpose: "staking", tier: "low" })).toBe("Owner: human 4 (yield, low tier) · wallet purpose staking.");
  });

  it("labels a wallet's owner in the actors table", () => {
    expect(ownerLabel({ owner: { human: 2, purpose: "spending" } } as ActorRow)).toBe("human 2, spending");
    expect(ownerLabel({} as ActorRow)).toBe("");
  });

  it("defines every term it uses", () => {
    for (const k of ["human", "motivation", "purpose", "wealth_tier", "fiat", "held", "share", "style", "buffer", "spread", "transfer"]) {
      expect(HUMAN_HELP[k]?.help.length).toBeGreaterThan(20);
    }
    expect(HUMAN_GLOSSARY.map((t) => t.term)).toContain("Purpose");
  });
});
