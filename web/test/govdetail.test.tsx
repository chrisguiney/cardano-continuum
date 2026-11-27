import { describe, expect, it } from "vitest";
import { renderToStaticMarkup as render } from "react-dom/server";
import { groupParams, govPath, securityNames } from "../src/lib/govdetail";
import { GOV_GLOSSARY, GOV_HELP } from "../src/lib/govhelp";
import { GLOSSARY } from "../src/lib/help";
import { parseRoute } from "../src/lib/router";
import type { GovContent, GovDetail, GovernanceView, ProposalRow, Tally } from "../src/lib/types";
import { GovBody } from "../src/views/GovAction";
import { Help } from "../src/views/Help";
import { ProposalTable } from "../src/views/Governance";

const TX = "ab".repeat(32);
const PREV = "cd".repeat(32);
const tally = (over: Partial<Tally> = {}): Tally => ({ yes: 0, no: 0, abstain: 0, yes_power: 0, no_power: 0, abstain_power: 0, ...over });

const row = (over: Partial<ProposalRow> = {}): ProposalRow => ({
  id: `${TX}#0`, type: "info", proposer: 5, profile: "network", deposit_lovelace: 100_000_000_000, slot: 5, epoch: 1, expires_epoch: 7,
  status: "open", spo_may_vote: true, tally: { drep: tally({ no: 1, no_power: 3_000_000 }), spo: tally({ yes: 1, yes_power: 5_000_000 }), committee: tally() }, ...over,
});

const base: GovContent = {
  tx: TX, index: 0, slot: 5, kind: "info", return_address: "stake_test1uqret", return_actor: { id: 5, name: "network-5", role: "active" },
  deposit: 100_000_000_000, anchor: { url: "https://example.test/a", hash: "12".repeat(32) },
};

const voters = (spo: boolean, committee = true) => [
  { class: "drep", allowed: true, reason: "DReps vote on every kind of governance action.", rule: "isDRepVotingAllowed" },
  { class: "spo", allowed: spo, reason: spo ? "Pools vote." : "Pools do not vote on this.", rule: "isStakePoolVotingAllowed" },
  { class: "committee", allowed: committee, reason: "Committee reason.", rule: "isCommitteeVotingAllowed" },
];

const detail = (content: GovContent, over: Partial<GovDetail> = {}): GovDetail => ({
  id: `${TX}#0`, epoch: 2, lifetime_epochs: 6, proposal: row({ type: content.kind }), summary: "a summary", content, voters: voters(true),
  votes: [
    { slot: 10, at: 12_000, tx: "ef".repeat(32), proposal: `${TX}#0`, type: content.kind, voter: "spo", voter_id: "07".repeat(28), actor: 7, profile: "pools", choice: "yes", power: 5_000_000, anchor: { url: "https://example.test/why", hash: "14".repeat(32) } },
    { slot: 10, at: 12_000, tx: "01".repeat(32), proposal: `${TX}#0`, type: content.kind, voter: "drep", voter_id: "08".repeat(28), choice: "no", power: 3_000_000 },
  ],
  ...over,
});

const security: GovContent = {
  ...base, kind: "parameter_change", prev_action: { tx: PREV, index: 1 }, policy_hash: "31".repeat(28),
  params: [
    { field: "MaxTxSize", name: "maxTxSize", group: "network", security: true, unit: "bytes", desc: "The most bytes one transaction may take.", new: "20,000", current: "16,384" },
    { field: "MaxTxExUnits", name: "maxTxExUnits", group: "network", security: false, unit: "memory units and CPU steps", desc: "The most memory and CPU steps one transaction's scripts may use.", new: "20,000,000 mem, 10,000,000,000 steps", current: "16,500,000 mem, 10,000,000,000 steps" },
    { field: "DRepDeposit", name: "dRepDeposit", group: "governance", security: false, unit: "lovelace", desc: "The deposit a DRep pays.", new: "600,000,000" },
  ],
};

