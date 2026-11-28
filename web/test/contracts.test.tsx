import { describe, expect, it } from "vitest";
import { renderToStaticMarkup as render } from "react-dom/server";
import { navGroups } from "../src/lib/nav";
import { parseRoute } from "../src/lib/router";
import { decisionScripts, scriptRows, unitsText } from "../src/lib/contracts";
import type { ContractsView, ScriptRun } from "../src/lib/contracts";
import { FIELD_HELP, GLOSSARY } from "../src/lib/help";
import { ContractsBody } from "../src/views/Contracts";
import { TxBody } from "../src/views/ExTx";
import { Why } from "../src/views/Decisions";
import type { TxDetail } from "../src/lib/extypes";

const H = "a".repeat(64);
const SCRIPT = "c".repeat(56);

const run: ScriptRun = {
  validator: "vesting", instance: "vesting#1", script: SCRIPT, purpose: "spend", index: 0,
  datum: "d8799f", datum_json: '{"constructor":0,"fields":[]}', redeemer: "d87980", redeemer_json: '{"constructor":0,"fields":[]}',
  declared: { mem: 1200, steps: 340000 }, used: { mem: 1000, steps: 300000 }, ok: true,
};

function tx(over: Partial<TxDetail> = {}): TxDetail {
  return {
    hash: H, block: H, height: 2, slot: 9, index: 0, evicted: false, valid: true, size: 400, fee: 200_000,
    inputs: [], outputs: [], reference_inputs: [], collateral: [], mint: [], certificates: [], withdrawals: [], required_signers: [],
    redeemers: [{ tag: "spend", index: 0, mem: 1200, steps: 340000, purpose: "spend", script: SCRIPT, validator: "", data_hex: "d87980" }],
    scripts: [{ hash: SCRIPT, language: "plutus_v3", size: 279 }], votes: [], proposals: [],
    ...over,
  };
}

describe("script rows", () => {
  it("joins each on-chain redeemer with the engine's run of it", () => {
    const rows = scriptRows(tx({ continuum: { intent: 3, amount: 0, findings: [], scripts: [run] } }));
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ purpose: "spend", validator: "vesting#1", script: SCRIPT, declared: { mem: 1200, steps: 340000 }, used: { mem: 1000, steps: 300000 }, ok: true });
  });

  it("shows declared units only when the engine did not evaluate the transaction", () => {
    const rows = scriptRows(tx());
    expect(rows[0]!.used).toBeUndefined();
    expect(rows[0]!.ok).toBeUndefined();
    expect(rows[0]!.validator).toBe("");
  });

  it("formats units with thousands separators", () => {
    expect(unitsText({ mem: 1234567, steps: 89 })).toBe("1,234,567 mem / 89 steps");
  });
});

describe("transaction scripts section", () => {
  it("lists the validator, both budgets and the verdict", () => {
    const failed = { ...run, ok: false, error: "declared units below the units used" };
    const html = render(<TxBody tx={tx({ continuum: { intent: 3, amount: 0, findings: [], scripts: [failed] } })} />);
    expect(html).toContain("vesting#1");
    expect(html).toContain("1,200 mem / 340,000 steps");
    expect(html).toContain("1,000 mem / 300,000 steps");
    expect(html).toContain("declared units below the units used");
    expect(html).toContain("About ExUnits used");
  });
});

describe("contracts page", () => {
  const view: ContractsView = {
    toolchain: "Aiken v1.1.24+bacbeb3",
    validators: [{
      name: "vesting", module: "validators/vesting.ak", hash: SCRIPT, size: 279, language: "plutus_v3", purposes: ["spend"], params: ["_nonce"],
      datum: "continuum/types/VestingDatum", instances: 2, runs: 5, failures: 1,
      used: { avg_mem: 1000, avg_steps: 300000, max_mem: 1100, max_steps: 310000 },
      declared: { avg_mem: 1200, avg_steps: 360000, max_mem: 1300, max_steps: 370000 },
      reasons: [{ reason: "script failed", count: 1 }],
      last: [{ tx: H, at: 5000, instance: "vesting#1", purpose: "spend", ok: false, used: { mem: 1, steps: 2 }, declared: { mem: 3, steps: 4 }, error: "script failed" }],
    }],
  };

  it("lists every validator with its runs, budgets, failures and recent transactions", () => {
    const html = render(<ContractsBody view={view} />);
    for (const s of ["vesting", "validators/vesting.ak", SCRIPT, "279", "plutus_v3", "Aiken v1.1.24+bacbeb3", "script failed", `/explorer/txs/${H}`, "1,000 mem / 300,000 steps", "1,200 mem / 360,000 steps"]) {
      expect(html, s).toContain(s);
    }
    expect(html).toContain("About Validator");
  });

  it("has a route and a menu entry beside the explorer", () => {
    expect(parseRoute("/contracts")).toEqual({ name: "contracts" });
    expect(navGroups({ controls: false, explorer: true }).flatMap((g) => g.items)).toContainEqual({ path: "/contracts", label: "Contracts" });
    expect(navGroups({ controls: false, explorer: false }).flatMap((g) => g.items).some((n) => n.path === "/contracts")).toBe(false);
  });
});

describe("script help", () => {
  it("defines the script words in the glossary and as field help", () => {
    const terms = GLOSSARY.map((g) => g.term);
    for (const t of ["Validator", "Script purpose", "Datum", "Redeemer", "ExUnits"]) expect(terms).toContain(t);
    for (const k of ["validator", "script_purpose", "exunits_declared", "exunits_used", "datum", "redeemer"]) expect(FIELD_HELP[k], k).toBeDefined();
  });
});

describe("decision why box", () => {
  it("names the validator a script call ran", () => {
    const d = { id: 4, at: 10, kind: "Submitted", actor: 2, intent: "unlock", body: { kind: "unlock", validator: "escrow#2", scripts: [{ ...run, validator: "escrow", instance: "escrow#2" }] } };
    expect(decisionScripts(d)?.validator).toBe("escrow#2");
    const html = render(<Why d={d} />);
    expect(html).toContain("escrow#2");
    expect(html).toContain("1,000 mem / 300,000 steps");
    expect(decisionScripts({ id: 5, at: 1, kind: "Submitted", actor: 1, body: { kind: "" } })).toBeNull();
  });
  it("says what the call did with the contract", () => {
    const d = { id: 6, at: 10, kind: "Submitted", actor: 3, intent: "unlock", body: { kind: "unlock", validator: "escrow#2", call: "release", scripts: [] } };
    expect(decisionScripts(d)?.call).toBe("release");
    expect(render(<Why d={d} />)).toContain("why: release at escrow#2");
  });
});
