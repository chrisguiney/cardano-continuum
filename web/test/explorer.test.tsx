import { describe, expect, it } from "vitest";
import { renderToStaticMarkup as render } from "react-dom/server";
import { navFor } from "../src/lib/nav";
import { parseRoute } from "../src/lib/router";
import { blocksPath, forkLines, lovelaceText, usage } from "../src/lib/explorer";
import { runSearch } from "../src/lib/search";
import { GLOSSARY } from "../src/lib/help";
import { Help } from "../src/views/Help";
import { BlockTable } from "../src/views/Explorer";
import { BlockBody } from "../src/views/ExBlock";
import { TxBody } from "../src/views/ExTx";
import { AddressBody } from "../src/views/ExAddress";
import { EpochBody } from "../src/views/ExEpoch";
import { SearchBox } from "../src/views/SearchBox";
import type { AddressDetail, BlockDetail, BlockPage, EpochView, TxDetail } from "../src/lib/extypes";

const H1 = "a".repeat(64);
const H2 = "b".repeat(64);
const ISSUER = "c".repeat(56);
const ADDR = "addr_test1qpmtp5t0t5y6cqkaz7rfsyrx7mld77kpvksgkwm0p7en7qum7a589n30e80tclq0dvpn8m9p5g6z07j0mncc8ru5jqs6nwn8q";

const limits = { max_mem: 14_000_000, max_steps: 10_000_000_000, max_bytes: 90112 };
const row = { height: 7, slot: 70, epoch: 1, time_ms: 70_000, hash: H1, prev: H2, issuer: ISSUER, txs: 2, size: 2048, fees: 340_000, ex_mem: 1_400_000, ex_steps: 100_000_000 };

describe("top menu", () => {
  it("ends with Help however many views the server enables", () => {
    for (const controls of [false, true]) {
      for (const explorer of [false, true]) {
        const nav = navFor({ controls, explorer });
        expect(nav[nav.length - 1]).toEqual(["/help", "Help"]);
        expect(nav.some(([p]) => p === "/explorer")).toBe(explorer);
        expect(nav.some(([p]) => p === "/controls")).toBe(controls);
        expect(new Set(nav.map(([p]) => p)).size).toBe(nav.length);
        expect(nav).toContainEqual(["/nodes", "Nodes"]);
        expect(nav).toContainEqual(["/pools", "Stake pools"]);
      }
    }
  });
});

describe("explorer routes", () => {
  it("parse each view and refuse a malformed argument", () => {
    expect(parseRoute("/explorer")).toEqual({ name: "explorer" });
    expect(parseRoute(`/explorer/blocks/${H1}`)).toEqual({ name: "xblock", key: H1 });
    expect(parseRoute("/explorer/blocks/12")).toEqual({ name: "xblock", key: "12" });
    expect(parseRoute(`/explorer/txs/${H2}`)).toEqual({ name: "xtx", key: H2 });
    expect(parseRoute(`/explorer/addresses/${ADDR}`)).toEqual({ name: "xaddr", key: ADDR });
    expect(parseRoute("/explorer/epochs/3")).toEqual({ name: "xepoch", n: 3 });
    expect(parseRoute(`/explorer/pools/${ISSUER}`)).toEqual({ name: "xpool", key: ISSUER });
    for (const bad of ["/explorer/blocks/zz", "/explorer/txs/12", "/explorer/addresses/../x", "/explorer/epochs/x", "/explorer/pools/abc", "/explorer/nope/1"]) {
      expect(parseRoute(bad).name, bad).toBe("notfound");
    }
  });
});

