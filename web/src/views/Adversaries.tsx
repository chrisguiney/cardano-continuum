import { useEffect, useState } from "preact/hooks";
import { ADV_HELP } from "../lib/advhelp";
import { VIEW_BLURB } from "../lib/help";
import { useEnums } from "../lib/meta";
import { buildQuery, parseQuery, writeQuery } from "../lib/query";
import { costBar, EMPTY_STATE, honestImpact, outcomeCounts } from "../lib/adversaries";
import { ada, count, simTime } from "../lib/format";
import { usePoll } from "../lib/hooks";
import type { AdversaryRow, AdversaryView } from "../lib/types";
import { ErrorNote, Link } from "./common";
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
      <h1>Adversaries</h1>
      <Blurb text={VIEW_BLURB.adversaries!} />
      <form class="filters" aria-label="Filter adversaries" onSubmit={(e) => e.preventDefault()}>
        <AdversaryFilterBar f={filt} enums={enums} onChange={(k, v) => setFilt((f) => ({ ...f, [k]: v }))} />
      </form>
      <ErrorNote error={error} />
      {data && data.adversaries.length === 0 && (
        <section class="card empty-state" aria-labelledby="h-none">
          <h2 id="h-none">{EMPTY_STATE[0]}</h2>
          {EMPTY_STATE.slice(1).map((l) => <p key={l}>{l}</p>)}
        </section>
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
    <section class="card" aria-labelledby={`adv-${a.id}`}>
      <h2 id={`adv-${a.id}`}>
        <Link to={`/actors/${a.id}`}>{a.attack}</Link><AdvTip k={a.attack} scope={s} /> <span class="muted">actor {a.id}</span>
      </h2>
      <div class="meter" role="img" aria-label={bar.label}>
        <span class={`bar-fill${bar.over ? " over" : ""}`} style={{ width: `${bar.pct}%` }} />
      </div>
      <p class="muted">{bar.label}; {ada(a.cost.spent)} over the run</p>
      <dl class="traits">
        <dt>Cost ceiling<AdvTip k="cost_ceiling" scope={s} /></dt>
        <dd>{a.cost.ceiling > 0 ? `${ada(a.cost.ceiling)} per epoch` : "unknown"}</dd>
        <dt>Budget<AdvTip k="budget" scope={s} /></dt><dd>{ada(a.budget)}</dd>
        <dt>Agenda<AdvTip k="agenda" scope={s} /></dt><dd>{a.agenda || "unknown"}</dd>
        <dt>Success metric<AdvTip k="success_metric" scope={s} /></dt>
        <dd>{a.metric.name || "unset"}<AdvTip k={a.metric.name} scope={s} />: <strong>{num(a.metric.value)}</strong>{control && ` (control ${num(a.metric.control)})`}</dd>
      </dl>
      <ul class="outcomes">
        {outcomeCounts(a).map(([k, v]) => <li key={k}><strong>{count(v)}</strong> {k.toLowerCase()}<AdvTip k={OUTCOME_KEY[k] ?? ""} scope={s} /></li>)}
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
          <thead><tr><th>Time</th><th>Intent</th><th>Mutation<AdvTip k="mutation" scope={s} /></th><th class="num">Cost<AdvTip k="step_cost" scope={s} /></th><th class="num">Value grabbed<AdvTip k="step_value" scope={s} /></th><th class="num">Txs<AdvTip k="step_txs" scope={s} /></th></tr></thead>
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

export function Impact({ v }: { v: AdversaryView }) {
  const rows = honestImpact(v);
  return (
    <section class="card" aria-labelledby="h-impact">
      <h2 id="h-impact">Impact on honest actors</h2>
      <p class="muted">Honest means an intent no adversary made. Each metric is folded from the run's event log; the control is a second run with the same seed and no attacks.</p>
      {!v.has_control && <p class="muted">Live counters. Serve with --control CONTROL/events.log for deltas against a run without attacks.</p>}
      <div class="table-wrap">
        <table>
          <thead><tr><th>Metric<AdvTip k="metric" scope="imp" /></th><th class="num">This run<AdvTip k="this_run" scope="imp" /></th>{v.has_control && <><th class="num">Control<AdvTip k="control" scope="imp" /></th><th class="num">Delta<AdvTip k="delta" scope="imp" /></th></>}</tr></thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.metric}>
                <td>{r.metric}<AdvTip k={r.metric} scope="imp" /></td><td class="num">{num(r.value)}</td>
                {v.has_control && <><td class="num">{num(r.control)}</td><td class="num">{num(r.delta)}</td></>}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}
