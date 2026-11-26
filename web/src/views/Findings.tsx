import { count } from "../lib/format";
import { usePoll } from "../lib/hooks";
import type { FindingPage } from "../lib/types";
import { ErrorNote } from "./common";

export function Findings() {
  const { data, error } = usePoll<FindingPage>("/api/findings?limit=100", 10000);
  return (
    <>
      <h1>Findings</h1>
      <ErrorNote error={error} />
      <div class="table-wrap">
        <table>
          <thead>
            <tr><th>Id</th><th>Summary</th><th>Verdicts</th><th>Build</th><th>Reproducible</th><th>Replay</th></tr>
          </thead>
          <tbody>
            {data?.rows.map((f) => (
              <tr key={f.id}>
                <td>{f.id}</td>
                <td>{f.summary}</td>
                <td>{f.verdicts.map((v) => `${v.impl}: ${v.verdict}`).join("; ")}</td>
                <td>{f.provenance ?? ""}</td>
                <td>{f.reproducible ? "yes" : <span class="tag warn">non-reproducible</span>}</td>
                <td>{f.replay_command ? <code>{f.replay_command}</code> : ""}</td>
              </tr>
            ))}
            {data && data.rows.length === 0 && (
              <tr><td colSpan={6} class="empty">No findings. Oracle disagreements will appear here.</td></tr>
            )}
          </tbody>
        </table>
      </div>
      {data && data.total > 0 && <p class="muted">{count(data.total)} findings</p>}
    </>
  );
}
