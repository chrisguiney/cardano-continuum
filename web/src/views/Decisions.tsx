import { useEffect, useRef, useState } from "react";
import { Feed, foldDecisions, type EventSourceLike, type FeedFilter, type FeedRow, type FeedState, type IdleMode, type IdleRun } from "../lib/feed";
import { chances, clamp, percent } from "../lib/chance";
import { simTime } from "../lib/format";
import { FIELD_HELP, VIEW_BLURB } from "../lib/help";
import { useEnums } from "../lib/meta";
import { usePoll } from "../lib/hooks";
import { buildQuery, parseQuery, writeQuery } from "../lib/query";
import type { Decision, Snapshot } from "../lib/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCaption, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Banner, Filters, Link, Mono, Muted, Note, Title } from "./common";
import { Blurb, HelpTip, SelectField } from "./fields";
import { DecisionFilterBar, type DecisionFilterState } from "./filterbars";

const CAP = 500;
/** RAW_CAP bounds the decisions kept to re-fold when the idle mode changes. */
const RAW_CAP = 5000;
/** FLUSH_MS batches renders: one per interval however fast decisions arrive. */
const FLUSH_MS = 300;

const ROW = "border-b px-3 py-2 last:border-b-0";

const IDLE_MODES = [{ value: "group", label: "group" }, { value: "hide", label: "hide" }, { value: "show", label: "show each" }];

function IdleRow({ r }: { r: IdleRun }) {
  const span = r.from === r.to ? simTime(r.from) : `${simTime(r.from)} – ${simTime(r.to)}`;
  return (
    <li className={`${ROW} text-[0.92em] text-muted-foreground`}>
      <Mono className="mr-2">{span}</Mono>
      <span className="mr-2 font-semibold">idle</span>
      <span>
        {r.count.toLocaleString()} idle decision{r.count === 1 ? "" : "s"} from {r.actors.toLocaleString()} actor{r.actors === 1 ? "" : "s"}
      </span>
    </li>
  );
}

function Th({ id, k, className }: { id: string; k: string; className?: string }) {
  const h = FIELD_HELP[k]!;
  return <TableHead className={className}>{h.label} <HelpTip id={id} label={h.label} text={h.help} /></TableHead>;
}

