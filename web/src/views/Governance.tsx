import { useEffect, useMemo, useState } from "react";
import { Feed, appendDecision, type EventSourceLike, type FeedState } from "../lib/feed";
import { ada, count, short, simTime } from "../lib/format";
import { CLASSES, choiceLine, filterGov, sortProposals, yesShare, type GovFilter } from "../lib/governance";
import { govPath } from "../lib/govdetail";
import { GOV_HELP } from "../lib/govhelp";
import { FIELD_HELP, VIEW_BLURB } from "../lib/help";
import { optionsOf, useEnums } from "../lib/meta";
import { usePoll } from "../lib/hooks";
import type { Decision, GovernanceView, ProposalRow, VoteRow } from "../lib/types";
import { ErrorNote, Link, Stat } from "./common";
import { Blurb, HelpTip, SelectField } from "./fields";

const FEED_CAP = 100;

const CLASS_LABEL: Record<string, string> = { drep: "DRep", spo: "SPO", committee: "Constitutional committee" };

function Actor({ id, profile }: { id?: number; profile?: string }) {
  if (id === undefined) return <span className="muted">not ours</span>;
  return (
    <>
      <Link to={`/actors/${id}`}>actor {id}</Link>
      {profile && <span className="muted"> {profile}</span>}
    </>
  );
}

function Share({ v }: { v: number | null }) {
  return v === null ? <span className="muted">–</span> : <span>{(v * 100).toFixed(0)}% yes</span>;
}

/** VoteTable lists votes, newest first. */
export function VoteTable({ votes, empty }: { votes: VoteRow[]; empty: string }) {
  return (
    <div className="table-wrap">
      <table>
        <thead>
          <tr><th>Slot</th><th>Voter</th><th>Actor</th><th>Proposal</th><th>Type</th><th>Choice</th><th>Power</th></tr>
        </thead>
        <tbody>
          {votes.map((v) => (
            <tr key={`${v.tx}-${v.proposal}-${v.voter}`}>
              <td className="num">{count(v.slot)}</td>
              <td>{CLASS_LABEL[v.voter] ?? v.voter}</td>
              <td><Actor id={v.actor} profile={v.profile} /></td>
              <td><ProposalLink id={v.proposal} /></td>
              <td>{v.type || "–"}</td>
              <td>{v.choice}</td>
              <td className="num">{v.power > 0 ? ada(v.power) : "–"}</td>
            </tr>
          ))}
          {votes.length === 0 && <tr><td colSpan={7} className="empty">{empty}</td></tr>}
        </tbody>
      </table>
    </div>
  );
}

function ProposalLink({ id }: { id: string }) {
  const to = govPath(id);
  const text = <code>{short(id, 10)}</code>;
  return to ? <Link to={to}>{text}</Link> : text;
}

/** ColHead is a column heading with the "?" tip of a governance term. */
function ColHead({ k, label, id }: { k: string; label?: string; id: string }) {
  const h = (GOV_HELP[k] ?? FIELD_HELP[k])!;
  return <th>{label ?? h.label}<HelpTip id={`gh-${id}`} label={h.label} text={h.help} /></th>;
}

const CLASS_TERM: Record<string, string> = { drep: "drep", spo: "spo", committee: "committee" };

/** ProposalTable lists proposals, each linking to its page with a one-line summary of what it asks for. */
export function ProposalTable({ proposals }: { proposals: ProposalRow[] }) {
  return (
    <div className="table-wrap">
      <table>
        <thead>
          <tr>
            <th>Proposal</th><ColHead k="action_type" label="Type" id="type" /><th>Proposer</th><ColHead k="deposit" id="deposit" />
            <ColHead k="gov_lifetime" label="Expires" id="expires" /><ColHead k="status" id="status" />
            {CLASSES.map((c) => <ColHead key={c} k={CLASS_TERM[c]!} label={CLASS_LABEL[c]} id={`class-${c}`} />)}
          </tr>
        </thead>
        <tbody>
          {proposals.map((p) => (
            <tr key={p.id} className={p.status === "expired" ? "dim" : undefined}>
              <td><ProposalLink id={p.id} /></td>
              <td>{p.type || "unknown"}{p.summary && <div className="muted">{p.summary}</div>}</td>
              <td><Actor id={p.proposer} profile={p.profile} /></td>
              <td className="num">{ada(p.deposit_lovelace)}</td>
              <td className="num">{p.expires_epoch === undefined ? "–" : `epoch ${p.expires_epoch}`}</td>
              <td><span className={`tag ${p.status === "open" || p.status === "ratified" ? "ok" : "warn"}`}>{p.status}</span></td>
              {CLASSES.map((c) => {
                const t = p.tally[c];
                if (c === "spo" && !p.spo_may_vote) return <td key={c} className="muted" title="the ledger does not let stake pools vote on this action">not eligible</td>;
                return <td key={c}>{t ? <>{choiceLine(t)}<br /><Share v={yesShare(t)} /></> : "–"}</td>;
              })}
            </tr>
          ))}
          {proposals.length === 0 && <tr><td colSpan={9} className="empty">No proposals on the chain yet.</td></tr>}
        </tbody>
      </table>
    </div>
  );
}

