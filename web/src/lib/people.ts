import type { FiatFlow, HumanRow, OwnerNote } from "./types";
import type { Series } from "./markets";

/** money shows cents of a currency as units with two decimals and the code. */
export function money(cents: number, currency?: string): string {
  const units = (cents / 100).toLocaleString("en-US", { minimumFractionDigits: 2, maximumFractionDigits: 2 });
  return currency ? `${units} ${currency}` : units;
}

/** ownerLine is a decision's owner note in one line, for the why box. */
export function ownerLine(o: OwnerNote): string {
  const parts = [`Owner: human ${o.human} (${o.motivation}, ${o.tier} tier${o.currency ? `, ${o.currency}` : ""})`, `wallet purpose ${o.purpose}`];
  if (o.currency) parts.push(`fiat ${money(o.fiat ?? 0, o.currency)}${o.held ? ` (held for open buys ${money(o.held, o.currency)})` : ""}`);
  return parts.join(" · ") + ".";
}

/** walletCounts is how many humans own each number of wallets, ascending. */
export function walletCounts(rows: HumanRow[]): [number, number][] {
  const by = new Map<number, number>();
  for (const r of rows) by.set(r.wallets, (by.get(r.wallets) ?? 0) + 1);
  return [...by.entries()].sort((a, b) => a[0] - b[0]);
}

/** flowCurrencies lists the currencies the fiat flows carry, in code order. */
export function flowCurrencies(fs: FiatFlow[]): string[] {
  return [...new Set(fs.map((f) => f.currency))].sort();
}

/** FLOW_LINES are the flows a fiat chart draws, with their labels. */
export const FLOW_LINES: [keyof Pick<FiatFlow, "income" | "bills" | "invested" | "scored" | "sold">, string][] = [
  ["income", "pay"],
  ["bills", "bills paid"],
  ["invested", "bought from income"],
  ["scored", "bought by the agendas"],
  ["sold", "sold"],
];

/** flowSeries is one currency's flows over time as chart lines, in units of the currency. */
export function flowSeries(fs: FiatFlow[], currency: string): Series[] {
  const mine = fs.filter((f) => f.currency === currency).sort((a, b) => a.at - b.at);
  return FLOW_LINES.map(([k, label]) => ({ label, points: mine.map((f): [number, number] => [f.at, f[k] / 100]) }));
}

/** outShare is the share of a currency's paydays spent out of work, or null without any. */
export function outShare(fs: FiatFlow[], currency: string): number | null {
  let paydays = 0;
  let out = 0;
  for (const f of fs) {
    if (f.currency !== currency) continue;
    paydays += f.paydays;
    out += f.out;
  }
  return paydays > 0 ? out / paydays : null;
}

/** savingsRate is the share of a human's monthly pay left after bills, at least zero. */
export function savingsRate(r: HumanRow): number {
  return r.income > 0 ? Math.max(0, r.income - r.bills) / r.income : 0;
}
