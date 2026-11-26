export type Route =
  | { name: "overview" | "actors" | "pools" | "decisions" | "findings" | "controls" | "notfound" }
  | { name: "actor"; id: number };

const simple = new Set(["actors", "pools", "decisions", "findings", "controls"]);

export function parseRoute(path: string): Route {
  const parts = path.split("/").filter(Boolean);
  const [head, arg] = parts;
  if (parts.length === 0) return { name: "overview" };
  if (parts.length === 1 && head && simple.has(head)) return { name: head as "actors" };
  if (parts.length === 2 && head === "actors" && arg && /^\d+$/.test(arg)) return { name: "actor", id: Number(arg) };
  return { name: "notfound" };
}

/** navigate changes the client route without a reload and tells listeners. */
export function navigate(path: string): void {
  if (path === location.pathname) return;
  history.pushState(null, "", path);
  dispatchEvent(new PopStateEvent("popstate"));
}
