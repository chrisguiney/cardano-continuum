export type Route =
  | { name: "overview" | "actors" | "pools" | "governance" | "decisions" | "findings" | "adversaries" | "controls" | "help" | "explorer" | "notfound" }
  | { name: "actor"; id: number }
  | { name: "govaction"; tx: string; index: number }
  | { name: "xblock" | "xtx" | "xaddr" | "xpool"; key: string }
  | { name: "xepoch"; n: number };

const simple = new Set(["actors", "pools", "governance", "decisions", "findings", "adversaries", "controls", "help", "explorer"]);

const reHash = /^[0-9a-fA-F]{64}$/;
const reKey = /^(?:[0-9a-fA-F]{64}|\d{1,18})$/;
const reAddr = /^[a-z0-9_]{8,200}$/;
const rePool = /^[0-9a-f]{56}$/;

// The explorer's routes validate their argument here so a view never
// builds an API path from arbitrary text.
const exRoutes: Record<string, [Route["name"], RegExp]> = {
  blocks: ["xblock", reKey],
  txs: ["xtx", reHash],
  addresses: ["xaddr", reAddr],
  pools: ["xpool", rePool],
};

export function parseRoute(path: string): Route {
  const parts = path.split("/").filter(Boolean);
  const [head, arg, key] = parts;
  if (parts.length === 0) return { name: "overview" };
  if (parts.length === 1 && head && simple.has(head)) return { name: head as "actors" };
  if (parts.length === 2 && head === "actors" && arg && /^\d+$/.test(arg)) return { name: "actor", id: Number(arg) };
  if (head === "governance" && parts.length === 3 && arg && key) return govRoute(arg, key);
  if (head === "explorer" && parts.length === 3 && arg && key) return explorerRoute(arg, key);
  return { name: "notfound" };
}

function govRoute(tx: string, index: string): Route {
  return reHash.test(tx) && /^\d{1,9}$/.test(index) ? { name: "govaction", tx: tx.toLowerCase(), index: Number(index) } : { name: "notfound" };
}

function explorerRoute(kind: string, key: string): Route {
  if (kind === "epochs") return /^\d{1,9}$/.test(key) ? { name: "xepoch", n: Number(key) } : { name: "notfound" };
  const r = exRoutes[kind];
  if (!r || !r[1].test(key)) return { name: "notfound" };
  return { name: r[0] as "xblock", key };
}

/** navigate changes the client route without a reload and tells listeners. */
export function navigate(path: string): void {
  if (path === location.pathname) return;
  history.pushState(null, "", path);
  dispatchEvent(new PopStateEvent("popstate"));
}
