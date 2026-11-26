import { costBar, EMPTY_STATE, honestImpact, outcomeCounts } from "../lib/adversaries";
import { ada, count, simTime } from "../lib/format";
import { usePoll } from "../lib/hooks";
import type { AdversaryRow, AdversaryView } from "../lib/types";
import { ErrorNote, Link } from "./common";

const num = (x: number | null) => (x === null ? "-" : Number.isInteger(x) ? count(x) : x.toFixed(3));

export function Adversaries() {
  const { data, error } = usePoll<AdversaryView>("/api/adversaries", 4000);
  return (
    <>
      <h1>Adversaries</h1>
      <ErrorNote error={error} />
      {data && data.adversaries.length === 0 && (
        <section class="card empty-state" aria-labelledby="h-none">
          <h2 id="h-none">{EMPTY_STATE[0]}</h2>
          {EMPTY_STATE.slice(1).map((l) => <p key={l}>{l}</p>)}
        </section>
      )}
      {data && data.adversaries.map((a) => <Adversary key={a.id} a={a} control={data.has_control} />)}
      {data && data.adversaries.length > 0 && <Impact v={data} />}
    </>
  );
}

function Adversary({ a, control }: { a: AdversaryRow; control: boolean }) {
  const bar = costBar(a.cost);
  const fatal = a.findings.some((f) => f.severity === "fatal");
  return (
    <section class="card" aria-labelledby={`adv-${a.id}`}>
      <h2 id={`adv-${a.id}`}>
        <Link to={`/actors/${a.id}`}>{a.attack}</Link> <span class="muted">actor {a.id} · agenda {a.agenda || "unknown"} · budget {ada(a.budget)}</span>
      </h2>
      <div class="meter" role="img" aria-label={bar.label}>
        <span class={`bar-fill${bar.over ? " over" : ""}`} style={{ width: `${bar.pct}%` }} />
      </div>
      <p class="muted">{bar.label}; {ada(a.cost.spent)} over the run</p>
      <dl class="traits">
        <dt>Success metric</dt>
        <dd>{a.metric.name || "unset"}: <strong>{num(a.metric.value)}</strong>{control && ` (control ${num(a.metric.control)})`}</dd>
      </dl>
      <ul class="outcomes">
        {outcomeCounts(a).map(([k, v]) => <li key={k}><strong>{count(v)}</strong> {k.toLowerCase()}</li>)}
      </ul>
      {fatal && <p class="banner bad" role="alert">Fatal finding on this adversary's transaction.</p>}
      {a.findings.length > 0 && (
        <ul class="findings">
          {a.findings.map((f) => (
            <li key={f.id} class={f.severity === "fatal" ? "bad" : undefined}>
              <span class={`tag ${f.severity === "fatal" ? "bad" : "warn"}`}>{f.severity}</span> <strong>{f.kind}</strong> {f.summary}
            </li>
          ))}
        </ul>
      )}
      <h3>Recent attacks</h3>
      <div class="table-wrap">
        <table>
          <thead><tr><th>Time</th><th>Intent</th><th>Mutation</th><th class="num">Cost</th><th class="num">Value grabbed</th><th class="num">Txs</th></tr></thead>
          <tbody>
            {a.recent.map((r) => (
              <tr key={r.seq}>
                <td class="mono">{simTime(r.at)}</td><td>{r.intent}</td><td>{r.mutation || "-"}</td>
                <td class="num">{ada(r.cost)}</td><td class="num">{r.value ? ada(r.value) : "-"}</td><td class="num">{r.txs}</td>
              </tr>
            ))}
            {a.recent.length === 0 && <tr><td colSpan={6} class="empty">No attack steps yet.</td></tr>}
          </tbody>
        </table>
      </div>
    </section>
  );
}

function Impact({ v }: { v: AdversaryView }) {
  const rows = honestImpact(v);
  return (
    <section class="card" aria-labelledby="h-impact">
      <h2 id="h-impact">Impact on honest actors</h2>
      {!v.has_control && <p class="muted">Live counters. Serve with --control CONTROL/events.log for deltas against a run without attacks.</p>}
      <div class="table-wrap">
        <table>
          <thead><tr><th>Metric</th><th class="num">This run</th>{v.has_control && <><th class="num">Control</th><th class="num">Delta</th></>}</tr></thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.metric}>
                <td>{r.metric}</td><td class="num">{num(r.value)}</td>
                {v.has_control && <><td class="num">{num(r.control)}</td><td class="num">{num(r.delta)}</td></>}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}
