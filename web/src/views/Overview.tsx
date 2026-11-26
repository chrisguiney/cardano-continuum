import { ada, count, short, simTime } from "../lib/format";
import { usePoll } from "../lib/hooks";
import type { Snapshot } from "../lib/types";
import { ErrorNote, Stat } from "./common";

function AdaChart({ a }: { a: Snapshot["ada"] }) {
  const rows = [
    { label: "Submitted", v: a.submitted_lovelace, cls: "bar-a" },
    { label: "Confirmed", v: a.confirmed_lovelace, cls: "bar-b" },
    { label: "Fees", v: a.fees_lovelace, cls: "bar-c" },
  ];
  const max = Math.max(...rows.map((r) => r.v), 1);
  const w = 520;
  return (
    <svg viewBox={`0 0 ${w} 130`} role="img" aria-label="ADA accounting: submitted, confirmed and fees" class="chart">
      {rows.map((r, i) => {
        const y = 10 + i * 40;
        const len = Math.max((r.v / max) * 300, r.v > 0 ? 2 : 0);
        return (
          <g key={r.label}>
            <text x="0" y={y + 17} class="axis">{r.label}</text>
            <rect x="80" y={y} width={len} height="26" rx="3" class={r.cls} />
            <text x={80 + len + 8} y={y + 17} class="value">{ada(r.v)}</text>
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
    <section class="card">
      <h3>{title}</h3>
      {entries.length === 0 && <p class="muted">None yet.</p>}
      <ul class="bars">
        {entries.map(([k, v]) => (
          <li key={k}>
            <span class="bar-name">{k}</span>
            <span class="bar-track"><span class="bar-fill" style={{ width: `${(v / max) * 100}%` }} /></span>
            <span class="bar-num">{count(v)}</span>
          </li>
        ))}
      </ul>
    </section>
  );
}

export function Overview() {
  const { data: s, error } = usePoll<Snapshot>("/api/overview", 2000);
  return (
    <>
      <h1>Overview</h1>
      <ErrorNote error={error} />
      {!s && !error && <p class="muted">Loading…</p>}
      {s && (
        <>
          <div class="stats">
            <Stat label="Tip slot" value={s.tip ? count(s.tip.slot) : "none"} sub={s.tip ? short(s.tip.hash, 12) : "no block yet"} />
            <Stat label="Epoch" value={s.epoch === null ? "unknown" : String(s.epoch)} sub={`sim time ${simTime(s.sim_time_ms)}`} />
            <Stat label="Tx per second" value={s.tx_per_sec.toFixed(2)} sub={`${s.submit_per_sec.toFixed(2)} submitted/s`} />
            <Stat label="Blocks" value={count(s.blocks)} sub={`${count(s.rollbacks)} rollbacks`} />
            <Stat label="Actors" value={count(s.population.total)} />
          </div>
          <section class="card">
            <h2>ADA accounting</h2>
            <AdaChart a={s.ada} />
            <p class="muted">
              {count(s.txs.confirmed)} of {count(s.txs.submitted)} submissions confirmed; {count(s.txs.retries)} retries, {count(s.txs.refused)} refused,{" "}
              {count(s.txs.released)} released, {count(s.txs.gave_up)} gave up, {count(s.txs.skipped)} skipped.
            </p>
          </section>
          <div class="grid2">
            <Breakdown title="Population by profile" items={s.population.by_profile} />
            <Breakdown title="Population by state" items={s.population.by_state} />
          </div>
          <section class="card">
            <h2>Invariants</h2>
            <ul class="lights">
              {s.invariants.map((i) => (
                <li key={i.name} class={i.ok ? "ok" : "bad"}>
                  <span class="light" aria-hidden="true">{i.ok ? "✓" : "✗"}</span>
                  <span>{i.name}</span>
                  <span class="sr">{i.ok ? "holding" : "violated"}</span>
                  {i.detail && <span class="muted"> {i.detail}</span>}
                </li>
              ))}
            </ul>
          </section>
        </>
      )}
    </>
  );
}