describe("explorer helpers", () => {
  it("builds a bounded list path and keeps only a well-formed pool filter", () => {
    expect(blocksPath(25, "")).toBe("/api/explorer/blocks?limit=25&offset=25");
    expect(blocksPath(0, ISSUER)).toContain(`pool=${ISSUER}`);
    expect(blocksPath(0, "evil&x=1")).not.toContain("evil");
  });
  it("shows use against a limit", () => {
    expect(usage(50, 100)).toEqual({ pct: 50, text: "50%" });
    expect(usage(0, 100).text).toBe("0.0%");
    expect(usage(1, 100000).text).toBe("<0.1%");
    expect(usage(300, 100).pct).toBe(100);
    expect(usage(5, 0).pct).toBe(0);
  });
  it("names the fork", () => {
    expect(forkLines(6, [{ pool: "p1", state: "on", on_block: true }, { pool: "p3", state: "fork", on_block: false }])).toEqual(["p3 is on a different block at height 6"]);
  });
  it("renders lovelace", () => {
    expect(lovelaceText(2_500_000)).toBe("2.5 ADA");
    expect(lovelaceText(1_000_000_000)).toBe("1,000 ADA");
  });
});

describe("blocks list", () => {
  const page: BlockPage = { total: 1, offset: 0, limit: 25, limits, rows: [{ ...row, pool: { name: "p1", impl: "dingo" } }, { ...row, hash: H2, height: 6, issuer: "d".repeat(56) }] };
  const html = render(<BlockTable page={page} />);
  it("shows each column, resolving a continuum pool by name and implementation", () => {
    for (const t of ["Height", "Slot", "Epoch", "Time", "Hash", "Issuer", "Txs", "Size", "Fees", "ExUnits used"]) expect(html).toContain(`>${t}<`);
    expect(html).toContain("p1");
    expect(html).toContain("(dingo)");
    expect(html).toContain("0:01:10");
  });
  it("links hashes, pools and epochs, with a copy button", () => {
    expect(html).toContain(`href="/explorer/blocks/${H1}"`);
    expect(html).toContain(`href="/pools/${ISSUER}"`);
    expect(html).toContain('href="/nodes/p1"');
    expect(html).toContain('href="/explorer/epochs/1"');
    expect(html).toContain('aria-label="Copy block hash"');
  });
  it("shows ExUnits as a labelled percentage of the limit", () => {
    expect(html).toContain("1.0%");
    expect(html).toContain("memory: 10% of the block limit used");
    expect(html).toContain("steps: 1.0% of the block limit used");
    expect(html).toContain("14,000,000");
  });
  it("says so when empty", () => {
    expect(render(<BlockTable page={{ ...page, rows: [] }} />)).toContain("No blocks indexed yet");
  });
});

describe("block detail", () => {
  const b: BlockDetail = {
    ...row, txs: [{ hash: H2, fee: 170_000 }], next: "e".repeat(64), evicted: false, stub: false, fork: true,
    header: { proto_major: 10, proto_minor: 0, body_hash: "f".repeat(64), body_size: 1024, issuer_vkey: "1".repeat(64), vrf_key: "2".repeat(64), opcert_hot_vkey: "3".repeat(64), opcert_seq: 4, kes_period: 5 },
    pools: [{ pool: "p1", state: "on", on_block: true, hash: H1 }, { pool: "p3", state: "fork", on_block: false, hash: "9".repeat(64) }],
  };
  const html = render(<BlockBody b={b} />);
  it("shows the header fields", () => {
    for (const t of ["Protocol version", "10.0", "Body hash", "VRF key", "Operational certificate", "sequence 4, KES period 5"]) expect(html).toContain(t);
  });
  it("lists transactions as links", () => {
    expect(html).toContain(`href="/explorer/txs/${H2}"`);
  });
  it("shows a fork as another node on a different block, linking its block and the node", () => {
    expect(html).toContain("p3 is on a different block at height 7");
    expect(html).toContain('href="/nodes/p3"');
    expect(html).toContain("Nodes on this block");
    expect(html).toContain(`href="/explorer/blocks/${"9".repeat(64)}"`);
    expect(html).toContain("different block at this height");
  });
  it("explains a stub block", () => {
    expect(render(<BlockBody b={{ ...b, stub: true, header: undefined as never }} />)).toContain("not in the index");
  });
});

