import { useEffect, useState } from "react";
import { findingsPath, groupKnown, knownSummary, reproBadge, type FindingFilters } from "../lib/findings";
import { VIEW_BLURB } from "../lib/help";
import { useEnums } from "../lib/meta";
import { buildQuery, parseQuery, writeQuery } from "../lib/query";
import { count } from "../lib/format";
import { usePoll } from "../lib/hooks";
import type { Finding, FindingPage } from "../lib/types";
import { ErrorNote } from "./common";
import { Blurb } from "./fields";
import { FindingFilterBar } from "./filterbars";

function badgeOf(f: Finding) {
  const b = reproBadge(f);
  return b ? <span className={`tag ${b.tone}`} title={b.title}>{b.text}</span> : "yes";
}

function Row({ f }: { f: Finding }) {
  return (
    <tr>
      <td>{f.id}</td>
      <td><span className={`tag ${f.severity === "fatal" ? "bad" : f.severity === "error" ? "warn" : ""}`}>{f.severity}</span></td>
      <td>{f.kind}</td>
      <td>{f.summary}</td>
      <td>{f.verdicts.map((v) => `${v.impl}: ${v.verdict}`).join("; ")}</td>
      <td>{f.provenance ?? ""}</td>
      <td>{badgeOf(f)}</td>
      <td>{f.replay_command ? <code>{f.replay_command}</code> : ""}</td>
    </tr>
  );
}

function Head() {
  return <tr><th>Id</th><th>Severity</th><th>Kind</th><th>Summary</th><th>Verdicts</th><th>Build</th><th>Reproducible</th><th>Replay</th></tr>;
}

const FILTER_DEFAULTS: FindingFilters = { known: "false", kind: "", severity: "" };

// Known findings are explained by an upstream issue. They stay recorded and
// are listed on request, grouped by issue, so new findings are not buried.
export function Findings() {
  const [showKnown, setShowKnown] = useState(false);
  const [filt, setFilt] = useState<FindingFilters>(() => parseQuery(location.search, FILTER_DEFAULTS));
  const enums = useEnums();
  useEffect(() => writeQuery(buildQuery({ ...filt }, FILTER_DEFAULTS)), [filt]);
  const fresh = usePoll<FindingPage>(findingsPath(filt), 10000);
  const known = usePoll<FindingPage>(showKnown ? "/api/findings?limit=100&known=true" : "/api/findings?limit=1&known=true", 10000);
  const data = fresh.data;
  const knownTotal = data?.known_total ?? 0;
  return (
    <>
      <h1>Findings</h1>
      <Blurb text={VIEW_BLURB.findings!} />
      <form className="filters" aria-label="Filter findings" onSubmit={(e) => e.preventDefault()}>
        <FindingFilterBar f={filt} enums={enums} onChange={(k, v) => setFilt((f) => ({ ...f, [k]: v }))} />
      </form>
      <ErrorNote error={fresh.error} />
      <div className="table-wrap">
        <table>
          <thead><Head /></thead>
          <tbody>
            {data?.rows.map((f) => <Row key={f.id} f={f} />)}
            {data && data.rows.length === 0 && (
              <tr><td colSpan={8} className="empty">No findings match. Oracle disagreements will appear here.</td></tr>
            )}
          </tbody>
        </table>
      </div>
      {data && <p className="muted" role="status">{count(data.total)} findings shown</p>}
      {knownTotal > 0 && filt.known === "false" && (
        <section className="card">
          <h2>Known issues</h2>
          <p className="muted">
            {count(knownTotal)} findings are explained by an upstream issue ({knownSummary(data?.known_counts)}).{" "}
            <button type="button" onClick={() => setShowKnown(!showKnown)}>{showKnown ? "Hide" : "Show"}</button>
          </p>
          {showKnown && groupKnown(known.data?.rows ?? []).map((g) => (
            <details key={g.ref}>
              <summary>{g.ref} ({count(data?.known_counts?.[g.ref] ?? g.rows.length)} findings)</summary>
              <div className="table-wrap">
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
