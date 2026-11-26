import { useEffect, useRef, useState } from "preact/hooks";
import { Feed, appendDecision, type EventSourceLike, type FeedFilter, type FeedState } from "../lib/feed";
import { simTime } from "../lib/format";
import type { Decision } from "../lib/types";
import { Link } from "./common";

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

function parseFilter(actor: string, profile: string, kinds: string): FeedFilter {
  const f: FeedFilter = {};
  if (/^\d+$/.test(actor)) f.actor = Number(actor);
  if (/^[a-z0-9_-]{1,64}$/.test(profile)) f.profile = profile;
  const ks = kinds.split(",").map((k) => k.trim()).filter((k) => /^[A-Za-z]{1,32}$/.test(k));
  if (ks.length) f.kinds = ks;
  return f;
}

export function Decisions() {
  const [rows, setRows] = useState<Decision[]>([]);
  const [state, setState] = useState<FeedState>("connecting");
  const [gap, setGap] = useState(false);
  const [actor, setActor] = useState("");
  const [profile, setProfile] = useState("");
  const [kinds, setKinds] = useState("");
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
    feed.current?.setFilter(parseFilter(actor.trim(), profile.trim(), kinds));
  }, [actor, profile, kinds]);

  return (
    <>
      <h1>Decisions</h1>
      <div class="filters">
        <label>Actor id<input type="text" inputMode="numeric" value={actor} onInput={(e) => setActor(e.currentTarget.value)} placeholder="any" /></label>
        <label>Profile<input type="text" value={profile} onInput={(e) => setProfile(e.currentTarget.value)} placeholder="any" /></label>
        <label>Kinds<input type="text" value={kinds} onInput={(e) => setKinds(e.currentTarget.value)} placeholder="IntentChosen, …" /></label>
        <span class={`tag ${state === "live" ? "ok" : "warn"}`} role="status">{state}</span>
      </div>
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
