import { useEffect, useState } from "react";
import { VIEW_BLURB } from "../lib/help";
import { useEnums } from "../lib/meta";
import { writeQuery } from "../lib/query";
import { ACTOR_COLUMNS } from "../lib/actorcols";
import { count } from "../lib/format";
import { usePoll } from "../lib/hooks";
import { clampOffset, fromSearch, nextPage, pageInfo, prevPage, setFilter, setLimit, setOrder, setSort, toQuery, type FilterName, type PageState, type SortKey } from "../lib/paging";
import type { ActorPage, Snapshot } from "../lib/types";
import { ArrowDownIcon, ArrowUpIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { EmptyRow, ErrorNote, Filters, Link, Muted, Mono, TableFrame, Title } from "./common";
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
      <Title>Actors</Title>
      <Blurb text={VIEW_BLURB.actors!} />
      <Filters aria-label="Filter actors">
        <ActorFilters
          page={page}
          enums={enums}
          profiles={snap?.population.by_profile ?? {}}
          states={snap?.population.by_state ?? {}}
          onFilter={change}
          onLimit={(n) => setPage((p) => setLimit(p, n))}
          onSort={(k, d) => setPage((p) => setOrder(p, k, d))}
        />
        <Muted className="pb-2" role="status">{count(total)} matching</Muted>
      </Filters>
      <ErrorNote error={error} />
      <TableFrame>
        <Table>
          <TableHeader>
            <TableRow>
              {ACTOR_COLUMNS.map((c) => (
                <TableHead key={c.label} aria-sort={c.sort ? dirOf(c.sort) : undefined} className={c.num ? "text-right" : undefined}>
                  {c.sort ? (
                    <Button type="button" variant="ghost" size="xs" className="-ml-2 font-semibold" onClick={() => setPage((p) => setSort(p, c.sort!))}>
                      {c.label}
                      {page.sort === c.sort && (page.desc ? <ArrowDownIcon aria-hidden="true" /> : <ArrowUpIcon aria-hidden="true" />)}
                    </Button>
                  ) : (
                    c.label
                  )}
                </TableHead>
              ))}
            </TableRow>
          </TableHeader>
          <TableBody>
            {data?.rows.map((r) => (
              <TableRow key={r.id}>
                {ACTOR_COLUMNS.map((c) => (
                  <TableCell key={c.label} className={c.num ? "text-right tabular-nums" : undefined}>
                    {c.label === "ID" ? <Link to={`/actors/${r.id}`}>{r.id}</Link> : c.label === "Address" && c.cell(r) ? <Mono>{c.cell(r)}</Mono> : c.cell(r) || <Muted>{c.label === "Name" ? "unnamed" : "-"}</Muted>}
                  </TableCell>
                ))}
              </TableRow>
            ))}
            {data && data.rows.length === 0 && <EmptyRow cols={ACTOR_COLUMNS.length}>No actors match.</EmptyRow>}
          </TableBody>
        </Table>
      </TableFrame>
      <nav className="my-4 flex items-center justify-center gap-4" aria-label="Pages">
        <Button type="button" variant="outline" disabled={page.offset === 0} onClick={() => setPage(prevPage)}>Previous</Button>
        <span>
          {count(info.from)}–{count(info.to)} of {count(total)} (page {count(info.page)} of {count(info.pages)})
        </span>
        <Button type="button" variant="outline" disabled={page.offset + page.limit >= total} onClick={() => setPage((p) => nextPage(p, total))}>Next</Button>
      </nav>
    </>
  );
}
