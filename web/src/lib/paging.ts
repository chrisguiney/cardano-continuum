/** Server-side paging state for the actors table. Only one page is ever fetched. */

export const SORT_KEYS = ["id", "name", "role", "profile", "state", "intents", "txs", "retries", "confirmed", "released", "kind", "balance", "tokens"] as const;
export type SortKey = (typeof SORT_KEYS)[number];
export type FilterName = "profile" | "state" | "kind" | "delegated";

export interface PageState {
  offset: number;
  limit: number;
  sort: SortKey;
  desc: boolean;
  profile: string;
  state: string;
  kind: string;
  /** "" (any), "true" or "false". */
  delegated: string;
}

export function initialPage(): PageState {
  return { offset: 0, limit: 100, sort: "id", desc: false, profile: "", state: "", kind: "", delegated: "" };
}

/** toQuery encodes the state as the /api/actors query string, omitting unset fields. */
export function toQuery(s: PageState): string {
  const q = new URLSearchParams({ limit: String(s.limit), offset: String(s.offset) });
  if (s.sort !== "id" || s.desc) q.set("sort", s.sort);
  if (s.desc) q.set("dir", "desc");
  for (const f of ["profile", "state", "kind", "delegated"] as const) if (s[f]) q.set(f, s[f]);
  return q.toString();
}

export function setSort(s: PageState, key: SortKey): PageState {
  return { ...s, sort: key, desc: s.sort === key ? !s.desc : false, offset: 0 };
}

export function setFilter(s: PageState, name: FilterName, value: string): PageState {
  return { ...s, [name]: value, offset: 0 };
}

export function prevPage(s: PageState): PageState {
  return { ...s, offset: Math.max(0, s.offset - s.limit) };
}

export function nextPage(s: PageState, total: number): PageState {
  const next = s.offset + s.limit;
  return next < total ? { ...s, offset: next } : s;
}

/** clampOffset pulls the offset back onto the last page after a filter shrinks the total. */
export function clampOffset(s: PageState, total: number): PageState {
  const last = total === 0 ? 0 : Math.floor((total - 1) / s.limit) * s.limit;
  return s.offset > last ? { ...s, offset: last } : s;
}

export function pageInfo(s: PageState, total: number): { from: number; to: number; page: number; pages: number } {
  const pages = Math.max(1, Math.ceil(total / s.limit));
  if (total === 0) return { from: 0, to: 0, page: 1, pages };
  return { from: s.offset + 1, to: Math.min(total, s.offset + s.limit), page: Math.floor(s.offset / s.limit) + 1, pages };
}

/** PAGE_SIZES are the rows-per-page choices; the server caps a page at 500. */
export const PAGE_SIZES = [25, 50, 100, 250, 500] as const;

const reLabel = /^[a-z0-9_-]{1,64}$/;

/** fromSearch restores a page state from a query string, ignoring values the server would refuse. */
export function fromSearch(search: string): PageState {
  const q = new URLSearchParams(search);
  const s = initialPage();
  const sort = q.get("sort");
  if (sort && (SORT_KEYS as readonly string[]).includes(sort)) s.sort = sort as SortKey;
  s.desc = q.get("dir") === "desc";
  const limit = Number(q.get("limit"));
  if ((PAGE_SIZES as readonly number[]).includes(limit)) s.limit = limit;
  const offset = Number(q.get("offset"));
  if (Number.isInteger(offset) && offset > 0) s.offset = offset;
  for (const f of ["profile", "state", "kind"] as const) {
    const v = q.get(f) ?? "";
    if (reLabel.test(v)) s[f] = v;
  }
  const d = q.get("delegated");
  if (d === "true" || d === "false") s.delegated = d;
  return s;
}

export function setLimit(s: PageState, limit: number): PageState {
  return { ...s, limit, offset: 0 };
}

export function setOrder(s: PageState, sort: SortKey, desc: boolean): PageState {
  return { ...s, sort, desc, offset: 0 };
}
