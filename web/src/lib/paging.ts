/** Server-side paging state for the actors table. Only one page is ever fetched. */

export const SORT_KEYS = ["id", "name", "role", "profile", "state", "intents", "txs", "retries", "confirmed", "released"] as const;
export type SortKey = (typeof SORT_KEYS)[number];
export type FilterName = "profile" | "state" | "role";

export interface PageState {
  offset: number;
  limit: number;
  sort: SortKey;
  desc: boolean;
  profile: string;
  state: string;
  role: string;
}

export function initialPage(): PageState {
  return { offset: 0, limit: 100, sort: "id", desc: false, profile: "", state: "", role: "" };
}

/** toQuery encodes the state as the /api/actors query string, omitting unset fields. */
export function toQuery(s: PageState): string {
  const q = new URLSearchParams({ limit: String(s.limit), offset: String(s.offset) });
  if (s.sort !== "id" || s.desc) q.set("sort", s.sort);
  if (s.desc) q.set("dir", "desc");
  for (const f of ["profile", "state", "role"] as const) if (s[f]) q.set(f, s[f]);
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