function useVoteFeed(): [Decision[], FeedState] {
  const [rows, setRows] = useState<Decision[]>([]);
  const [state, setState] = useState<FeedState>("connecting");
  useEffect(() => {
    const f = new Feed({
      base: "/api/decisions",
      filter: { vote: true },
      open: (u) => new EventSource(u) as unknown as EventSourceLike,
      onDecision: (d) => setRows((r) => appendDecision(r, d, FEED_CAP)),
      onGap: () => {},
      onReset: () => setRows([]),
      onState: setState,
    });
    f.start();
    return () => f.stop();
  }, []);
  return [rows, state];
}

function Summary({ g }: { g: GovernanceView }) {
  const open = g.proposals.filter((p) => p.status === "open").length;
  const byClass = (c: string) => g.proposals.reduce((n, p) => n + (p.tally[c]?.yes ?? 0) + (p.tally[c]?.no ?? 0) + (p.tally[c]?.abstain ?? 0), 0);
  return (
    <div className="stats">
      <Stat label="Proposals" value={count(g.proposals.length)} sub={`${count(open)} open${g.epoch !== undefined ? `, epoch ${g.epoch}` : ""}`} />
      {CLASSES.map((c) => <Stat key={c} label={`${CLASS_LABEL[c]} votes`} value={count(byClass(c))} />)}
      <Stat label="Lifetime" value={`${g.lifetime_epochs} epochs`} sub="a proposal stays open this long" />
    </div>
  );
}

export function Governance() {
  const { data: all, error } = usePoll<GovernanceView>("/api/governance", 3000);
  const [feed, state] = useVoteFeed();
  const enums = useEnums();
  const [filt, setFilt] = useState<GovFilter>({ voter: "", type: "" });
  const g = useMemo(() => (all ? filterGov(all, filt) : null), [all, filt]);
  const proposals = useMemo(() => (g ? sortProposals(g.proposals) : []), [g]);
  return (
    <>
      <h1>Governance</h1>
      <Blurb text={VIEW_BLURB.governance!} />
      <form className="filters" aria-label="Filter governance" onSubmit={(e) => e.preventDefault()}>
        <SelectField id="g-voter" help={FIELD_HELP.voter_class!} value={filt.voter} options={optionsOf(enums?.voter_class ?? [])} onChange={(v) => setFilt((f) => ({ ...f, voter: v }))} />
        <SelectField id="g-type" help={FIELD_HELP.action_type!} value={filt.type} options={optionsOf(enums?.action_type ?? [])} onChange={(v) => setFilt((f) => ({ ...f, type: v }))} />
      </form>
      <ErrorNote error={error} />
      {!g && !error && <p className="muted">Loading…</p>}
      {g && (
        <>
          <Summary g={g} />
          <h2>Proposals</h2>
          <p className="muted">Votes are each voter's latest; power is the stake behind the vote in the engine's chain view.</p>
          <ProposalTable proposals={proposals} />
          <h2>Votes on the chain</h2>
          <VoteTable votes={g.votes} empty="No votes on the chain yet." />
        </>
      )}
      <h2>Vote decisions <span className={`tag ${state === "live" ? "ok" : "warn"}`} role="status">{state}</span></h2>
      <p className="muted">The voters' vote intents and submissions as they happen, newest first, last {FEED_CAP}.</p>
      <ul className="feed">
        {feed.map((d) => (
          <li key={d.id}>
            <span className="when">{simTime(d.at)}</span>
            <span className="kind">{d.kind}</span>
            <Link to={`/actors/${d.actor}`}>actor {d.actor}</Link>
            {d.profile && <span className="muted"> {d.profile}</span>}
            {d.kind === "IntentChosen" && d.scores?.find((s) => s.kind === "vote" && s.proposal)?.why && (
              <span> {d.scores.find((s) => s.kind === "vote" && s.proposal)?.why}</span>
            )}
          </li>
        ))}
        {feed.length === 0 && <li className="empty">Waiting for vote decisions…</li>}
      </ul>
    </>
  );
}
