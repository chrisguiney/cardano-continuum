/** Filter state kept in the URL query string, so a view's filters make a shareable link. */

/** parseQuery reads the keys of defaults from search; anything else is ignored. */
export function parseQuery<T extends object>(search: string, defaults: T): T {
  const q = new URLSearchParams(search);
  const out: Record<string, string> = { ...(defaults as Record<string, string>) };
  for (const k of Object.keys(defaults)) {
    const v = q.get(k);
    if (v !== null) out[k] = v;
  }
  return out as T;
}

/** buildQuery encodes state, leaving out values equal to their default. */
export function buildQuery(state: object, defaults: object): string {
  const q = new URLSearchParams();
  const d = defaults as Record<string, string>;
  for (const [k, v] of Object.entries(state) as [string, string][]) if (v !== (d[k] ?? "")) q.set(k, v);
  return q.toString();
}

/** writeQuery replaces the address-bar query without adding a history entry. */
export function writeQuery(qs: string): void {
  try {
    history.replaceState(history.state, "", qs ? `?${qs}` : location.pathname);
  } catch {
    // the link just stops reflecting the filters.
  }
}
