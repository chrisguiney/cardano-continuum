import { getJSON } from "./api";
import type { SearchResult } from "./extypes";

export type SearchOutcome = { ok: true; route: string } | { ok: false; message: string };

/** runSearch asks the server where a key leads. The key is trimmed and bounded here; the server decides what it names. */
export async function runSearch(q: string, get: typeof getJSON = getJSON): Promise<SearchOutcome> {
  const key = q.trim();
  if (key === "") return { ok: false, message: "Enter a block hash or height, transaction hash, address, pool id or actor id." };
  if (key.length > 200) return { ok: false, message: "That is too long to be an id or an address." };
  try {
    const r = await get<SearchResult>(`/api/explorer/search?q=${encodeURIComponent(key)}`);
    return { ok: true, route: r.route };
  } catch (e) {
    return { ok: false, message: e instanceof Error ? e.message : String(e) };
  }
}