describe("governance action page", () => {
  it("shows the header: type, proposer, deposit, epochs, status, anchor and previous action", () => {
    const html = render(<GovBody d={detail(security)} />);
    for (const t of ["parameter_change", "network-5", "100,000.00 ADA", "epoch 1", "epoch 7", "open", "https://example.test/a", "12".repeat(32)]) expect(html, t).toContain(t);
    expect(html).toContain('href="/actors/5"');
    expect(html).toContain(`href="/governance/${PREV}/1"`);
    expect(html).toContain(`href="/explorer/txs/${TX}"`);
  });

  it("parameter change: a table of each change with its group, unit, new and current value and security flag", () => {
    const html = render(<GovBody d={detail(security)} />);
    for (const t of ["maxTxSize", "20,000", "16,384", "bytes", "maxTxExUnits", "20,000,000 mem, 10,000,000,000 steps", "16,500,000 mem", "dRepDeposit", "600,000,000"]) expect(html, t).toContain(t);
    for (const g of ["network", "governance"]) expect(html, g).toContain(`>${g}<`);
    expect(html).toContain("The most memory and CPU steps one transaction&#x27;s scripts may use.");
    expect(html).toContain("security group");
    expect(html).toContain("31".repeat(28));
    expect(html).toContain("Current value");
  });

  it("splits parameters by group and names the security ones", () => {
    expect(groupParams(security.params!).map(([g, ps]) => [g, ps.length])).toEqual([["network", 2], ["governance", 1]]);
    expect(securityNames(security.params!)).toEqual(["maxTxSize"]);
    expect(securityNames([security.params![1]!])).toEqual([]);
  });

  it("treasury withdrawal: recipients linked to their actors, and the total", () => {
    const c: GovContent = { ...base, kind: "treasury_withdrawals", total: 1_200_000_000, withdrawals: [{ address: "stake_test1a", lovelace: 200_000_000 }, { address: "stake_test1b", lovelace: 1_000_000_000, actor: { id: 6, name: "hodl-6", role: "active" } }] };
    const html = render(<GovBody d={detail(c, { voters: voters(false) })} />);
    expect(html).toContain("1,200.00 ADA");
    expect(html).toContain("stake_test1b");
    expect(html).toContain('href="/actors/6"');
    expect(html).toContain("Total");
  });

  it("update committee: removed and added members with terms, and the quorum", () => {
    const c: GovContent = { ...base, kind: "update_committee", removed: ["key:" + "41".repeat(28)], added: [{ credential: "script:" + "42".repeat(28), epoch: 250 }], quorum: "2/3" };
    const html = render(<GovBody d={detail(c, { voters: voters(true, false) })} />);
    for (const t of ["key:" + "41".repeat(28), "script:" + "42".repeat(28), "epoch 250", "2/3", "Quorum"]) expect(html, t).toContain(t);
  });

  it("new constitution: anchor and guardrails script", () => {
    const c: GovContent = { ...base, kind: "new_constitution", constitution: { anchor: { url: "https://example.test/constitution", hash: "13".repeat(32) }, script_hash: "33".repeat(28) } };
    const html = render(<GovBody d={detail(c)} />);
    for (const t of ["https://example.test/constitution", "13".repeat(32), "33".repeat(28), "Guardrails script"]) expect(html, t).toContain(t);
  });

  it("hard fork: the target protocol version", () => {
    const html = render(<GovBody d={detail({ ...base, kind: "hard_fork_initiation", version: { major: 11, minor: 0 } })} />);
    expect(html).toContain("11.0");
  });

  it("no confidence and info say what they mean", () => {
    expect(render(<GovBody d={detail({ ...base, kind: "no_confidence" })} />)).toContain(GOV_HELP.no_confidence!.help.slice(0, 40));
    expect(render(<GovBody d={detail({ ...base, kind: "info" })} />)).toContain("A poll");
  });

  it("who may vote: each class, with the reason and the rule in its tip", () => {
    const html = render(<GovBody d={detail({ ...base, kind: "treasury_withdrawals" }, { voters: voters(false) })} />);
    expect(html).toContain("Pools do not vote on this.");
    expect(html).toContain("isStakePoolVotingAllowed");
    expect(html).toContain("not eligible");
  });

  it("votes: voter linked to its actor, choice, power, anchor and transaction link, with tallies by class", () => {
    const html = render(<GovBody d={detail(security)} />);
    expect(html).toContain('href="/actors/7"');
    expect(html).toContain(`href="/explorer/txs/${"ef".repeat(32)}"`);
    expect(html).toContain("https://example.test/why");
    expect(html).toContain("5.00 ADA");
    expect(html).toContain("1 yes (5.00 ADA)");
    expect(html).toContain("1 no (3.00 ADA)");
    expect(html).toContain("100% yes");
  });

  it("every term has a tip", () => {
    const html = render(<GovBody d={detail(security)} />);
    const ids = [...html.matchAll(/aria-describedby="([^"]+)"/g)].map((m) => m[1]!);
    expect(ids.length).toBeGreaterThan(10);
    for (const id of new Set(ids)) expect(html, id).toContain(`id="${id}"`);
    for (const t of ["About Governance action", "About DRep", "About Guardrails script", "About Anchor", "About Security group", "About GovLifetime", "About Ratification"]) expect(html, t).toContain(t);
  });

  it("without decoded content it still shows the votes and says why", () => {
    const html = render(<GovBody d={detail(security, { content: undefined })} />);
    expect(html).toContain("not decoded");
    expect(html).toContain('href="/actors/7"');
  });
});