const tx: TxDetail = {
  hash: H2, block: H1, height: 7, slot: 70, index: 0, evicted: false, valid: false, size: 512, fee: 300_000, ttl: 10_000,
  inputs: [{ tx: H1, index: 0, resolved: { index: 0, address: ADDR, lovelace: 20_000_000 } }, { tx: H2, index: 3 }],
  outputs: [{ tx: H2, index: 0, address: ADDR, lovelace: 3_000_000, assets: [{ policy: "33".repeat(28), name: "746f6b", name_text: "tok", quantity: "7" }], datum: { kind: "inline", hash: "44".repeat(32), hex: "d87980" }, script_ref: { hash: "55".repeat(28), language: "plutus_v2", size: 8 } }],
  reference_inputs: [{ tx: H1, index: 1 }], collateral: [], mint: [{ policy: "33".repeat(28), name: "746f6b", name_text: "tok", quantity: "-7" }],
  certificates: [{ kind: "stake_delegation", credential: "66".repeat(28), pool: "77".repeat(28) }, { kind: "drep_registration", amount: 500_000_000 }],
  withdrawals: [], required_signers: ["88".repeat(28)], metadata: { "674": { msg: ["hello"] } },
  redeemers: [{ tag: "spend", index: 0, mem: 1000, steps: 2000, data_hex: "00" }], scripts: [{ hash: "55".repeat(28), language: "plutus_v2", size: 8 }],
  votes: [{ voter: "drep_key", voter_hash: "99".repeat(28), action_tx: H1, action_index: 0, choice: "yes" }],
  proposals: [{ index: 0, kind: "info", deposit: 100_000_000_000, return_address: ADDR }],
  cbor_hex: "84a1",
  continuum: { actor: { id: 4, name: "alice", role: "wallet" }, intent: 9, amount: 2_000_000, verdict: "accepted", findings: [{ id: "f1", kind: "wrong_class", severity: "fatal", summary: "bad class" }] },
};

describe("transaction detail", () => {
  const html = render(<TxBody tx={tx} />);
  it("shows validity, fee and the validity interval", () => {
    expect(html).toContain("invalid (collateral spent)");
    expect(html).toContain("0.3 ADA");
    expect(html).toContain("10000");
  });
  it("resolves inputs to their source output and says when it cannot", () => {
    expect(html).toContain("20 ADA");
    expect(html).toContain("source not indexed");
  });
  it("shows outputs with tokens, inline datum and reference script", () => {
    expect(html).toContain("7 tok");
    expect(html).toContain("inline");
    expect(html).toContain("plutus_v2");
  });
  it("decodes certificates, votes and proposals", () => {
    for (const t of ["stake_delegation", "drep_registration", "deposit 500 ADA", "drep_key", "yes", "info"]) expect(html).toContain(t);
  });
  it("links each proposal and each vote's action to the governance action page", () => {
    expect(html).toContain(`href="/governance/${H2}/0"`);
    expect(html).toContain(`href="/governance/${H1}/0"`);
  });
  it("shows mint, metadata, redeemer ExUnits, scripts and signers", () => {
    for (const t of ["Mint and burn", "-7 tok", "hello", "1,000", "2,000", "Scripts, redeemers and signers", "Required signers"]) expect(html).toContain(t);
  });
  it("links the continuum actor and findings", () => {
    expect(html).toContain('href="/actors/4"');
    expect(html).toContain("alice");
    expect(html).toContain('href="/findings"');
    expect(html).toContain("accepted");
  });
  it("offers the raw CBOR to copy and download", () => {
    expect(html).toContain('aria-label="Copy transaction CBOR"');
    expect(html).toContain(`href="/api/explorer/txs/${H2}/cbor"`);
    expect(html).toContain("download=");
    expect(html).toContain("84a1");
  });
  it("shows only the header for an evicted transaction", () => {
    const out = render(<TxBody tx={{ ...tx, evicted: true }} />);
    expect(out).toContain("older than the blocks");
    expect(out).not.toContain("Inputs (");
  });
});

