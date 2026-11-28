import { useEffect, useMemo, useState } from "react";
import { Feed, appendDecision, type EventSourceLike, type FeedState } from "../lib/feed";
import { ada, count, short, simTime } from "../lib/format";
import { CLASSES, choiceLine, filterGov, forkLine, sortProposals, treasuryLine, versionText, yesShare, type GovFilter } from "../lib/governance";
import { govPath } from "../lib/govdetail";
import { GOV_HELP } from "../lib/govhelp";
import { FIELD_HELP, VIEW_BLURB } from "../lib/help";
import { optionsOf, useEnums } from "../lib/meta";
import { usePoll } from "../lib/hooks";
import type { Decision, GovernanceView, ProposalRow, VoteRow } from "../lib/types";
import { Badge } from "@/components/ui/badge";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { cn } from "../lib/utils";
import { EmptyRow, ErrorNote, Filters, Heading, Link, Mono, Muted, Note, Stat, Stats, TableFrame, Title } from "./common";
import { Blurb, HelpTip, SelectField } from "./fields";

const FEED_CAP = 100;

const CLASS_LABEL: Record<string, string> = { drep: "DRep", spo: "SPO", committee: "Constitutional committee" };

function Actor({ id, profile }: { id?: number; profile?: string }) {
  if (id === undefined) return <Muted>not ours</Muted>;
  return (
    <>
      <Link to={`/actors/${id}`}>actor {id}</Link>
      {profile && <Muted> {profile}</Muted>}
    </>
  );
}

function Share({ v }: { v: number | null }) {
  return v === null ? <Muted>–</Muted> : <span>{(v * 100).toFixed(0)}% yes</span>;
}

/** VoteTable lists votes, newest first. */
export function VoteTable({ votes, empty }: { votes: VoteRow[]; empty: string }) {
  return (
    <TableFrame>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead className="text-right">Slot</TableHead><TableHead>Voter</TableHead><TableHead>Actor</TableHead><TableHead>Proposal</TableHead>
            <TableHead>Type</TableHead><TableHead>Choice</TableHead><TableHead className="text-right">Power</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {votes.map((v) => (
            <TableRow key={`${v.tx}-${v.proposal}-${v.voter}`}>
              <TableCell className="text-right tabular-nums">{count(v.slot)}</TableCell>
              <TableCell>{CLASS_LABEL[v.voter] ?? v.voter}</TableCell>
              <TableCell><Actor id={v.actor} profile={v.profile} /></TableCell>
              <TableCell><ProposalLink id={v.proposal} /></TableCell>
              <TableCell>{v.type || "–"}</TableCell>
              <TableCell>{v.choice}</TableCell>
              <TableCell className="text-right tabular-nums">{v.power > 0 ? ada(v.power) : "–"}</TableCell>
            </TableRow>
          ))}
          {votes.length === 0 && <EmptyRow cols={7}>{empty}</EmptyRow>}
        </TableBody>
      </Table>
    </TableFrame>
  );
}

function ProposalLink({ id }: { id: string }) {
  const to = govPath(id);
  const text = <code className="text-[0.85em]">{short(id, 10)}</code>;
  return to ? <Link to={to}>{text}</Link> : text;
}

/** ColHead is a column heading with the "?" tip of a governance term. */
function ColHead({ k, label, id, className }: { k: string; label?: string; id: string; className?: string }) {
  const h = (GOV_HELP[k] ?? FIELD_HELP[k])!;
  return <TableHead className={className}>{label ?? h.label}<HelpTip id={`gh-${id}`} label={h.label} text={h.help} /></TableHead>;
}

const CLASS_TERM: Record<string, string> = { drep: "drep", spo: "spo", committee: "committee" };