describe("governance routes and links", () => {
  it("routes /governance/{tx}/{index}", () => {
    expect(parseRoute(`/governance/${TX}/2`)).toEqual({ name: "govaction", tx: TX, index: 2 });
    expect(parseRoute("/governance/xyz/2")).toEqual({ name: "notfound" });
    expect(parseRoute(`/governance/${TX}/x`)).toEqual({ name: "notfound" });
  });
  it("builds a detail path from a proposal id", () => {
    expect(govPath(`${TX}#3`)).toBe(`/governance/${TX}/3`);
    expect(govPath("10#0")).toBeNull();
  });
});

describe("governance list", () => {
  const g: GovernanceView = {
    epoch: 2, lifetime_epochs: 6, votes: [], pools: [],
    proposals: [row({ summary: "Change maxTxExUnits 16.5M→20M mem; 2 other params" }), row({ id: `${PREV}#1`, type: "treasury_withdrawals", spo_may_vote: false, summary: "Withdraw 1,200 ADA to 3 recipients" })],
  };
  const html = render(<ProposalTable proposals={g.proposals} />);
  it("links each proposal to its page and shows the one-line summary", () => {
    expect(html).toContain(`href="/governance/${TX}/0"`);
    expect(html).toContain(`href="/governance/${PREV}/1"`);
    expect(html).toContain("Change maxTxExUnits 16.5M→20M mem; 2 other params");
    expect(html).toContain("Withdraw 1,200 ADA to 3 recipients");
  });
  it("gives every column heading a tip", () => {
    for (const t of ["About Action type", "About Deposit", "About GovLifetime", "About Status", "About DRep", "About SPO", "About Constitutional committee"]) expect(html, t).toContain(t);
  });
});

describe("governance glossary", () => {
  it("is on the help page with its terms", () => {
    const html = render(<Help />);
    for (const t of ["Governance action", "DRep", "SPO", "Constitutional committee", "Guardrails script", "Security group", "Ratification", "GovLifetime", "Anchor"]) {
      expect(GOV_GLOSSARY.map((x) => x.term), t).toContain(t);
      expect(html, t).toContain(`>${t}<`);
    }
    expect(html).toContain(">Governance<");
    expect(GLOSSARY.length).toBeGreaterThan(0);
  });
});
