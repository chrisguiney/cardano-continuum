import { useEffect, useState } from "preact/hooks";
import { count } from "../lib/format";
import { usePoll } from "../lib/hooks";
import { SORT_KEYS, clampOffset, initialPage, nextPage, pageInfo, prevPage, setFilter, setSort, toQuery, type FilterName, type PageState, type SortKey } from "../lib/paging";
import type { ActorPage, Snapshot } from "../lib/types";
import { ErrorNote, Link } from "./common";

const COLUMNS: [SortKey, string][] = SORT_KEYS.map((k) => [k, k[0]!.toUpperCase() + k.slice(1)]);

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
          Role
          <input type="text" value={page.role} placeholder="any" maxLength={64} onInput={(e) => change("role", e.currentTarget.value.trim().toLowerCase())} />
        </label>
        <span class="muted">{count(total)} matching</span>
      </div>
      <ErrorNote error={error} />
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              {COLUMNS.map(([k, label]) => (
                <th key={k} aria-sort={dirOf(k)}>
                  <button type="button" class="th" onClick={() => setPage((p) => setSort(p, k))}>
                    {label}
                    <span aria-hidden="true">{page.sort === k ? (page.desc ? " ▼" : " ▲") : ""}</span>
                  </button>
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {data?.rows.map((r) => (
              <tr key={r.id}>
                <td><Link to={`/actors/${r.id}`}>{r.id}</Link></td>
                <td>{r.name || <span class="muted">unnamed</span>}</td>
                <td>{r.role}</td>
                <td>{r.profile}</td>
                <td>{r.state}</td>
                <td class="num">{count(r.intents)}</td>
                <td class="num">{count(r.txs)}</td>
                <td class="num">{count(r.retries)}</td>
                <td class="num">{count(r.confirmed)}</td>
                <td class="num">{count(r.released)}</td>
              </tr>
            ))}
            {data && data.rows.length === 0 && (
              <tr><td colSpan={10} class="empty">No actors match.</td></tr>
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
