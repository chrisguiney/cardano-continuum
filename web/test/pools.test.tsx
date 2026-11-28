import { describe, expect, it } from "vitest";
import { renderToStaticMarkup as render } from "react-dom/server";
import { NodeDetail, NodeTable } from "../src/views/Nodes";
import { StakePoolDetail, StakePoolTable, statusText } from "../src/views/StakePools";
import { sortNodes } from "../src/lib/nodesort";
import { FIELD_HELP, GLOSSARY } from "../src/lib/help";
import type { NodeRow, StakePoolRow } from "../src/lib/types";

const POOL = "0a".repeat(28);
const node = (over: Partial<NodeRow>): NodeRow => ({
  name: "p3", label: "continuum-pool-3", impl: "dingo", tier: "forging", phase: "Forging", dark: false,
  tip: { slot: 100, hash: "aa".repeat(32), height: 20 }, slot_lag: 0, fork: false, last_block: null, findings: 0, known_findings: 0, forged: 0, verified: 0, ...over,
});
const pool = (over: Partial<StakePoolRow>): StakePoolRow => ({
  id: POOL, bech32: "pool1pg9q5zs2pg9q5zs2pg9q5zs2pg9q5zs2pg9q5zs2pg9q5lc4t0t", actor: 7, actor_name: "pool-7", actor_state: "registered",
  registration: { pledge: 1_000_000_000, cost: 340_000_000, margin: 0.05, reward_account: "21".repeat(28), owners: 1, slot: 5 },
  retire_epoch: null, delegators: 2, delegated_lovelace: 11_000_000, blocks: 3, votes: 1, nodes: [], ...over,
});

describe("nodes view", () => {
  it("links each node and the stake pool its keys belong to", () => {
    const html = render(<NodeTable rows={[node({ stake_pool: POOL }), node({ name: "r1", tier: "validating", phase: "Following", slot_lag: 4 })]} forked={new Set()} total={2} />);
    expect(html).toContain('href="/nodes/p3"');
    expect(html).toContain(`href="/pools/${POOL}"`);
    expect(html).toContain("no keys (validating)");
    expect(html).toContain(">4<");
  });

  it("says a forging node has no pool until it forges", () => {
    expect(render(<NodeTable rows={[node({})]} forked={new Set()} total={1} />)).toContain("not forged yet");
  });

  it("shows one node in full with its last own block and findings", () => {
    const html = render(<NodeDetail n={node({ stake_pool: POOL, last_block: { slot: 95, hash: "bb".repeat(32), height: 19 }, findings: 3, known_findings: 2 })} forked />);
    expect(html).toContain("Node p3");
    expect(html).toContain(`href="/explorer/blocks/${"bb".repeat(32)}"`);
    expect(html).toContain("3 (2 known)");
    expect(html).toContain("continuum-pool-3");
    expect(html).toContain("on a disagreeing branch");
  });
});

describe("node block counts", () => {
  it("shows forged and verified in the table and the detail, with a dash when unknown", () => {
    const rows = [node({ forged: 7, verified: 1234 }), node({ name: "r1", tier: "validating", forged: null, verified: null })];
    const html = render(<NodeTable rows={rows} forked={new Set()} total={2} />);
    expect(html).toContain("Forged");
    expect(html).toContain("Verified");
    expect(html).toContain('aria-describedby="nh-forged"');
    expect(html).toContain(">1,234<");
    expect(html).toContain("unknown");
    const d = render(<NodeDetail n={node({ forged: 7, verified: 1234 })} forked={false} />);
    expect(d).toContain(">7<");
    expect(d).toContain(">1,234<");
  });

  it("sorts by a count, unknown last, without changing the input", () => {
    const rows = [node({ name: "a", forged: 2 }), node({ name: "b", forged: null }), node({ name: "c", forged: 9 })];
    expect(sortNodes(rows, "forged", true).map((n) => n.name)).toEqual(["c", "a", "b"]);
    expect(sortNodes(rows, "forged", false).map((n) => n.name)).toEqual(["a", "c", "b"]);
    expect(rows.map((n) => n.name)).toEqual(["a", "b", "c"]);
    expect(sortNodes(rows, "name", false).map((n) => n.name)).toEqual(["a", "b", "c"]);
  });

  it("defines both counts in the help", () => {
    expect(FIELD_HELP.forged!.help).toContain("selected chain");
    expect(FIELD_HELP.verified!.help).toContain("other");
    expect(GLOSSARY.map((g) => g.term)).toEqual(expect.arrayContaining(["Forged", "Verified"]));
  });
});

describe("stake pools view", () => {
  it("lists terms, delegation, blocks and the node running each pool", () => {
    const html = render(<StakePoolTable rows={[pool({ nodes: [{ name: "p3", impl: "dingo", tier: "forging", phase: "Forging" }] }), pool({ id: "0b".repeat(28), bech32: "", actor: null, registration: null, blocks: null })]} />);
    expect(html).toContain(`href="/pools/${POOL}"`);
    expect(html).toContain('href="/actors/7"');
    expect(html).toContain('href="/nodes/p3"');
    expect(html).toContain(`href="/explorer/pools/${POOL}"`);
    expect(html).toContain("5.0%");
    expect(html).toContain("no node (logical)");
    expect(html).toContain("no actor");
  });

  it("names the status from the operator's state and a pending retirement", () => {
    expect(statusText(pool({}))).toBe("registered");
    expect(statusText(pool({ retire_epoch: 9 }))).toBe("retires at epoch 9");
    expect(statusText(pool({ actor_state: "retired", retire_epoch: 9 }))).toBe("retired");
  });

  it("shows one pool in full", () => {
    const html = render(<StakePoolDetail p={pool({})} />);
    for (const t of [POOL, "pool1pg9q5", "pool-7", "Fixed cost", "no node (logical)", "none (no ticker or name)"]) expect(html).toContain(t);
  });

  it("explains every stake pool column with a tooltip", () => {
    const html = render(<StakePoolTable rows={[pool({})]} />);
    for (const k of ["pool_id", "pool_operator", "pool_status", "pledge", "margin", "pool_cost", "pool_delegators", "pool_delegated", "pool_blocks", "pool_votes", "pool_node"]) {
      expect(FIELD_HELP[k], k).toBeTruthy();
      expect(html, k).toContain(`id="ph-${k}"`);
    }
    expect(FIELD_HELP.pledge!.help).toMatch(/promise/);
    expect(FIELD_HELP.margin!.help).toMatch(/after its fixed cost/);
    expect(FIELD_HELP.delegated!.label).toBe("Delegated");
  });
});

