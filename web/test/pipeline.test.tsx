import { describe, expect, it } from "vitest";
import { renderToStaticMarkup as render } from "react-dom/server";
import { parseRoute } from "../src/lib/router";
import { contractPath, phaseSummary } from "../src/lib/contracts";
import type { ContractDetail, InstanceDetail, Pipeline } from "../src/lib/contracts";
import { FIELD_HELP, GLOSSARY } from "../src/lib/help";
import { ContractBody } from "../src/views/ContractDetail";
import { ExTxScriptLink } from "../src/views/ExTx";

const HASH = "d2".repeat(28);

function pipe(over: Partial<Pipeline> = {}): Pipeline {
  return {
    uplc: "(program 1.1.0\n  (lam v0\n    v0\n  )\n)",
    flat: "01010029", flat_size: 4,
    wrapped: "4401010029", wrapped_size: 5,
    tag: 3, hash: HASH,
    ...over,
  };
}

const detail: ContractDetail = {
  name: "vesting", toolchain: "Aiken v1.1.24+bacbeb3", params: ["_nonce"],
  sources: [
    { path: "validators/vesting.ak", text: "validator vesting(_nonce: Int) {\n  spend() { True }\n}\n" },
    { path: "lib/continuum/types.ak", text: "pub type VestingDatum {\n  owner: ByteArray\n}\n" },
  ],
  pipeline: pipe(), instances: [1, 3],
};

const instance: InstanceDetail = {
  name: "vesting#3",
  pipeline: pipe({ uplc: "(program 1.1.0\n  [\n    (lam v0\n      v0\n    )\n    (con data (I 3))\n  ]\n)", flat_size: 9, wrapped_size: 10, hash: "ab".repeat(28) }),
};

describe("contract routes", () => {
  it("parses a validator and an instance, and rejects other names", () => {
    expect(parseRoute("/contracts/vesting")).toEqual({ name: "contract", key: "vesting" });
    expect(parseRoute("/contracts/vesting/3")).toEqual({ name: "contract", key: "vesting", instance: 3 });
    expect(parseRoute("/contracts/Ves ting")).toEqual({ name: "notfound" });
    expect(parseRoute("/contracts/vesting/x")).toEqual({ name: "notfound" });
    expect(parseRoute("/contracts")).toEqual({ name: "contracts" });
  });

  it("links an instance name or a validator name to its page", () => {
    expect(contractPath("vesting#3")).toBe("/contracts/vesting/3");
    expect(contractPath("escrow")).toBe("/contracts/escrow");
    expect(contractPath("odd name#x")).toBeNull();
  });

  it("links a transaction's script rows to the page", () => {
    const html = render(<ExTxScriptLink validator="vesting#3" />);
    expect(html).toContain('href="/contracts/vesting/3"');
  });
});

describe("pipeline view", () => {
  const html = render(<ContractBody detail={detail} instance={null} selected={null} />);

  it("shows the source with line numbers, the validator module first", () => {
    expect(html).toContain("validators/vesting.ak");
    expect(html).toContain("lib/continuum/types.ak");
    expect(html).toContain("validator vesting(_nonce: Int)");
    expect(html).toMatch(/>1\n2\n3</);
  });

  it("shows each phase with its size", () => {
    for (const s of ["Aiken source", "UPLC", "Flat bytes", "On-chain bytes"]) expect(html).toContain(s);
    expect(html).toContain("4 bytes");
    expect(html).toContain("5 bytes");
  });

  it("derives the hash from the tag and the wrapped bytes", () => {
    expect(html).toContain("blake2b-224");
    expect(html).toContain("03");
    expect(html).toContain(HASH);
  });

  it("applies an instance's parameters and shows both programs", () => {
    const h = render(<ContractBody detail={detail} instance={instance} selected={3} />);
    expect(h).toContain("vesting#3");
    expect(h).toContain("(con data (I 3))");
    expect(h).toContain("ab".repeat(28));
    expect(h).toContain(HASH);
  });

  it("says what each step changed, in sizes", () => {
    const rows = phaseSummary(pipe(), "validator vesting");
    expect(rows.map((r) => r.phase)).toEqual(["Aiken source", "UPLC", "Flat bytes", "On-chain bytes"]);
    expect(rows[3]!.size).toBe(5);
    expect(rows[3]!.change).toContain("1 byte");
  });
});

describe("pipeline help", () => {
  it("defines the new words", () => {
    const terms = GLOSSARY.map((g) => g.term);
    for (const t of ["UPLC", "Flat encoding", "Aiken", "Script hash"]) expect(terms).toContain(t);
    for (const k of ["phase_source", "phase_uplc", "phase_flat", "phase_onchain", "script_hash_derivation"]) expect(FIELD_HELP[k], k).toBeDefined();
  });
});
