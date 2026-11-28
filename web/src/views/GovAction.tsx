import { usePoll } from "../lib/hooks";
import { ada, count, short } from "../lib/format";
import { CLASSES, choiceLine, yesShare } from "../lib/governance";
import { govApi, securityNames, voterLink } from "../lib/govdetail";
import type { GovDetail, GovVote } from "../lib/types";
import { Badge } from "@/components/ui/badge";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { EmptyRow, ErrorNote, Facts, Heading, Link, Mono, Muted, Note, Panel, TableFrame, Title } from "./common";
import type { ReactNode } from "react";
import { Blurb, HelpTip } from "./fields";
import { TxHash } from "./exparts";
import { Account, AnchorLine, Contents, PrevLink, Term, Tip } from "./govparts";

const CLASS_LABEL: Record<string, string> = { drep: "DRep", spo: "SPO", committee: "Constitutional committee" };

function Header({ d }: { d: GovDetail }) {
  const c = d.content;
  const p = d.proposal;
  const rows: [ReactNode, ReactNode][] = [
    ["Action type", <Badge>{p.type || "unknown"}</Badge>],
    ...(c ? ([[<Term k="return_address" />, <Account address={c.return_address} actor={c.return_actor} />]] as [ReactNode, ReactNode][]) : []),
    ...(!c && p.proposer !== undefined ? ([["Proposer", <Link to={`/actors/${p.proposer}`}>actor {p.proposer}</Link>]] as [ReactNode, ReactNode][]) : []),
    [<Term k="deposit" />, ada(p.deposit_lovelace)],
    ["Submitted", <>{p.epoch === undefined ? "epoch unknown" : `epoch ${p.epoch}`}, slot {count(p.slot)} · transaction <TxHash value={d.id.split("#")[0]!} n={12} /></>],
    [<Term k="gov_lifetime" />, <>{p.expires_epoch === undefined ? "–" : `last epoch ${p.expires_epoch}`} <Muted>({d.lifetime_epochs} epochs from submission{d.epoch !== undefined ? `, now epoch ${d.epoch}` : ""})</Muted></>],
    [<Term k="status" />, <><Badge variant={p.status === "open" || p.status === "ratified" ? "ok" : "warn"}>{p.status}</Badge> <Muted>{p.status === "ratified" ? "by the votes the run saw" : "ratified once its votes meet the thresholds"} <Tip k="ratification" /></Muted></>],
    ...(c ? ([[<Term k="anchor" />, <AnchorLine a={c.anchor} />], [<Term k="prev_action" />, <PrevLink c={c} />]] as [ReactNode, ReactNode][]) : []),
  ];
  return (
    <Panel aria-labelledby="g-head">
      <Heading id="g-head">Header</Heading>
      <Facts muted rows={rows} />
    </Panel>
  );
}

