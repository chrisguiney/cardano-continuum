import { useEffect, useState } from "react";
import { ADV_HELP } from "../lib/advhelp";
import { VIEW_BLURB } from "../lib/help";
import { useEnums } from "../lib/meta";
import { buildQuery, parseQuery, writeQuery } from "../lib/query";
import { costBar, EMPTY_STATE, honestImpact, outcomeCounts } from "../lib/adversaries";
import { ada, count, simTime } from "../lib/format";
import { usePoll } from "../lib/hooks";
import type { AdversaryRow, AdversaryView } from "../lib/types";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { cn } from "../lib/utils";
import { Banner, EmptyRow, ErrorNote, Facts, Filters, FindingList, Heading, Link, Mono, Muted, Note, Panel, Subheading, TableFrame, Title } from "./common";
import { Blurb, HelpTip } from "./fields";
import { AdversaryFilterBar, type AdversaryFilterState } from "./filterbars";

const num = (x: number | null) => (x === null ? "-" : Number.isInteger(x) ? count(x) : x.toFixed(3));

/** AdvTip is the "?" for an adversary term; scope keeps ids unique when a term shows on several cards. */
function AdvTip({ k, scope }: { k: string; scope: string }) {
  const h = ADV_HELP[k];
  return h ? <HelpTip id={`adv-${scope}-${k}`} label={h.label} text={h.help} /> : null;
}

const OUTCOME_KEY: Record<string, string> = { Attempted: "attempted", Landed: "landed", Detected: "detected", Rejected: "rejected" };

const ADV_DEFAULTS: AdversaryFilterState = { attack: "", agenda: "" };

export function Adversaries() {
  const [filt, setFilt] = useState<AdversaryFilterState>(() => parseQuery(location.search, ADV_DEFAULTS));
  const enums = useEnums();
  useEffect(() => writeQuery(buildQuery({ ...filt }, ADV_DEFAULTS)), [filt]);
  const { data, error } = usePoll<AdversaryView>("/api/adversaries", 4000);
  return (
    <>
      <Title>Adversaries</Title>
      <Blurb text={VIEW_BLURB.adversaries!} />
      <Filters aria-label="Filter adversaries">
        <AdversaryFilterBar f={filt} enums={enums} onChange={(k, v) => setFilt((f) => ({ ...f, [k]: v }))} />
      </Filters>
      <ErrorNote error={error} />
      {data && data.adversaries.length === 0 && (
        <Panel className="mx-auto my-8 max-w-[60ch]" aria-labelledby="h-none">
          <Heading id="h-none">{EMPTY_STATE[0]}</Heading>
          {EMPTY_STATE.slice(1).map((l) => <p key={l} className="my-2">{l}</p>)}
        </Panel>
      )}
      {data && data.adversaries.filter((a) => (!filt.attack || a.attack === filt.attack) && (!filt.agenda || a.agenda === filt.agenda)).map((a) => <Adversary key={a.id} a={a} control={data.has_control} />)}
      {data && data.adversaries.length > 0 && <Impact v={data} />}
    </>
  );
}

