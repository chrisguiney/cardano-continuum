import { ada, count, short, simTime } from "../lib/format";
import { usePoll } from "../lib/hooks";
import type { Snapshot } from "../lib/types";
import { VIEW_BLURB } from "../lib/help";
import { cn } from "../lib/utils";
import { ErrorNote, Grid, Heading, Muted, Note, Panel, Stat, Stats, Subheading, Title } from "./common";
import { Blurb } from "./fields";

function AdaChart({ a }: { a: Snapshot["ada"] }) {
  const rows = [
    { label: "Submitted", v: a.submitted_lovelace, cls: "fill-chart-a" },
    { label: "Confirmed", v: a.confirmed_lovelace, cls: "fill-chart-b" },
    { label: "Fees", v: a.fees_lovelace, cls: "fill-chart-c" },
  ];
  const max = Math.max(...rows.map((r) => r.v), 1);
  const w = 520;
  return (
    <svg viewBox={`0 0 ${w} 130`} role="img" aria-label="ADA accounting: submitted, confirmed and fees" className="h-auto w-full max-w-[640px] text-[13px]">
      {rows.map((r, i) => {
        const y = 10 + i * 40;
        const len = Math.max((r.v / max) * 300, r.v > 0 ? 2 : 0);
        return (
          <g key={r.label}>
            <text x="0" y={y + 17} className="fill-muted-foreground">{r.label}</text>
            <rect x="80" y={y} width={len} height="26" rx="3" className={r.cls} />
            <text x={80 + len + 8} y={y + 17} className="fill-foreground">{ada(r.v)}</text>
          </g>
        );
      })}
    </svg>
  );
}

function Breakdown({ title, items }: { title: string; items: Record<string, number> }) {
  const entries = Object.entries(items).sort((a, b) => b[1] - a[1]);
  const max = Math.max(...entries.map((e) => e[1]), 1);
  return (
    <Panel>
      <Subheading className="mt-0">{title}</Subheading>
      {entries.length === 0 && <Note>None yet.</Note>}
      <ul>
        {entries.map(([k, v]) => (
          <li key={k} className="my-1 grid grid-cols-[5rem_1fr_3rem] items-center gap-2 sm:grid-cols-[8rem_1fr_4rem]">
            <span className="truncate" title={k}>{k}</span>
            <span className="h-3 overflow-hidden rounded bg-muted"><span className="block h-full bg-chart-a" style={{ width: `${(v / max) * 100}%` }} /></span>
            <span className="text-right tabular-nums">{count(v)}</span>
          </li>
        ))}
      </ul>
    </Panel>
  );
}

export function Overview() {
  const { data: s, error } = usePoll<Snapshot>("/api/overview", 2000);
  return (
    <>
      <Title>Overview</Title>
      <Blurb text={VIEW_BLURB.overview!} />
      <ErrorNote error={error} />
      {!s && !error && <Note>Loading…</Note>}
      {s && (
        <>
          <Stats>
            <Stat help="Slot number of the newest block. A slot is one tick of the chain clock." label="Tip slot" value={s.tip ? count(s.tip.slot) : "none"} sub={s.tip ? short(s.tip.hash, 12) : "no block yet"} />
            <Stat help="A fixed run of slots; stake and rewards are recalculated at its end." label="Epoch" value={s.epoch === null ? "unknown" : String(s.epoch)} sub={`sim time ${simTime(s.sim_time_ms)}`} />
            <Stat help="Transactions confirmed per second of simulated time." label="Tx per second" value={s.tx_per_sec.toFixed(2)} sub={`${s.submit_per_sec.toFixed(2)} submitted/s`} />
            <Stat help="Blocks made so far; a rollback replaces recent blocks with a different branch." label="Blocks" value={count(s.blocks)} sub={`${count(s.rollbacks)} rollbacks`} />
            <Stat help="Simulated participants alive in the run." label="Actors" value={count(s.population.total)} />
          </Stats>
          <Panel>
            <Heading>ADA accounting</Heading>
            <AdaChart a={s.ada} />
            <Note>
              {count(s.txs.confirmed)} of {count(s.txs.submitted)} submissions confirmed; {count(s.txs.retries)} retries, {count(s.txs.refused)} refused,{" "}
              {count(s.txs.released)} released, {count(s.txs.gave_up)} gave up, {count(s.txs.skipped)} skipped.
            </Note>
          </Panel>
          <Grid>
            <Breakdown title="Population by profile" items={s.population.by_profile} />
            <Breakdown title="Population by state" items={s.population.by_state} />
          </Grid>
          <Panel>
            <Heading>Invariants</Heading>
            <ul className="flex flex-wrap gap-x-6 gap-y-2">
              {s.invariants.map((i) => (
                <li key={i.name} className="flex items-center gap-1.5">
                  <span className={cn("inline-grid size-6 place-items-center rounded-full font-bold text-background", i.ok ? "bg-ok" : "bg-destructive")} aria-hidden="true">{i.ok ? "✓" : "✗"}</span>
                  <span>{i.name}</span>
                  <span className="sr-only">{i.ok ? "holding" : "violated"}</span>
                  {i.detail && <Muted> {i.detail}</Muted>}
                </li>
              ))}
            </ul>
          </Panel>
        </>
      )}
    </>
  );
}
