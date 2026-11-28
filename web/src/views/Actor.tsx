import { detailSections } from "../lib/actordetail";
import { count, simTime } from "../lib/format";
import { usePoll } from "../lib/hooks";
import type { ActorDetail } from "../lib/types";
import { VIEW_BLURB } from "../lib/help";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { CopyButton, EmptyRow, ErrorNote, Facts, FindingList, Grid, Heading, Link, Mono, Muted, Note, Panel, TableFrame, Title } from "./common";
import { Blurb } from "./fields";

export function Actor({ id }: { id: number }) {
  const { data: d, error } = usePoll<ActorDetail>(`/api/actors/${id}`, 3000);
  return (
    <>
      <p className="mt-4"><Link to="/actors">← Actors</Link></p>
      <Title>Actor {id}{d?.name ? ` · ${d.name}` : ""}</Title>
      <Blurb text={VIEW_BLURB.actor!} />
      <ErrorNote error={error} />
      {d && <Detail d={d} />}
    </>
  );
}

function Detail({ d }: { d: ActorDetail }) {
  const sections = detailSections(d);
  return (
    <>
      <Grid>
        {sections.filter((s) => s.id !== "findings").map((s) => (
          <Panel key={s.id} aria-labelledby={`h-${s.id}`}>
            <Heading id={`h-${s.id}`}>{s.title}</Heading>
            {s.note && <Note>{s.note}</Note>}
            <Facts
              muted
              rows={s.rows.map((r) => [
                r.label,
                r.copy ? (
                  <Mono className="[overflow-wrap:anywhere]">
                    {r.value}
                    {r.value !== "none" && <> <CopyButton value={r.value} label={r.label} /></>}
                  </Mono>
                ) : r.value,
              ])}
            />
            {s.id === "holdings" && <Tokens d={d} />}
          </Panel>
        ))}
      </Grid>
      <Panel aria-labelledby="h-findings">
        <Heading id="h-findings">Findings</Heading>
        {d.findings.length === 0 ? <Note>No findings involve this actor's transactions.</Note> : <FindingList items={d.findings} />}
      </Panel>
      <Panel aria-labelledby="h-recent">
        <Heading id="h-recent">Recent decisions</Heading>
        {d.activity.recent.length === 0 && <Note>No intents chosen yet.</Note>}
        {d.activity.recent.map((r) => (
          <details key={r.id} className="my-1">
            <summary className="cursor-pointer text-primary"><Mono className="mr-2 text-muted-foreground">{simTime(r.at)}</Mono> chose <strong>{r.intent}</strong></summary>
            <Table className="my-1">
              <TableBody>
                {(r.scores ?? []).map((s, i) => (
                  <TableRow key={i} className={s.kind === r.intent ? "font-bold" : undefined}>
                    <TableCell>{s.kind}{s.target ? ` → ${s.target}` : ""}</TableCell>
                    <TableCell className="text-right tabular-nums">{s.value.toFixed(3)}</TableCell>
                    <TableCell className="whitespace-normal text-muted-foreground">{s.why}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </details>
        ))}
      </Panel>
      <Heading className="mt-6">Timeline</Heading>
      <ol className="rounded-lg border bg-card">
        {d.timeline.map((e) => (
          <li key={e.seq} className="border-b px-3 py-2 last:border-b-0">
            <Mono className="mr-2 text-muted-foreground">{simTime(e.at)}</Mono>
            <span className="mr-2 font-semibold">{e.kind}</span>
            <span>{e.detail}</span>
            {e.intent !== undefined && <Muted> intent {e.intent}</Muted>}
          </li>
        ))}
        {d.timeline.length === 0 && <li className="p-4 text-center text-muted-foreground">No events yet.</li>}
      </ol>
    </>
  );
}

function Tokens({ d }: { d: ActorDetail }) {
  const ts = d.holdings.tokens;
  return (
    <TableFrame tabIndex={0} role="region" aria-label="Native tokens, scrolls sideways">
      <Table aria-label="Native tokens">
        <TableHeader><TableRow><TableHead>Policy</TableHead><TableHead>Asset</TableHead><TableHead className="text-right">Quantity</TableHead></TableRow></TableHeader>
        <TableBody>
          {ts.map((t) => (
            <TableRow key={t.policy + t.name}>
              <TableCell><Mono>{t.policy.slice(0, 8)}…</Mono> <CopyButton value={t.policy} label="policy id" /></TableCell>
              <TableCell>{t.name_text || <Mono>{t.name || "(empty)"}</Mono>}</TableCell>
              <TableCell className="text-right tabular-nums">{count(t.quantity)}</TableCell>
            </TableRow>
          ))}
          {ts.length === 0 && <EmptyRow cols={3}>No native tokens.</EmptyRow>}
        </TableBody>
      </Table>
    </TableFrame>
  );
}
