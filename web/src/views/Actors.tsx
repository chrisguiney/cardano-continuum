import { useEffect, useState } from "preact/hooks";
import { ACTOR_COLUMNS } from "../lib/actorcols";
import { count } from "../lib/format";
import { usePoll } from "../lib/hooks";
import { clampOffset, initialPage, nextPage, pageInfo, prevPage, setFilter, setSort, toQuery, type FilterName, type PageState, type SortKey } from "../lib/paging";
import type { ActorPage, Snapshot } from "../lib/types";
import { ErrorNote, Link } from "./common";

function Filter({ name, label, value, options, onChange }: { name: FilterName; label: string; value: string; options: string[]; onChange: (n: FilterName, v: string) => void }) {
  return (
    <label>
      {label}
      <select value={value} onChange={(e) => onChange(name, e.currentTarget.value)}>
        <option value="">All</option>
        {options.map((o) => (
          <option key={o} value={o}>{o}</option>
        ))}
      </select>
    </label>
  );
}

export function Actors() {
  const [page, setPage] = useState<PageState>(initialPage);
  const { data, error } = usePoll<ActorPage>(`/api/actors?${toQuery(page)}`, 5000);
  const { data: snap } = usePoll<Snapshot>("/api/overview", 10000);
  const total = data?.total ?? 0;

  useEffect(() => {
    if (data) setPage((p) => clampOffset(p, data.total));
  }, [data?.total]);

  const info = pageInfo(page, total);
  const change = (n: FilterName, v: string) => setPage((p) => setFilter(p, n, v));
  const dirOf = (k: SortKey) => (page.sort !== k ? "none" : page.desc ? "descending" : "ascending");

  return (
    <>
      <h1>Actors</h1>
      <div class="filters">
        <Filter name="profile" label="Profile" value={page.profile} options={Object.keys(snap?.population.by_profile ?? {}).sort()} onChange={change} />
        <Filter name="state" label="State" value={page.state} options={Object.keys(snap?.population.by_state ?? {}).sort()} onChange={change} />
        <label>
          Delegated
          <select value={page.delegated} onChange={(e) => change("delegated", e.currentTarget.value)}>
            <option value="">Any</option>
            <option value="true">Delegated</option>
            <option value="false">Not delegated</option>
          </select>
        </label>
        <span class="muted">{count(total)} matching</span>
      </div>
      <ErrorNote error={error} />
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              {ACTOR_COLUMNS.map((c) => (
                <th key={c.label} aria-sort={c.sort ? dirOf(c.sort) : undefined}>
                  {c.sort ? (
                    <button type="button" class="th" onClick={() => setPage((p) => setSort(p, c.sort!))}>
                      {c.label}
                      <span aria-hidden="true">{page.sort === c.sort ? (page.desc ? " ▼" : " ▲") : ""}</span>
                    </button>
                  ) : (
                    c.label
                  )}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {data?.rows.map((r) => (
              <tr key={r.id}>
                {ACTOR_COLUMNS.map((c) => (
                  <td key={c.label} class={c.num ? "num" : c.label === "Address" ? "mono" : undefined}>
                    {c.label === "ID" ? <Link to={`/actors/${r.id}`}>{r.id}</Link> : c.cell(r) || <span class="muted">{c.label === "Name" ? "unnamed" : "-"}</span>}
                  </td>
                ))}
              </tr>
            ))}
            {data && data.rows.length === 0 && (
              <tr><td colSpan={ACTOR_COLUMNS.length} class="empty">No actors match.</td></tr>
            )}
          </tbody>
        </table>
      </div>
      <div class="pager">
        <button type="button" disabled={page.offset === 0} onClick={() => setPage(prevPage)}>Previous</button>
        <span>
          {count(info.from)}–{count(info.to)} of {count(total)} (page {count(info.page)} of {count(info.pages)})
        </span>
        <button type="button" disabled={page.offset + page.limit >= total} onClick={() => setPage((p) => nextPage(p, total))}>Next</button>
      </div>
    </>
  );
}