function Eligibility({ d }: { d: GovDetail }) {
  const sec = d.content?.params ? securityNames(d.content.params) : [];
  return (
    <Panel aria-labelledby="g-who">
      <Heading id="g-who"><Term k="eligibility" /></Heading>
      {sec.length > 0 && <Note>This change touches the security group ({sec.join(", ")}), so stake pools vote too.</Note>}
      <TableFrame>
        <Table>
          <TableHeader><TableRow><TableHead>Voter</TableHead><TableHead>May vote</TableHead><TableHead>Why</TableHead></TableRow></TableHeader>
          <TableBody>
            {d.voters.map((v) => (
              <TableRow key={v.class}>
                <TableCell>{GOV_LABEL(v.class)}</TableCell>
                <TableCell>{v.allowed ? <Badge variant="ok">yes</Badge> : <Badge variant="warn">not eligible</Badge>}</TableCell>
                <TableCell className="whitespace-normal">{v.reason} <HelpTip id={`rule-${v.class}`} label={`${CLASS_LABEL[v.class] ?? v.class} rule`} text={`${v.reason} Rule: ${v.rule}, cardano-ledger.`} /></TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </TableFrame>
    </Panel>
  );
}

const GOV_LABEL = (c: string) => CLASS_LABEL[c] ?? c;

function Tallies({ d }: { d: GovDetail }) {
  return (
    <TableFrame>
      <Table>
        <TableHeader><TableRow><TableHead>Class</TableHead><TableHead>Votes <Tip k="tally" /></TableHead><TableHead>Share</TableHead></TableRow></TableHeader>
        <TableBody>
          {CLASSES.map((c) => {
            const t = d.proposal.tally[c];
            const eligible = d.voters.find((v) => v.class === c)?.allowed ?? true;
            const share = t ? yesShare(t) : null;
            return (
              <TableRow key={c}>
                <TableCell>{c === "drep" ? <Term k="drep" id="tally-drep" /> : c === "spo" ? <Term k="spo" id="tally-spo" /> : <Term k="committee" id="tally-committee" />}</TableCell>
                <TableCell>{!eligible ? <Muted>not eligible</Muted> : t ? choiceLine(t) : "no votes"}</TableCell>
                <TableCell>{eligible && share !== null ? `${(share * 100).toFixed(0)}% yes` : <Muted>–</Muted>}</TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
    </TableFrame>
  );
}

function VoteList({ votes }: { votes: GovVote[] }) {
  return (
    <TableFrame>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead className="text-right">Slot</TableHead><TableHead>Voter</TableHead><TableHead>Id</TableHead><TableHead>Choice</TableHead>
            <TableHead className="text-right"><Term k="voting_power" /></TableHead><TableHead>Transaction</TableHead><TableHead>Anchor</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {votes.map((v) => {
            const to = voterLink(v.actor);
            return (
              <TableRow key={`${v.tx}-${v.voter}-${v.voter_id}`}>
                <TableCell className="text-right tabular-nums">{count(v.slot)}</TableCell>
                <TableCell>{GOV_LABEL(v.voter)}</TableCell>
                <TableCell>{to ? <Link to={to}>actor {v.actor}</Link> : <Mono title={v.voter_id}>{short(v.voter_id, 10)}</Mono>}{v.profile && <Muted> {v.profile}</Muted>}</TableCell>
                <TableCell><Badge>{v.choice}</Badge></TableCell>
                <TableCell className="text-right tabular-nums">{v.power > 0 ? ada(v.power) : "–"}</TableCell>
                <TableCell><TxHash value={v.tx} n={10} /></TableCell>
                <TableCell>{v.anchor ? <Mono title={v.anchor.hash}>{v.anchor.url}</Mono> : <Muted>none</Muted>}</TableCell>
              </TableRow>
            );
          })}
          {votes.length === 0 && <EmptyRow cols={7}>No votes on this action yet.</EmptyRow>}
        </TableBody>
      </Table>
    </TableFrame>
  );
}

/** GovBody is one governance action in full: header, contents, who may vote and the votes. */
export function GovBody({ d }: { d: GovDetail }) {
  return (
    <>
      <Title><Term k="governance_action" /> <Muted className="font-normal">{d.proposal.type}</Muted></Title>
      {d.summary && <Blurb text={d.summary} />}
      <Header d={d} />
      <Panel aria-labelledby="g-body">
        <Heading id="g-body">What it asks for</Heading>
        {d.content ? <Contents c={d.content} treasury={d.treasury_lovelace} /> : <Note>The explorer has not decoded this action (its transaction is not in the index), so only the type is known.</Note>}
      </Panel>
      <Eligibility d={d} />
      <Panel aria-labelledby="g-votes">
        <Heading id="g-votes">Votes</Heading>
        <Tallies d={d} />
        <VoteList votes={d.votes} />
      </Panel>
    </>
  );
}

export function GovAction({ tx, index }: { tx: string; index: number }) {
  const { data, error } = usePoll<GovDetail>(govApi(tx, index), 3000);
  return (
    <>
      <p className="mt-4"><Link to="/governance">All governance actions</Link></p>
      <ErrorNote error={error} />
      {!data && !error && <Note>Loading…</Note>}
      {data && <GovBody d={data} />}
    </>
  );
}
