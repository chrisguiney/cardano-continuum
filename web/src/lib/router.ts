export type Route =
  | { name: "overview" | "actors" | "pools" | "nodes" | "governance" | "markets" | "decisions" | "findings" | "adversaries" | "controls" | "help" | "explorer" | "contracts" | "notfound" }
  | { name: "pool" | "node"; key: string }
  | { name: "actor"; id: number }
  | { name: "people"; id?: number }
  | { name: "contract"; key: string; instance?: number }
  | { name: "govaction"; tx: string; index: number }
  | { name: "xblock" | "xtx" | "xaddr" | "xpool"; key: string }
  | { name: "xepoch"; n: number };

const simple = new Set(["actors", "pools", "nodes", "governance", "markets", "decisions", "findings", "adversaries", "controls", "help", "explorer", "contracts"]);

const reHash = /^[0-9a-fA-F]{64}$/;
const reKey = /^(?:[0-9a-fA-F]{64}|\d{1,18})$/;
const reAddr = /^[a-z0-9_]{8,200}$/;
const rePool = /^[0-9a-f]{56}$/;
// reNode is a Kubernetes object name, which a node's name is.
const reNode = /^[a-z0-9](?:[-a-z0-9.]{0,251}[a-z0-9])?$/;
const reValidator = /^[a-z][a-z0-9_]{0,63}$/;

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
  if (parts.length === 1 && head === "people") return { name: "people" };
  if (parts.length === 2 && head === "people" && arg) return /^\d{1,18}$/.test(arg) ? { name: "people", id: Number(arg) } : { name: "notfound" };
  if (parts.length === 2 && head === "pools" && arg) return rePool.test(arg) ? { name: "pool", key: arg } : { name: "notfound" };
  if (parts.length === 2 && head === "nodes" && arg) return reNode.test(arg) ? { name: "node", key: arg } : { name: "notfound" };
  if (head === "contracts" && (parts.length === 2 || parts.length === 3) && arg) return contractRoute(arg, key);
  if (head === "governance" && parts.length === 3 && arg && key) return govRoute(arg, key);
  if (head === "explorer" && parts.length === 3 && arg && key) return explorerRoute(arg, key);
  return { name: "notfound" };
}

function contractRoute(name: string, instance?: string): Route {
  if (!reValidator.test(name)) return { name: "notfound" };
  if (instance === undefined) return { name: "contract", key: name };
  return /^\d{1,18}$/.test(instance) ? { name: "contract", key: name, instance: Number(instance) } : { name: "notfound" };
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

/**
 * redirectFor is where an old link now lives, or null. /pools used to list
 * the nodes, filtered by impl, tier and phase; it now lists stake pools,
 * so a link carrying those filters goes to /nodes.
 */
export function redirectFor(path: string, search: string): string | null {
  if (path !== "/pools") return null;
  const q = new URLSearchParams(search);
  return ["impl", "tier", "phase"].some((k) => q.has(k)) ? `/nodes${search}` : null;
}

/** navigate changes the client route without a reload and tells listeners. */
export function navigate(path: string): void {
  if (path === location.pathname) return;
  history.pushState(null, "", path);
  dispatchEvent(new PopStateEvent("popstate"));
}
