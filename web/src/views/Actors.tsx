import { useEffect, useState } from "react";
import { VIEW_BLURB } from "../lib/help";
import { useEnums } from "../lib/meta";
import { writeQuery } from "../lib/query";
import { ACTOR_COLUMNS } from "../lib/actorcols";
import { count } from "../lib/format";
import { usePoll } from "../lib/hooks";
import { clampOffset, fromSearch, nextPage, pageInfo, prevPage, setFilter, setLimit, setOrder, setSort, toQuery, type FilterName, type PageState, type SortKey } from "../lib/paging";
import type { ActorPage, Snapshot } from "../lib/types";
import { ErrorNote, Link } from "./common";
import { Blurb } from "./fields";
import { ActorFilters } from "./filterbars";

export function Actors() {
  const [page, setPage] = useState<PageState>(() => fromSearch(location.search));
  const { data, error } = usePoll<ActorPage>(`/api/actors?${toQuery(page)}`, 5000);
  const { data: snap } = usePoll<Snapshot>("/api/overview", 10000);
  const enums = useEnums();
  const total = data?.total ?? 0;

  useEffect(() => writeQuery(toQuery(page)), [page]);

  useEffect(() => {
    if (data) setPage((p) => clampOffset(p, data.total));
  }, [data?.total]);

  const info = pageInfo(page, total);
  const change = (n: FilterName, v: string) => setPage((p) => setFilter(p, n, v));
  const dirOf = (k: SortKey) => (page.sort !== k ? "none" : page.desc ? "descending" : "ascending");

  return (
    <>
      <h1>Actors</h1>
      <Blurb text={VIEW_BLURB.actors!} />
      <form className="filters" aria-label="Filter actors" onSubmit={(e) => e.preventDefault()}>
        <ActorFilters
          page={page}
          enums={enums}
          profiles={snap?.population.by_profile ?? {}}
          states={snap?.population.by_state ?? {}}
          onFilter={change}
          onLimit={(n) => setPage((p) => setLimit(p, n))}
          onSort={(k, d) => setPage((p) => setOrder(p, k, d))}
        />
        <span className="muted" role="status">{count(total)} matching</span>
      </form>
      <ErrorNote error={error} />
      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              {ACTOR_COLUMNS.map((c) => (
                <th key={c.label} aria-sort={c.sort ? dirOf(c.sort) : undefined}>
                  {c.sort ? (
                    <button type="button" className="th" onClick={() => setPage((p) => setSort(p, c.sort!))}>
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
                  <td key={c.label} className={c.num ? "num" : c.label === "Address" ? "mono" : undefined}>
                    {c.label === "ID" ? <Link to={`/actors/${r.id}`}>{r.id}</Link> : c.cell(r) || <span className="muted">{c.label === "Name" ? "unnamed" : "-"}</span>}
                  </td>
                ))}
              </tr>
            ))}
            {data && data.rows.length === 0 && (
              <tr><td colSpan={ACTOR_COLUMNS.length} className="empty">No actors match.</td></tr>
            )}
          </tbody>
        </table>
      </div>
      <div className="pager">
        <button type="button" disabled={page.offset === 0} onClick={() => setPage(prevPage)}>Previous</button>
        <span>
          {count(info.from)}–{count(info.to)} of {count(total)} (page {count(info.page)} of {count(info.pages)})
        </span>
        <button type="button" disabled={page.offset + page.limit >= total} onClick={() => setPage((p) => nextPage(p, total))}>Next</button>
      </div>
    </>
  );
}