/** Why lists the scored options of a decision with the chance each had. */
export function Why({ d }: { d: Decision }) {
  if (!d.scores?.length && d.body === undefined) return null;
  const scores = d.scores ?? [];
  const pct = chances(scores);
  const pre = `why-${d.id}`;
  return (
    <details className="mt-1">
      <summary className="cursor-pointer text-primary">{scores.length ? `why (${scores.length} scored)` : "body"}</summary>
      {scores.length ? (
        <>
          <Table className="my-1 text-[0.9rem]">
            <TableCaption className="mt-1 text-left">
              Chance = score x weight / total; idle scores 1 as the baseline.
              {d.clock ? ` Chosen among ${d.clock} options after its fitted clock fired.` : ""}
            </TableCaption>
            <TableHeader>
              <TableRow>
                <Th id={`${pre}-intent`} k="intent" />
                <Th id={`${pre}-score`} k="score" className="text-right" />
                <Th id={`${pre}-weight`} k="weight" className="text-right" />
                <Th id={`${pre}-chance`} k="chance" className="text-right" />
                <TableHead>Why</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {scores.map((s, i) => {
                const chosen = s.kind === d.intent;
                return (
                  <TableRow key={i} data-chosen={chosen || undefined} className={chosen ? "bg-accent font-bold" : undefined}>
                    <TableCell>{s.kind}{chosen ? " (chosen)" : ""}</TableCell>
                    <TableCell className="text-right tabular-nums">{s.value.toFixed(3)}</TableCell>
                    <TableCell className="text-right tabular-nums">{+clamp(s.weight ?? 1).toFixed(3)}</TableCell>
                    <TableCell className="text-right tabular-nums">
                      {pct[i]! > 0 ? percent(pct[i]!) : (
                        <>— <HelpTip id={`${pre}-z${i}`} label="a zero chance" text={`${s.why}. ${FIELD_HELP.zero_score!.help}`} /></>
                      )}
                    </TableCell>
                    <TableCell className="min-w-48 whitespace-normal">{s.why}</TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </>
      ) : (
        <pre className="mt-1 max-h-56 overflow-auto rounded-md border bg-muted p-2 font-mono text-[0.85em]">{JSON.stringify(d.body, null, 1)}</pre>
      )}
    </details>
  );
}

const DEFAULTS: DecisionFilterState = { actor: "", profile: "", kind: "", intent: "" };

function parseFilter(f: DecisionFilterState): FeedFilter {
  const out: FeedFilter = {};
  if (/^\d+$/.test(f.actor.trim())) out.actor = Number(f.actor.trim());
  if (f.profile) out.profile = f.profile;
  if (f.kind) out.kinds = [f.kind];
  if (f.intent) out.intent = f.intent;
  return out;
}

export function Decisions() {
  const [rows, setRows] = useState<FeedRow[]>([]);
  const [state, setState] = useState<FeedState>("connecting");
  const [gap, setGap] = useState(false);
  const [filt, setFilt] = useState<DecisionFilterState>(() => parseQuery(location.search, DEFAULTS));
  const [mode, setMode] = useState<IdleMode>("group");
  const [paused, setPaused] = useState(false);
  const [waiting, setWaiting] = useState(0);
  const enums = useEnums();
  const { data: snap } = usePoll<Snapshot>("/api/overview", 10000);
  const feed = useRef<Feed | null>(null);
  // raw holds the newest decisions oldest first, so a mode change can
  // re-fold them; pending holds those not yet drawn.
  const raw = useRef<Decision[]>([]);
  const pending = useRef<Decision[]>([]);
  const view = useRef({ mode, paused });
  view.current = { mode, paused };

  useEffect(() => {
    const f = new Feed({
      base: "/api/decisions",
      filter: {},
      open: (u) => new EventSource(u) as unknown as EventSourceLike,
      onDecision: (d) => {
        raw.current.push(d);
        if (raw.current.length > RAW_CAP) raw.current.splice(0, raw.current.length - RAW_CAP);
        pending.current.push(d);
      },
      onGap: () => setGap(true),
      onReset: () => {
        raw.current = [];
        pending.current = [];
        setRows([]);
        setWaiting(0);
        setGap(false);
      },
      onState: setState,
    });
    feed.current = f;
    f.start();
    const t = setInterval(() => {
      const batch = pending.current;
      if (batch.length === 0) return;
      if (view.current.paused) {
        setWaiting(batch.length);
        return;
      }
      pending.current = [];
      setRows((r) => foldDecisions(r, batch, CAP, view.current.mode));
    }, FLUSH_MS);
    return () => {
      clearInterval(t);
      f.stop();
    };
  }, []);

  useEffect(() => {
    feed.current?.setFilter(parseFilter(filt));
    writeQuery(buildQuery({ ...filt }, DEFAULTS));
  }, [filt]);

  useEffect(() => {
    if (paused) return;
    pending.current = [];
    setWaiting(0);
    setRows(foldDecisions([], raw.current, CAP, mode));
  }, [mode, paused]);

  return (
    <>
      <Title>Decisions</Title>
      <Blurb text={VIEW_BLURB.decisions!} />
      <Filters aria-label="Filter decisions">
        <DecisionFilterBar f={filt} enums={enums} profiles={snap?.population.by_profile ?? {}} onChange={(k, v) => setFilt((f) => ({ ...f, [k]: v }))} />
        <SelectField id="f-idle" help={FIELD_HELP.idle!} value={mode} allLabel={null} options={IDLE_MODES} onChange={(v) => setMode(v as IdleMode)} />
        <span className="inline-flex items-center">
          <Button type="button" variant="outline" aria-pressed={paused} aria-describedby="f-pause-help" onClick={() => setPaused((p) => !p)}>
            {paused ? `Resume${waiting ? ` (${waiting.toLocaleString()} new)` : ""}` : "Pause"}
          </Button>
          <HelpTip id="f-pause-help" label={FIELD_HELP.pause!.label} text={FIELD_HELP.pause!.help} />
        </span>
        <Badge variant={state === "live" && !paused ? "ok" : "warn"} className="mb-2.5" role="status">{paused ? "paused" : state}</Badge>
      </Filters>
      {gap && <Banner tone="warn">Some decisions were dropped before this connection could replay them.</Banner>}
      <Note>Newest first, last {CAP} rows shown.</Note>
      <ul className="rounded-lg border bg-card">
        {rows.map((r) =>
          r.type === "idle" ? (
            <IdleRow key={`i${r.key}`} r={r} />
          ) : (
            <li key={r.d.id} className={ROW}>
              <Mono className="mr-2 text-muted-foreground">{simTime(r.d.at)}</Mono>
              <span className="mr-2 font-semibold">{r.d.kind}</span>
              <Link to={`/actors/${r.d.actor}`}>actor {r.d.actor}</Link>
              {r.d.profile && <Muted> {r.d.profile}</Muted>}
              {r.d.intent && <strong> {r.d.intent}</strong>}
              <Why d={r.d} />
            </li>
          ),
        )}
        {rows.length === 0 && <li className="p-4 text-center text-muted-foreground">Waiting for decisions…</li>}
      </ul>
    </>
  );
}
