import { ada, count } from "../lib/format";
import { groupParams, govPath } from "../lib/govdetail";
import { GOV_HELP } from "../lib/govhelp";
import type { GovAnchor, GovContent, GovParam } from "../lib/types";
import { Badge } from "@/components/ui/badge";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { EmptyRow, Facts, Link, Mono, Muted, Note, TableFrame } from "./common";
import { HelpTip } from "./fields";
import { Addr, HashLink } from "./exparts";

/** Tip is the "?" for a governance term; id keeps the tip unique when a term shows twice on a page. */
export function Tip({ k, id = k }: { k: string; id?: string }) {
  const h = GOV_HELP[k]!;
  return <HelpTip id={`gt-${id}`} label={h.label} text={h.help} />;
}

/** Term is a term's name with its tip. */
export function Term({ k, id }: { k: string; id?: string }) {
  return <>{GOV_HELP[k]!.label}<Tip k={k} id={id} /></>;
}

export function AnchorLine({ a }: { a?: GovAnchor }) {
  if (!a) return <Muted>none</Muted>;
  return (
    <>
      <Mono className="[overflow-wrap:anywhere]">{a.url || "no URL"}</Mono>
      <div className="text-muted-foreground">hash <Mono className="[overflow-wrap:anywhere]" title={a.hash}>{a.hash}</Mono></div>
    </>
  );
}

/** Account shows a reward address, linked to the continuum actor that owns it when there is one. */
export function Account({ address, actor }: { address: string; actor?: { id: number; name: string } }) {
  return (
    <>
      <Addr value={address} />
      {actor && <> <Link to={`/actors/${actor.id}`}>{actor.name} (actor {actor.id})</Link></>}
    </>
  );
}

function Params({ ps }: { ps: GovParam[] }) {
  return (
    <TableFrame>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Parameter</TableHead><TableHead><Term k="group" /></TableHead><TableHead><Term k="security_group" id="security-col" /></TableHead>
            <TableHead className="text-right">New value</TableHead><TableHead className="text-right"><Term k="param_current" /></TableHead><TableHead>Unit</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {groupParams(ps).flatMap(([, rows]) => rows).map((p) => (
            <TableRow key={p.field}>
              <TableCell className="min-w-56 whitespace-normal"><strong>{p.name}</strong><div className="text-muted-foreground">{p.desc}</div></TableCell>
              <TableCell>{p.group || "other"}</TableCell>
              <TableCell>{p.security ? <Badge variant="warn">security group</Badge> : <Muted>no</Muted>}</TableCell>
              <TableCell className="text-right tabular-nums">{p.new}</TableCell>
              <TableCell className="text-right tabular-nums">{p.current ?? <Muted>unknown</Muted>}</TableCell>
              <TableCell>{p.unit}</TableCell>
            </TableRow>
          ))}
          {ps.length === 0 && <EmptyRow cols={6}>This action changes no parameter.</EmptyRow>}
        </TableBody>
      </Table>
    </TableFrame>
  );
}

function Treasury({ c, now }: { c: GovContent; now?: number }) {
  return (
    <TableFrame>
      <Table>
        <TableHeader><TableRow><TableHead>Recipient</TableHead><TableHead className="text-right">Amount</TableHead></TableRow></TableHeader>
        <TableBody>
          {(c.withdrawals ?? []).map((w) => (
            <TableRow key={w.address}><TableCell><Account address={w.address} actor={w.actor} /></TableCell><TableCell className="text-right tabular-nums">{ada(w.lovelace)}</TableCell></TableRow>
          ))}
          <TableRow><TableHead scope="row">Total</TableHead><TableCell className="text-right tabular-nums"><strong>{ada(c.total ?? 0)}</strong></TableCell></TableRow>
          {now !== undefined && (
            <>
              <TableRow><TableHead scope="row">Treasury now</TableHead><TableCell className="text-right tabular-nums">{ada(now)}</TableCell></TableRow>
              <TableRow><TableHead scope="row">After this withdrawal</TableHead><TableCell className="text-right tabular-nums">{ada(Math.max(now - (c.total ?? 0), 0))}</TableCell></TableRow>
            </>
          )}
        </TableBody>
      </Table>
    </TableFrame>
  );
}

function Committee({ c }: { c: GovContent }) {
  const removed = c.removed ?? [];
  const added = c.added ?? [];
  return (
    <Facts
      muted
      rows={[
        ["Removed members", removed.length ? <ul>{removed.map((m) => <li key={m}><Mono className="[overflow-wrap:anywhere]">{m}</Mono></li>)}</ul> : <Muted>none</Muted>],
        ["Added members", added.length ? <ul>{added.map((m) => <li key={m.credential}><Mono className="[overflow-wrap:anywhere]">{m.credential}</Mono> term ends epoch {m.epoch}</li>)}</ul> : <Muted>none</Muted>],
        [<Term k="quorum" />, c.quorum || <Muted>unset</Muted>],
      ]}
    />
  );
}

/** Contents is what the action asks for, by kind. */
export function Contents({ c, treasury }: { c: GovContent; treasury?: number }) {
  const what = GOV_HELP[c.kind];
  return (
    <>
      {what && <Note>{what.help}</Note>}
      {c.kind === "parameter_change" && <Params ps={c.params ?? []} />}
      {c.kind === "treasury_withdrawals" && <Treasury c={c} now={treasury} />}
      {c.kind === "update_committee" && <Committee c={c} />}
      {c.kind === "new_constitution" && c.constitution && (
        <Facts
          muted
          rows={[
            [<Term k="anchor" id="constitution-anchor" />, <AnchorLine a={c.constitution.anchor} />],
            [<Term k="guardrails" id="constitution-script" />, c.constitution.script_hash ? <HashLink value={c.constitution.script_hash} n={28} label="guardrails script hash" /> : <Muted>none</Muted>],
          ]}
        />
      )}
      {c.kind === "hard_fork_initiation" && c.version && (
        <Facts muted rows={[[<Term k="hard_fork" />, <strong>{c.version.major}.{c.version.minor}</strong>]]} />
      )}
      {(c.kind === "parameter_change" || c.kind === "treasury_withdrawals") && c.policy_hash && (
        <Facts muted className="mt-2" rows={[[<Term k="guardrails" />, <HashLink value={c.policy_hash} n={28} label="guardrails script hash" />]]} />
      )}
    </>
  );
}

/** PrevLink links the action this one builds on. */
export function PrevLink({ c }: { c: GovContent }) {
  if (!c.prev_action) return <Muted>none</Muted>;
  const to = govPath(`${c.prev_action.tx}#${c.prev_action.index}`);
  const text = `${c.prev_action.tx.slice(0, 10)}…#${c.prev_action.index}`;
  return to ? <Link to={to}>{text}</Link> : <Mono>{text}</Mono>;
}

export const votes = (n: number) => `${count(n)} ${n === 1 ? "vote" : "votes"}`;
