import { detailSections } from "../lib/actordetail";
import { count, simTime } from "../lib/format";
import { usePoll } from "../lib/hooks";
import type { ActorDetail } from "../lib/types";
import { VIEW_BLURB } from "../lib/help";
import { CopyButton, ErrorNote, Link } from "./common";
import { Blurb } from "./fields";

export function Actor({ id }: { id: number }) {
  const { data: d, error } = usePoll<ActorDetail>(`/api/actors/${id}`, 3000);
  return (
    <>
      <p><Link to="/actors">← Actors</Link></p>
      <h1>Actor {id}{d?.name ? ` · ${d.name}` : ""}</h1>
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
      <div class="grid2">
        {sections.filter((s) => s.id !== "findings").map((s) => (
          <section class="card" key={s.id} aria-labelledby={`h-${s.id}`}>
            <h2 id={`h-${s.id}`}>{s.title}</h2>
            {s.note && <p class="muted">{s.note}</p>}
            <dl class="traits">
              {s.rows.map((r) => (
                <>
                  <dt>{r.label}</dt>
                  <dd class={r.copy ? "mono wrap" : undefined}>
                    {r.value}
                    {r.copy && r.value !== "none" && <> <CopyButton value={r.value} label={r.label} /></>}
                  </dd>
                </>
              ))}
            </dl>
            {s.id === "holdings" && <Tokens d={d} />}
          </section>
        ))}
      </div>
      <section class="card" aria-labelledby="h-findings">
        <h2 id="h-findings">Findings</h2>
        {d.findings.length === 0 ? (
          <p class="muted">No findings involve this actor's transactions.</p>
        ) : (
          <ul class="findings">
            {d.findings.map((f) => (
              <li key={f.id} class={f.severity === "fatal" ? "bad" : undefined}>
                <span class={`tag ${f.severity === "fatal" ? "bad" : "warn"}`}>{f.severity}</span> <strong>{f.kind}</strong> {f.summary}
              </li>
            ))}
          </ul>
        )}
      </section>
      <section class="card" aria-labelledby="h-recent">
        <h2 id="h-recent">Recent decisions</h2>
        {d.activity.recent.length === 0 && <p class="muted">No intents chosen yet.</p>}
        {d.activity.recent.map((r) => (
          <details key={r.id}>
            <summary><span class="when">{simTime(r.at)}</span> chose <strong>{r.intent}</strong></summary>
            <table class="scores">
              <tbody>
                {(r.scores ?? []).map((s, i) => (
                  <tr key={i} class={s.kind === r.intent ? "chosen" : undefined}>
                    <td>{s.kind}{s.target ? ` → ${s.target}` : ""}</td><td class="num">{s.value.toFixed(3)}</td><td class="muted">{s.why}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </details>
        ))}
      </section>
      <h2>Timeline</h2>
      <ol class="timeline">
        {d.timeline.map((e) => (
          <li key={e.seq}>
            <span class="when">{simTime(e.at)}</span>
            <span class="kind">{e.kind}</span>
            <span>{e.detail}</span>
            {e.intent !== undefined && <span class="muted"> intent {e.intent}</span>}
          </li>
        ))}
        {d.timeline.length === 0 && <li class="empty">No events yet.</li>}
      </ol>
    </>
  );
}

function Tokens({ d }: { d: ActorDetail }) {
  const ts = d.holdings.tokens;
  return (
    <div class="table-wrap tokens" tabIndex={0} role="region" aria-label="Native tokens, scrolls sideways">
      <table aria-label="Native tokens">
        <thead><tr><th>Policy</th><th>Asset</th><th class="num">Quantity</th></tr></thead>
        <tbody>
          {ts.map((t) => (
            <tr key={t.policy + t.name}>
              <td class="mono">{t.policy.slice(0, 8)}… <CopyButton value={t.policy} label="policy id" /></td>
              <td>{t.name_text || <span class="mono">{t.name || "(empty)"}</span>}</td>
              <td class="num">{count(t.quantity)}</td>
            </tr>
          ))}
          {ts.length === 0 && <tr><td colSpan={3} class="empty">No native tokens.</td></tr>}
        </tbody>
      </table>
    </div>
  );
}
