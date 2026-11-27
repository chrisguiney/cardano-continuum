import { describe, expect, it } from "vitest";
import { feedUrl } from "../src/lib/feed";
import { choiceLine, filterGov, poolVotes, sortProposals, yesShare } from "../src/lib/governance";
import { FIELD_HELP, VIEW_BLURB } from "../src/lib/help";
import { parseRoute } from "../src/lib/router";
import type { GovernanceView, ProposalRow, Tally } from "../src/lib/types";

const tally = (over: Partial<Tally> = {}): Tally => ({ yes: 0, no: 0, abstain: 0, yes_power: 0, no_power: 0, abstain_power: 0, ...over });

const prop = (id: string, status: string, expires: number | undefined, slot = 0): ProposalRow => ({
  id, type: "info", deposit_lovelace: 100_000, slot, status, spo_may_vote: true,
  expires_epoch: expires, tally: { drep: tally(), spo: tally(), committee: tally() },
});

describe("governance view", () => {
  it("routes /governance", () => {
    expect(parseRoute("/governance")).toEqual({ name: "governance" });
  });

  it("asks the decision feed for votes only", () => {
    expect(feedUrl("/api/decisions", { vote: true }, null)).toBe("/api/decisions?vote=true");
    expect(feedUrl("/api/decisions", { vote: false }, null)).toBe("/api/decisions");
  });

  it("lists open proposals first, the nearest expiry first, then the rest newest first", () => {
    const ps = [prop("a#0", "expired", 1, 10), prop("b#0", "open", 9), prop("c#0", "open", 4), prop("d#0", "expired", 2, 20)];
    expect(sortProposals(ps).map((p) => p.id)).toEqual(["c#0", "b#0", "d#0", "a#0"]);
  });

  it("weighs the yes share by voting power when the view knows it, by head count otherwise", () => {
    expect(yesShare(tally({ yes: 1, no: 3, yes_power: 900, no_power: 100 }))).toBeCloseTo(0.9);
    expect(yesShare(tally({ yes: 1, no: 3 }))).toBeCloseTo(0.25);
    expect(yesShare(tally({ abstain: 2, abstain_power: 50 }))).toBeNull();
    expect(yesShare(tally())).toBeNull();
  });

  it("renders a tally as counts with their power", () => {
    expect(choiceLine(tally())).toBe("no votes");
    expect(choiceLine(tally({ yes: 2, yes_power: 5_000_000, abstain: 1 }))).toBe("2 yes (5.00 ADA) · 1 abstain");
  });

  it("finds a pool actor's SPO votes", () => {
    const v: GovernanceView = {
      lifetime_epochs: 6, proposals: [], votes: [],
      pools: [{ actor: 7, votes: [{ slot: 1, at: 1000, tx: "aa", proposal: "a#0", type: "info", voter: "spo", voter_id: "07", actor: 7, choice: "yes", power: 1 }] }],
    };
    expect(poolVotes(v, 7)).toHaveLength(1);
    expect(poolVotes(v, 8)).toEqual([]);
    expect(poolVotes(null, 7)).toEqual([]);
  });

  it("filters votes by voter class and proposals by action type", () => {
    const vote = (voter: string, type: string) => ({ slot: 1, at: 1, tx: voter + type, proposal: "a#0", type, voter, voter_id: "00", choice: "yes", power: 0 });
    const v: GovernanceView = {
      lifetime_epochs: 6, proposals: [prop("a#0", "open", 3), { ...prop("b#0", "open", 4), type: "treasury_withdrawals" }],
      votes: [vote("spo", "info"), vote("drep", "info"), vote("drep", "treasury_withdrawals")], pools: [],
    };
    const f = filterGov(v, { voter: "drep", type: "info" });
    expect(f.votes.map((x) => x.tx)).toEqual(["drepinfo"]);
    expect(f.proposals.map((p) => p.id)).toEqual(["a#0"]);
    expect(filterGov(v, { voter: "", type: "" })).toEqual(v);
  });

  it("explains the governance controls and view", () => {
    for (const k of ["voter_class", "action_type"]) expect(FIELD_HELP[k], k).toBeTruthy();
    expect(VIEW_BLURB.governance).toBeTruthy();
  });
});
