import { useEffect, useRef, useState } from "preact/hooks";
import { Feed, appendDecision, type EventSourceLike, type FeedFilter, type FeedState } from "../lib/feed";
import { simTime } from "../lib/format";
import { VIEW_BLURB } from "../lib/help";
import { useEnums } from "../lib/meta";
import { usePoll } from "../lib/hooks";
import { buildQuery, parseQuery, writeQuery } from "../lib/query";
import type { Decision, Snapshot } from "../lib/types";
import { Link } from "./common";
import { Blurb } from "./fields";
import { DecisionFilterBar, type DecisionFilterState } from "./filterbars";

const CAP = 500;

function Why({ d }: { d: Decision }) {
  if (!d.scores?.length && d.body === undefined) return null;
  return (
    <details>
      <summary>{d.scores?.length ? `why (${d.scores.length} scored)` : "body"}</summary>
      {d.scores?.length ? (
        <table class="scores">
          <thead><tr><th>Intent</th><th>Score</th><th>Why</th></tr></thead>
          <tbody>
            {d.scores.map((s, i) => (
              <tr key={i} class={s.kind === d.intent ? "chosen" : undefined}>
                <td>{s.kind}{s.kind === d.intent ? " (chosen)" : ""}</td>
                <td class="num">{s.value.toFixed(3)}</td>
                <td>{s.why}</td>
              </tr>
            ))}
          </tbody>
        </table>
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
  const [rows, setRows] = useState<Decision[]>([]);
  const [state, setState] = useState<FeedState>("connecting");
  const [gap, setGap] = useState(false);
  const [filt, setFilt] = useState<DecisionFilterState>(() => parseQuery(location.search, DEFAULTS));
  const enums = useEnums();
  const { data: snap } = usePoll<Snapshot>("/api/overview", 10000);
  const feed = useRef<Feed | null>(null);

  useEffect(() => {
    const f = new Feed({
      base: "/api/decisions",
      filter: {},
      open: (u) => new EventSource(u) as unknown as EventSourceLike,
      onDecision: (d) => setRows((r) => appendDecision(r, d, CAP)),
      onGap: () => setGap(true),
      onReset: () => {
        setRows([]);
        setGap(false);
      },
      onState: setState,
    });
    feed.current = f;
    f.start();
    return () => f.stop();
  }, []);

  useEffect(() => {
    feed.current?.setFilter(parseFilter(filt));
    writeQuery(buildQuery({ ...filt }, DEFAULTS));
  }, [filt]);

  return (
    <>
      <h1>Decisions</h1>
      <Blurb text={VIEW_BLURB.decisions!} />
      <form class="filters" aria-label="Filter decisions" onSubmit={(e) => e.preventDefault()}>
        <DecisionFilterBar f={filt} enums={enums} profiles={snap?.population.by_profile ?? {}} onChange={(k, v) => setFilt((f) => ({ ...f, [k]: v }))} />
        <span class={`tag ${state === "live" ? "ok" : "warn"}`} role="status">{state}</span>
      </form>
      {gap && <p class="banner warn">Some decisions were dropped before this connection could replay them.</p>}
      <p class="muted">Newest first, last {CAP} shown.</p>
      <ul class="feed">
        {rows.map((d) => (
          <li key={d.id}>
            <span class="when">{simTime(d.at)}</span>
            <span class="kind">{d.kind}</span>
            <Link to={`/actors/${d.actor}`}>actor {d.actor}</Link>
            {d.profile && <span class="muted"> {d.profile}</span>}
            {d.intent && <strong> {d.intent}</strong>}
            <Why d={d} />
          </li>
        ))}
        {rows.length === 0 && <li class="empty">Waiting for decisions…</li>}
      </ul>
    </>
  );
}
