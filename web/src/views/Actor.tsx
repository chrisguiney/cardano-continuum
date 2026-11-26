import { count, simTime } from "../lib/format";
import { usePoll } from "../lib/hooks";
import type { Timeline } from "../lib/types";
import { ErrorNote, Link } from "./common";

export function Actor({ id }: { id: number }) {
  const { data: t, error } = usePoll<Timeline>(`/api/actors/${id}/timeline`, 3000);
  return (
    <>
      <p><Link to="/actors">← Actors</Link></p>
      <h1>Actor {id}</h1>
      <ErrorNote error={error} />
      {t && (
        <>
          <dl class="traits">
            <dt>Profile</dt><dd>{t.profile}</dd>
            <dt>Role</dt><dd>{t.role || "unknown"}</dd>
            <dt>State</dt><dd>{t.state}</dd>
            <dt>Born</dt><dd>{simTime(t.born_ms)}</dd>
          </dl>
          {t.dropped > 0 && <p class="muted">{count(t.dropped)} older entries are no longer held by the server.</p>}
          <ol class="timeline">
            {t.entries.map((e) => (
              <li key={e.seq}>
                <span class="when">{simTime(e.at)}</span>
                <span class="kind">{e.kind}</span>
                <span>{e.detail}</span>
                {e.intent !== undefined && <span class="muted"> intent {e.intent}</span>}
              </li>
            ))}
            {t.entries.length === 0 && <li class="empty">No events yet.</li>}
          </ol>
        </>
      )}
    </>
  );
}
