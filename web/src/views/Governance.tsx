import { useEffect, useMemo, useState } from "preact/hooks";
import { Feed, appendDecision, type EventSourceLike, type FeedState } from "../lib/feed";
import { ada, count, short, simTime } from "../lib/format";
import { CLASSES, choiceLine, sortProposals, yesShare } from "../lib/governance";
import { usePoll } from "../lib/hooks";
import type { Decision, GovernanceView, VoteRow } from "../lib/types";
import { ErrorNote, Link, Stat } from "./common";

const FEED_CAP = 100;

const CLASS_LABEL: Record<string, string> = { drep: "DRep", spo: "SPO", committee: "Committee" };

function Actor({ id, profile }: { id?: number; profile?: string }) {
  if (id === undefined) return <span class="muted">not ours</span>;
  return (
    <>
      <Link to={`/actors/${id}`}>actor {id}</Link>
      {profile && <span class="muted"> {profile}</span>}
    </>
  );
}

function Share({ v }: { v: number | null }) {
  return v === null ? <span class="muted">–</span> : <span>{(v * 100).toFixed(0)}% yes</span>;
}

/** VoteTable lists votes, newest first. */
export function VoteTable({ votes, empty }: { votes: VoteRow[]; empty: string }) {
  return (
    <div class="table-wrap">
      <table>
        <thead>
          <tr><th>Slot</th><th>Voter</th><th>Actor</th><th>Proposal</th><th>Type</th><th>Choice</th><th>Power</th></tr>
        </thead>
        <tbody>
          {votes.map((v) => (
            <tr key={`${v.tx}-${v.proposal}-${v.voter}`}>
              <td class="num">{count(v.slot)}</td>
              <td>{CLASS_LABEL[v.voter] ?? v.voter}</td>
              <td><Actor id={v.actor} profile={v.profile} /></td>
              <td><code>{short(v.proposal, 10)}</code></td>
              <td>{v.type || "–"}</td>
              <td>{v.choice}</td>
              <td class="num">{v.power > 0 ? ada(v.power) : "–"}</td>
            </tr>
          ))}
          {votes.length === 0 && <tr><td colSpan={7} class="empty">{empty}</td></tr>}
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
    <div class="stats">
      <Stat label="Proposals" value={count(g.proposals.length)} sub={`${count(open)} open${g.epoch !== undefined ? `, epoch ${g.epoch}` : ""}`} />
      {CLASSES.map((c) => <Stat key={c} label={`${CLASS_LABEL[c]} votes`} value={count(byClass(c))} />)}
      <Stat label="Lifetime" value={`${g.lifetime_epochs} epochs`} sub="a proposal stays open this long" />
    </div>
  );
}

export function Governance() {
  const { data: g, error } = usePoll<GovernanceView>("/api/governance", 3000);
  const [feed, state] = useVoteFeed();
  const proposals = useMemo(() => (g ? sortProposals(g.proposals) : []), [g]);
  return (
    <>
      <h1>Governance</h1>
      <ErrorNote error={error} />
      {!g && !error && <p class="muted">Loading…</p>}
      {g && (
        <>
          <Summary g={g} />
          <h2>Proposals</h2>
          <p class="muted">Votes are each voter's latest; power is the stake behind the vote in the engine's chain view.</p>
          <div class="table-wrap">
            <table>
              <thead>
                <tr><th>Proposal</th><th>Type</th><th>Proposer</th><th>Deposit</th><th>Expires</th><th>Status</th>
                  {CLASSES.map((c) => <th key={c}>{CLASS_LABEL[c]}</th>)}</tr>
              </thead>
              <tbody>
                {proposals.map((p) => (
                  <tr key={p.id} class={p.status === "open" ? undefined : "dim"}>
                    <td><code>{short(p.id, 10)}</code></td>
                    <td>{p.type || "unknown"}</td>
                    <td><Actor id={p.proposer} profile={p.profile} /></td>
                    <td class="num">{ada(p.deposit_lovelace)}</td>
                    <td class="num">{p.expires_epoch === undefined ? "–" : `epoch ${p.expires_epoch}`}</td>
                    <td><span class={`tag ${p.status === "open" ? "ok" : "warn"}`}>{p.status}</span></td>
                    {CLASSES.map((c) => {
                      const t = p.tally[c];
                      if (c === "spo" && !p.spo_may_vote) return <td key={c} class="muted" title="the ledger does not let stake pools vote on this action">not eligible</td>;
                      return <td key={c}>{t ? <>{choiceLine(t)}<br /><Share v={yesShare(t)} /></> : "–"}</td>;
                    })}
                  </tr>
                ))}
                {proposals.length === 0 && <tr><td colSpan={9} class="empty">No proposals on the chain yet.</td></tr>}
              </tbody>
            </table>
          </div>
          <h2>Votes on the chain</h2>
          <VoteTable votes={g.votes} empty="No votes on the chain yet." />
        </>
      )}
      <h2>Vote decisions <span class={`tag ${state === "live" ? "ok" : "warn"}`} role="status">{state}</span></h2>
      <p class="muted">The voters' vote intents and submissions as they happen, newest first, last {FEED_CAP}.</p>
      <ul class="feed">
        {feed.map((d) => (
          <li key={d.id}>
            <span class="when">{simTime(d.at)}</span>
            <span class="kind">{d.kind}</span>
            <Link to={`/actors/${d.actor}`}>actor {d.actor}</Link>
            {d.profile && <span class="muted"> {d.profile}</span>}
            {d.kind === "IntentChosen" && d.scores?.find((s) => s.kind === "vote" && s.proposal)?.why && (
              <span> {d.scores.find((s) => s.kind === "vote" && s.proposal)?.why}</span>
            )}
          </li>
        ))}
        {feed.length === 0 && <li class="empty">Waiting for vote decisions…</li>}
      </ul>
    </>
  );
}
