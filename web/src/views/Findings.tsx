import { useState } from "preact/hooks";
import { groupKnown, knownSummary, reproBadge } from "../lib/findings";
import { count } from "../lib/format";
import { usePoll } from "../lib/hooks";
import type { Finding, FindingPage } from "../lib/types";
import { ErrorNote } from "./common";

function badgeOf(f: Finding) {
  const b = reproBadge(f);
  return b ? <span class={`tag ${b.tone}`} title={b.title}>{b.text}</span> : "yes";
}

function Row({ f }: { f: Finding }) {
  return (
    <tr>
      <td>{f.id}</td>
      <td>{f.summary}</td>
      <td>{f.verdicts.map((v) => `${v.impl}: ${v.verdict}`).join("; ")}</td>
      <td>{f.provenance ?? ""}</td>
      <td>{badgeOf(f)}</td>
      <td>{f.replay_command ? <code>{f.replay_command}</code> : ""}</td>
    </tr>
  );
}

function Head() {
  return <tr><th>Id</th><th>Summary</th><th>Verdicts</th><th>Build</th><th>Reproducible</th><th>Replay</th></tr>;
}

// Known findings are explained by an upstream issue. They stay recorded and
// are listed on request, grouped by issue, so new findings are not buried.
export function Findings() {
  const [showKnown, setShowKnown] = useState(false);
  const fresh = usePoll<FindingPage>("/api/findings?limit=100&known=false", 10000);
  const known = usePoll<FindingPage>(showKnown ? "/api/findings?limit=100&known=true" : "/api/findings?limit=1&known=true", 10000);
  const data = fresh.data;
  const knownTotal = data?.known_total ?? 0;
  return (
    <>
      <h1>Findings</h1>
      <ErrorNote error={fresh.error} />
      <div class="table-wrap">
        <table>
          <thead><Head /></thead>
          <tbody>
            {data?.rows.map((f) => <Row key={f.id} f={f} />)}
            {data && data.rows.length === 0 && (
              <tr><td colSpan={6} class="empty">No new findings. Oracle disagreements will appear here.</td></tr>
            )}
          </tbody>
        </table>
      </div>
      {data && <p class="muted">{count(data.total)} new findings</p>}
      {knownTotal > 0 && (
        <section class="card">
          <h2>Known issues</h2>
          <p class="muted">
            {count(knownTotal)} findings are explained by an upstream issue ({knownSummary(data?.known_counts)}).{" "}
            <button type="button" onClick={() => setShowKnown(!showKnown)}>{showKnown ? "Hide" : "Show"}</button>
          </p>
          {showKnown && groupKnown(known.data?.rows ?? []).map((g) => (
            <details key={g.ref}>
              <summary>{g.ref} ({count(data?.known_counts?.[g.ref] ?? g.rows.length)} findings)</summary>
              <div class="table-wrap">
                <table>
                  <thead><Head /></thead>
                  <tbody>{g.rows.map((f) => <Row key={f.id} f={f} />)}</tbody>
                </table>
              </div>
            </details>
          ))}
        </section>
      )}
    </>
  );
}
