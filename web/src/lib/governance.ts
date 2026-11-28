import { ada } from "./format";
import type { GovernanceView, PendingFork, ProposalRow, ProtocolVersion, ScheduledFork, Tally, VoteRow } from "./types";

/** versionText renders a protocol version as major.minor, "unknown" when absent. */
export function versionText(v?: ProtocolVersion): string {
  return v ? `${v.major}.${v.minor}` : "unknown";
}

/**
 * forkLine says where the hard fork stands: ratified and enacting at an epoch, proposed
 * and open for votes (open: a hard-fork proposal is open), scheduled for an epoch, or none.
 */
export function forkLine(f?: PendingFork, plan?: ScheduledFork, open = false): string {
  if (f) return `hard fork to ${versionText(f.version)} ratified, in force from epoch ${f.at}`;
  if (plan && open) return `hard fork to ${versionText(plan.to)} proposed, open for votes`;
  if (plan) return `hard fork to ${versionText(plan.to)} scheduled at epoch ≥${plan.epoch}`;
  return "no hard fork pending";
}

/** The voter classes, in the order the view shows them. */
export const CLASSES = ["drep", "spo", "committee"] as const;

/** sortProposals puts open proposals first, the nearest expiry first, then the rest newest first. */
export function sortProposals(ps: ProposalRow[]): ProposalRow[] {
  const exp = (p: ProposalRow) => p.expires_epoch ?? Number.MAX_SAFE_INTEGER;
  return [...ps].sort((a, b) => {
    const ao = a.status === "open" ? 0 : 1;
    const bo = b.status === "open" ? 0 : 1;
    if (ao !== bo) return ao - bo;
    if (ao === 0) return exp(a) - exp(b) || a.id.localeCompare(b.id);
    return b.slot - a.slot || a.id.localeCompare(b.id);
  });
}

/**
 * yesShare is the yes share of the yes and no votes, weighted by voting
 * power when any of them carries power, else by head count; null with no
 * yes or no vote. Abstentions count for neither side, as in ratification.
 */
export function yesShare(t: Tally): number | null {
  const power = t.yes_power + t.no_power;
  if (power > 0) return t.yes_power / power;
  const n = t.yes + t.no;
  return n > 0 ? t.yes / n : null;
}

/** choiceLine renders a tally as its counts, each with the power behind it. */
export function choiceLine(t: Tally): string {
  const parts: string[] = [];
  const one = (n: number, p: number, what: string) => {
    if (n > 0) parts.push(`${n} ${what}${p > 0 ? ` (${ada(p)})` : ""}`);
  };
  one(t.yes, t.yes_power, "yes");
  one(t.no, t.no_power, "no");
  one(t.abstain, t.abstain_power, "abstain");
  return parts.length ? parts.join(" · ") : "no votes";
}

/** poolVotes is a pool actor's newest SPO votes, newest first. */
export function poolVotes(v: GovernanceView | null, actor: number): VoteRow[] {
  return v?.pools.find((p) => p.actor === actor)?.votes ?? [];
}

export interface GovFilter {
  /** voter is a voter class (drep, spo, committee), or empty for all. */
  voter: string;
  /** type is an action type, or empty for all. */
  type: string;
}

/** filterGov keeps the votes of one voter class and the proposals and votes of one action type. */
export function filterGov(v: GovernanceView, f: GovFilter): GovernanceView {
  if (!f.voter && !f.type) return v;
  return {
    ...v,
    proposals: v.proposals.filter((p) => !f.type || p.type === f.type),
    votes: v.votes.filter((x) => (!f.voter || x.voter === f.voter) && (!f.type || x.type === f.type)),
  };
}