export function Adversary({ a, control }: { a: AdversaryRow; control: boolean }) {
  const bar = costBar(a.cost);
  const s = `a${a.id}`;
  const fatal = a.findings.some((f) => f.severity === "fatal");
  return (
    <Panel aria-labelledby={`adv-${a.id}`}>
      <Heading id={`adv-${a.id}`}>
        <Link to={`/actors/${a.id}`}>{a.attack}</Link><AdvTip k={a.attack} scope={s} /> <Muted className="font-normal">actor {a.id}</Muted>
      </Heading>
      <div className="h-4 overflow-hidden rounded border bg-muted" role="img" aria-label={bar.label}>
        <span className={cn("block h-full", bar.over ? "bg-destructive" : "bg-chart-b")} style={{ width: `${bar.pct}%` }} />
      </div>
      <Note>{bar.label}; {ada(a.cost.spent)} over the run</Note>
      <Facts
        muted
        rows={[
          [<>Cost ceiling<AdvTip k="cost_ceiling" scope={s} /></>, a.cost.ceiling > 0 ? `${ada(a.cost.ceiling)} per epoch` : "unknown"],
          [<>Budget<AdvTip k="budget" scope={s} /></>, ada(a.budget)],
          [<>Agenda<AdvTip k="agenda" scope={s} /></>, a.agenda || "unknown"],
          [<>Success metric<AdvTip k="success_metric" scope={s} /></>, <>{a.metric.name || "unset"}<AdvTip k={a.metric.name} scope={s} />: <strong>{num(a.metric.value)}</strong>{control && ` (control ${num(a.metric.control)})`}</>],
        ]}
      />
      <ul className="my-3 flex flex-wrap gap-6">
        {outcomeCounts(a).map(([k, v]) => <li key={k}><strong>{count(v)}</strong> {k.toLowerCase()}<AdvTip k={OUTCOME_KEY[k] ?? ""} scope={s} /></li>)}
      </ul>
      {fatal && <Banner tone="bad" role="alert">Fatal finding on this adversary's transaction.</Banner>}
      {a.findings.length > 0 && <FindingList items={a.findings} />}
      <Subheading>Recent attacks</Subheading>
      <TableFrame>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Time</TableHead><TableHead>Intent</TableHead><TableHead>Mutation<AdvTip k="mutation" scope={s} /></TableHead>
              <TableHead className="text-right">Cost<AdvTip k="step_cost" scope={s} /></TableHead><TableHead className="text-right">Value grabbed<AdvTip k="step_value" scope={s} /></TableHead>
              <TableHead className="text-right">Txs<AdvTip k="step_txs" scope={s} /></TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {a.recent.map((r) => (
              <TableRow key={r.seq}>
                <TableCell><Mono>{simTime(r.at)}</Mono></TableCell><TableCell>{r.intent}</TableCell><TableCell>{r.mutation || "-"}</TableCell>
                <TableCell className="text-right tabular-nums">{ada(r.cost)}</TableCell><TableCell className="text-right tabular-nums">{r.value ? ada(r.value) : "-"}</TableCell><TableCell className="text-right tabular-nums">{r.txs}</TableCell>
              </TableRow>
            ))}
            {a.recent.length === 0 && <EmptyRow cols={6}>No attack steps yet.</EmptyRow>}
          </TableBody>
        </Table>
      </TableFrame>
    </Panel>
  );
}

export function Impact({ v }: { v: AdversaryView }) {
  const rows = honestImpact(v);
  return (
    <Panel aria-labelledby="h-impact">
      <Heading id="h-impact">Impact on honest actors</Heading>
      <Note>Honest means an intent no adversary made. Each metric is folded from the run's event log; the control is a second run with the same seed and no attacks.</Note>
      {!v.has_control && <Note>Live counters. Serve with --control CONTROL/events.log for deltas against a run without attacks.</Note>}
      <TableFrame>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Metric<AdvTip k="metric" scope="imp" /></TableHead><TableHead className="text-right">This run<AdvTip k="this_run" scope="imp" /></TableHead>
              {v.has_control && <><TableHead className="text-right">Control<AdvTip k="control" scope="imp" /></TableHead><TableHead className="text-right">Delta<AdvTip k="delta" scope="imp" /></TableHead></>}
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((r) => (
              <TableRow key={r.metric}>
                <TableCell>{r.metric}<AdvTip k={r.metric} scope="imp" /></TableCell><TableCell className="text-right tabular-nums">{num(r.value)}</TableCell>
                {v.has_control && <><TableCell className="text-right tabular-nums">{num(r.control)}</TableCell><TableCell className="text-right tabular-nums">{num(r.delta)}</TableCell></>}
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </TableFrame>
    </Panel>
  );
}
