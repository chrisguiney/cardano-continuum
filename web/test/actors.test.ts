import { describe, expect, it } from "vitest";
import { ACTOR_COLUMNS, delegationLabel } from "../src/lib/actorcols";
import { detailSections } from "../src/lib/actordetail";
import { copyText } from "../src/lib/clipboard";
import { initialPage, setFilter, toQuery } from "../src/lib/paging";
import { SORT_KEYS } from "../src/lib/paging";
import type { ActorDetail, ActorRow } from "../src/lib/types";

const row: ActorRow = {
  id: 7, name: "hodl-7", role: "active", profile: "hodl", state: "active", intents: 3, txs: 2, retries: 0, confirmed: 1, released: 0,
  kind: "wallet", address: "addr_test1qz…abcdef", balance: 12_500_000, pending: 1_000_000, tokens: 2, delegated: true, pool: "pool1abc…xyz123", drep: "always_abstain",
};

describe("actor table columns", () => {
  it("shows identity, wallet and holdings columns", () => {
    const labels = ACTOR_COLUMNS.map((c) => c.label);
    for (const want of ["ID", "Kind", "Address", "Balance", "Pending", "Tokens", "Delegation", "State"]) expect(labels).toContain(want);
  });
  it("renders each cell", () => {
    const cell = (label: string) => ACTOR_COLUMNS.find((c) => c.label === label)!.cell(row);
    expect(cell("Balance")).toBe("12.50 ADA");
    expect(cell("Pending")).toBe("1.00 ADA");
    expect(cell("Tokens")).toBe("2");
    expect(cell("Address")).toBe("addr_test1qz…abcdef");
    expect(cell("Delegation")).toBe("pool1abc…xyz123");
    expect(cell("Kind")).toBe("wallet");
  });
  it("labels an undelegated actor", () => {
    expect(delegationLabel({ ...row, delegated: false, pool: "" })).toBe("none");
  });
  it("only sorts on columns the server allowlists", () => {
    for (const c of ACTOR_COLUMNS) if (c.sort) expect(SORT_KEYS).toContain(c.sort);
    expect(SORT_KEYS).toContain("balance");
  });
  it("encodes the delegated filter", () => {
    const q = new URLSearchParams(toQuery(setFilter(initialPage(), "delegated", "true")));
    expect(q.get("delegated")).toBe("true");
    expect(toQuery(initialPage())).not.toContain("delegated");
  });
});

const detail: ActorDetail = {
  id: 7, name: "hodl-7", profile: "hodl", kind: "wallet", agenda: "hodl", has_identity: true,
  wallet: { payment_addr: "addr_test1qzfull", stake_addr: "stake_test1uzfull", payment_key_hash: "aa", stake_key_hash: "bb", drep_id: "drep1x" },
  holdings: { settled_lovelace: 5_000_000, reserved_lovelace: 1_000_000, unconfirmed_lovelace: 500_000, utxos: 2, in_flight: 1, tokens: [{ policy: "11", name: "6162", name_text: "ab", quantity: 3 }] },
  delegation: { stake_registered: true, pool: "pool1abc", drep: "always_abstain" },
  lifecycle: { state: "active", born_ms: 0, changed_ms: 5000, frustration: 0.3, hazard: 0.002 },
  traits: { competence: 0.9, care: 0.5, patience: 2, learning_rate: 0.1 },
  activity: { intents: 4, submitted: 3, confirmed: 2, refused: 1, released: 0, gave_up: 0, retries: 1, last_ms: 9000, lost_lovelace: 0, recent: [] },
  findings: [{ id: "f", seq: 1, kind: "unexpected_accept", severity: "fatal", summary: "bad" }],
  timeline: [],
};

describe("actor detail sections", () => {
  it("has the sections in reading order", () => {
    expect(detailSections(detail).map((s) => s.id)).toEqual(["identity", "wallet", "holdings", "delegation", "lifecycle", "traits", "activity", "findings"]);
  });
  it("shows a market wallet's currencies and cost basis after its holdings", () => {
    const m = { ...detail, traits: { ...detail.traits, loss_aversion: 0.7 }, market: { primary: "EUR", extra: ["USD"], basis: 0.52, units: 100_000_000, venue: "north" } };
    const s = detailSections(m);
    expect(s.map((x) => x.id).slice(0, 4)).toEqual(["identity", "wallet", "holdings", "market"]);
    const rows = Object.fromEntries(s.find((x) => x.id === "market")!.rows.map((r) => [r.label, r.value]));
    expect(rows["Primary currency"]).toBe("EUR");
    expect(rows["Other currencies"]).toBe("USD");
    expect(rows["Cost basis per ADA"]).toBe("0.5200 EUR");
    expect(rows["Last exchange"]).toBe("north");
    expect(s.find((x) => x.id === "traits")!.rows.map((r) => r.label)).toContain("Loss aversion");
  });
  it("marks addresses and ids copyable and never invents secrets", () => {
    const wallet = detailSections(detail).find((s) => s.id === "wallet")!;
    const copyable = wallet.rows.filter((r) => r.copy).map((r) => r.value);
    expect(copyable).toEqual(expect.arrayContaining(["addr_test1qzfull", "stake_test1uzfull", "aa", "bb", "drep1x"]));
  });
  it("labels settled and in-flight holdings apart", () => {
    const h = detailSections(detail).find((s) => s.id === "holdings")!;
    const labels = h.rows.map((r) => r.label);
    expect(labels).toEqual(expect.arrayContaining(["Settled", "Reserved by in-flight payments", "Unconfirmed (in flight)", "UTxOs"]));
  });
  it("notes missing identity when no store supplies it", () => {
    const s = detailSections({ ...detail, has_identity: false });
    expect(s[0]!.note).toMatch(/store/);
  });
  it("adds an adversary section when the actor attacks", () => {
    const s = detailSections({ ...detail, adversary: { spent: 3, ceiling: 10, metric: "utxo_growth" } });
    expect(s.map((x) => x.id)).toContain("adversary");
  });
});

describe("copyText", () => {
  it("uses the async clipboard when present", async () => {
    let got = "";
    await expect(copyText("x", { clipboard: { writeText: async (t) => void (got = t) }, exec: () => false })).resolves.toBe(true);
    expect(got).toBe("x");
  });
  it("falls back to the legacy copy when the clipboard is missing or refuses", async () => {
    let got = "";
    const exec = (t: string) => ((got = t), true);
    await expect(copyText("a", { exec })).resolves.toBe(true);
    await expect(copyText("b", { clipboard: { writeText: async () => Promise.reject(new Error("denied")) }, exec })).resolves.toBe(true);
    expect(got).toBe("b");
  });
  it("reports failure when neither works", async () => {
    await expect(copyText("c", { exec: () => false })).resolves.toBe(false);
  });
});
