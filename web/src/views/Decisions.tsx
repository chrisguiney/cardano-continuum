import { useEffect, useRef, useState } from "react";
import { Feed, foldDecisions, type EventSourceLike, type FeedFilter, type FeedRow, type FeedState, type IdleMode, type IdleRun } from "../lib/feed";
import { chances, clamp, percent } from "../lib/chance";
import { simTime } from "../lib/format";
import { FIELD_HELP, VIEW_BLURB } from "../lib/help";
import { useEnums } from "../lib/meta";
import { usePoll } from "../lib/hooks";
import { buildQuery, parseQuery, writeQuery } from "../lib/query";
import type { Decision, Snapshot } from "../lib/types";
import { Link } from "./common";
import { Blurb, HelpTip } from "./fields";
import { DecisionFilterBar, type DecisionFilterState } from "./filterbars";

const CAP = 500;
/** RAW_CAP bounds the decisions kept to re-fold when the idle mode changes. */
const RAW_CAP = 5000;
/** FLUSH_MS batches renders: one per interval however fast decisions arrive. */
const FLUSH_MS = 300;

function IdleRow({ r }: { r: IdleRun }) {
  const span = r.from === r.to ? simTime(r.from) : `${simTime(r.from)} – ${simTime(r.to)}`;
  return (
    <li className="idle-run">
      <span className="when">{span}</span>
      <span className="kind">idle</span>
      <span className="muted">
        {r.count.toLocaleString()} idle decision{r.count === 1 ? "" : "s"} from {r.actors.toLocaleString()} actor{r.actors === 1 ? "" : "s"}
      </span>
    </li>
  );
}

function Th({ id, k }: { id: string; k: string }) {
  const h = FIELD_HELP[k]!;
  return <th>{h.label} <HelpTip id={id} label={h.label} text={h.help} /></th>;
}

/** Why lists the scored options of a decision with the chance each had. */
export function Why({ d }: { d: Decision }) {
  if (!d.scores?.length && d.body === undefined) return null;
  const scores = d.scores ?? [];
  const pct = chances(scores);
  const pre = `why-${d.id}`;
  return (
    <details>
      <summary>{scores.length ? `why (${scores.length} scored)` : "body"}</summary>
      {scores.length ? (
        <>
          <table className="scores">
            <thead>
              <tr>
                <Th id={`${pre}-intent`} k="intent" />
                <Th id={`${pre}-score`} k="score" />
                <Th id={`${pre}-weight`} k="weight" />
                <Th id={`${pre}-chance`} k="chance" />
                <th>Why</th>
              </tr>
            </thead>
            <tbody>
              {scores.map((s, i) => (
                <tr key={i} className={s.kind === d.intent ? "chosen" : undefined}>
                  <td>{s.kind}{s.kind === d.intent ? " (chosen)" : ""}</td>
                  <td className="num">{s.value.toFixed(3)}</td>
                  <td className="num">{+clamp(s.weight ?? 1).toFixed(3)}</td>
                  <td className="num">
                    {pct[i]! > 0 ? percent(pct[i]!) : (
                      <>— <HelpTip id={`${pre}-z${i}`} label="a zero chance" text={`${s.why}. ${FIELD_HELP.zero_score!.help}`} /></>
                    )}
                  </td>
                  <td>{s.why}</td>
                </tr>
              ))}
            </tbody>
          </table>
          <p className="muted">
            Chance = score x weight / total; idle scores 1 as the baseline.
            {d.clock ? ` Chosen among ${d.clock} options after its fitted clock fired.` : ""}
          </p>
        </>
      ) : (
        <pre>{JSON.stringify(d.body, null, 1)}</pre>
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
      <h1>Decisions</h1>
      <Blurb text={VIEW_BLURB.decisions!} />
      <form className="filters" aria-label="Filter decisions" onSubmit={(e) => e.preventDefault()}>
        <DecisionFilterBar f={filt} enums={enums} profiles={snap?.population.by_profile ?? {}} onChange={(k, v) => setFilt((f) => ({ ...f, [k]: v }))} />
        <label title={FIELD_HELP.idle!.help}>
          {FIELD_HELP.idle!.label}{" "}
          <select value={mode} onChange={(e) => setMode((e.target as HTMLSelectElement).value as IdleMode)}>
            <option value="group">group</option>
            <option value="hide">hide</option>
            <option value="show">show each</option>
          </select>
        </label>
        <button type="button" title={FIELD_HELP.pause!.help} aria-pressed={paused} onClick={() => setPaused((p) => !p)}>
          {paused ? `Resume${waiting ? ` (${waiting.toLocaleString()} new)` : ""}` : "Pause"}
        </button>
        <span className={`tag ${state === "live" ? "ok" : "warn"}`} role="status">{paused ? "paused" : state}</span>
      </form>
      {gap && <p className="banner warn">Some decisions were dropped before this connection could replay them.</p>}
      <p className="muted">Newest first, last {CAP} rows shown.</p>
      <ul className="feed">
        {rows.map((r) =>
          r.type === "idle" ? (
            <IdleRow key={`i${r.key}`} r={r} />
          ) : (
            <li key={r.d.id}>
              <span className="when">{simTime(r.d.at)}</span>
              <span className="kind">{r.d.kind}</span>
              <Link to={`/actors/${r.d.actor}`}>actor {r.d.actor}</Link>
              {r.d.profile && <span className="muted"> {r.d.profile}</span>}
              {r.d.intent && <strong> {r.d.intent}</strong>}
              <Why d={r.d} />
            </li>
          ),
        )}
        {rows.length === 0 && <li className="empty">Waiting for decisions…</li>}
      </ul>
    </>
  );
}