/** ProposalTable lists proposals, each linking to its page with a one-line summary of what it asks for. */
export function ProposalTable({ proposals }: { proposals: ProposalRow[] }) {
  return (
    <TableFrame>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Proposal</TableHead><ColHead k="action_type" label="Type" id="type" /><TableHead>Proposer</TableHead><ColHead k="deposit" id="deposit" className="text-right" />
            <ColHead k="gov_lifetime" label="Expires" id="expires" className="text-right" /><ColHead k="status" id="status" />
            {CLASSES.map((c) => <ColHead key={c} k={CLASS_TERM[c]!} label={CLASS_LABEL[c]} id={`class-${c}`} />)}
          </TableRow>
        </TableHeader>
        <TableBody>
          {proposals.map((p) => (
            <TableRow key={p.id} className={cn(p.status === "expired" && "opacity-60")}>
              <TableCell><ProposalLink id={p.id} /></TableCell>
              <TableCell className="min-w-48 whitespace-normal">{p.type || "unknown"}{p.summary && <div className="text-muted-foreground">{p.summary}</div>}</TableCell>
              <TableCell><Actor id={p.proposer} profile={p.profile} /></TableCell>
              <TableCell className="text-right tabular-nums">{ada(p.deposit_lovelace)}</TableCell>
              <TableCell className="text-right tabular-nums">{p.expires_epoch === undefined ? "–" : `epoch ${p.expires_epoch}`}</TableCell>
              <TableCell><Badge variant={p.status === "open" || p.status === "ratified" || p.status === "enacted" ? "ok" : "warn"}>{p.status}</Badge></TableCell>
              {CLASSES.map((c) => {
                const t = p.tally[c];
                if (c === "spo" && !p.spo_may_vote) return <TableCell key={c} className="text-muted-foreground" title="the ledger does not let stake pools vote on this action">not eligible</TableCell>;
                return <TableCell key={c}>{t ? <>{choiceLine(t)}<br /><Share v={yesShare(t)} /></> : "–"}</TableCell>;
              })}
            </TableRow>
          ))}
          {proposals.length === 0 && <EmptyRow cols={9}>No proposals on the chain yet.</EmptyRow>}
        </TableBody>
      </Table>
    </TableFrame>
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
    <Stats>
      <Stat label="Proposals" value={count(g.proposals.length)} sub={`${count(open)} open${g.epoch !== undefined ? `, epoch ${g.epoch}` : ""}`} />
      {CLASSES.map((c) => <Stat key={c} label={`${CLASS_LABEL[c]} votes`} value={count(byClass(c))} />)}
      <Stat label="Lifetime" value={`${g.lifetime_epochs} epochs`} sub="a proposal stays open this long" />
      <Stat label="Protocol version" value={versionText(g.protocol_version)} sub={forkLine(g.pending_fork, g.scheduled_fork, g.proposals.some((p) => p.type === "hard_fork_initiation" && p.status === "open"))} />
      {g.treasury && <Stat label="Treasury" value={ada(g.treasury.now_lovelace)} sub={treasuryLine(g.treasury)} />}
    </Stats>
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
      <Title>Governance</Title>
      <Blurb text={VIEW_BLURB.governance!} />
      <Filters aria-label="Filter governance">
        <SelectField id="g-voter" help={FIELD_HELP.voter_class!} value={filt.voter} options={optionsOf(enums?.voter_class ?? [])} onChange={(v) => setFilt((f) => ({ ...f, voter: v }))} />
        <SelectField id="g-type" help={FIELD_HELP.action_type!} value={filt.type} options={optionsOf(enums?.action_type ?? [])} onChange={(v) => setFilt((f) => ({ ...f, type: v }))} />
      </Filters>
      <ErrorNote error={error} />
      {!g && !error && <Note>Loading…</Note>}
      {g && (
        <>
          <Summary g={g} />
          <Heading className="mt-6">Proposals</Heading>
          <Note>Votes are each voter's latest; power is the stake behind the vote in the engine's chain view.</Note>
          <ProposalTable proposals={proposals} />
          <Heading className="mt-6">Votes on the chain</Heading>
          <VoteTable votes={g.votes} empty="No votes on the chain yet." />
        </>
      )}
      <Heading className="mt-6 flex items-center gap-2">Vote decisions <Badge variant={state === "live" ? "ok" : "warn"} role="status">{state}</Badge></Heading>
      <Note>The voters' vote intents and submissions as they happen, newest first, last {FEED_CAP}.</Note>
      <ul className="rounded-lg border bg-card">
        {feed.map((d) => (
          <li key={d.id} className="border-b px-3 py-2 last:border-b-0">
            <Mono className="mr-2 text-muted-foreground">{simTime(d.at)}</Mono>
            <span className="mr-2 font-semibold">{d.kind}</span>
            <Link to={`/actors/${d.actor}`}>actor {d.actor}</Link>
            {d.profile && <Muted> {d.profile}</Muted>}
            {d.kind === "IntentChosen" && d.scores?.find((s) => s.kind === "vote" && s.proposal)?.why && (
              <span> {d.scores.find((s) => s.kind === "vote" && s.proposal)?.why}</span>
            )}
          </li>
        ))}
        {feed.length === 0 && <li className="p-4 text-center text-muted-foreground">Waiting for vote decisions…</li>}
      </ul>
    </>
  );
}