describe("address", () => {
  const a: AddressDetail = {
    address: ADDR, stake: "stake_test1uzxx", balance: 25_000_000, assets: [{ policy: "33".repeat(28), name: "746f6b", name_text: "tok", quantity: "7" }],
    utxos: [{ tx: H2, index: 0, address: ADDR, lovelace: 25_000_000 }], utxo_total: 1, tx_count: 2, history: [{ hash: H2, block: H1, height: 7, slot: 70 }],
    offset: 0, limit: 25, actor: { id: 4, name: "alice", role: "wallet" }, stake_actor: { id: 4, name: "alice", role: "wallet" },
  };
  const html = render(<AddressBody a={a} onPage={() => {}} />);
  it("shows balance, tokens, UTxOs and history, linking the owning actor", () => {
    for (const t of ["25 ADA", "7 tok", "Unspent outputs (1)", "History (2)"]) expect(html).toContain(t);
    expect(html).toContain('href="/actors/4"');
    expect(html).toContain("Stake address");
  });
  it("says when no actor owns it", () => {
    expect(render(<AddressBody a={{ ...a, actor: undefined, stake: undefined }} onPage={() => {}} />)).toContain("no continuum actor");
  });
});

describe("epoch", () => {
  const e: EpochView = { epoch: 2, blocks: 10, txs: 20, fees: 3_400_000, size: 4096, first_height: 21, last_height: 30, pools: [{ pool: ISSUER, resolved: { name: "p1", impl: "dingo" }, blocks: 6, expected: 5 }, { pool: "d".repeat(56), blocks: 4, expected: 5 }] };
  it("shows totals and each pool against its expected blocks", () => {
    const html = render(<EpochBody e={e} />);
    for (const t of ["Epoch 2", "21 to 30", "3.4 ADA", "p1", "(dingo)", "5.0", "equal share"]) expect(html).toContain(t);
  });
});

describe("search box", () => {
  it("is a labelled search form with the shared help tip", () => {
    const html = render(<SearchBox />);
    expect(html).toContain('role="search"');
    expect(html).toContain('<label for="search-q"');
    expect(html).toContain('id="search-q"');
    expect(html).toContain('aria-describedby="search-tip"');
    expect(html).toContain('id="search-tip"');
    expect(html).toContain('aria-label="About Search"');
  });
  it("routes to the view the server names", async () => {
    const get = (async (path: string) => {
      expect(path).toBe(`/api/explorer/search?q=${H1}`);
      return { kind: "block", id: H1, route: `/explorer/blocks/${H1}` };
    }) as never;
    expect(await runSearch(`  ${H1}\n`, get)).toEqual({ ok: true, route: `/explorer/blocks/${H1}` });
  });
  it("reports a miss and refuses empty or oversized keys without asking", async () => {
    const miss = (async () => {
      throw new Error("not found: nothing matches");
    }) as never;
    expect(await runSearch("nope", miss)).toEqual({ ok: false, message: "not found: nothing matches" });
    const never = (async () => {
      throw new Error("must not be called");
    }) as never;
    expect((await runSearch("  ", never)).ok).toBe(false);
    expect((await runSearch("x".repeat(201), never)).ok).toBe(false);
  });
});

describe("help", () => {
  it("defines the explorer's terms", () => {
    const html = render(<Help />);
    for (const t of ["Block", "Slot", "Epoch", "UTxO", "Datum", "Redeemer", "ExUnits", "Certificate", "Collateral"]) {
      expect(GLOSSARY.map((g) => g.term), t).toContain(t);
      expect(html).toContain(`>${t}<`);
    }
    expect(html).toContain(">Explorer<");
  });
});
