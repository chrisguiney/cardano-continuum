import { ada, count } from "./format";
import type { SortKey } from "./paging";
import type { ActorRow } from "./types";

export interface Column {
  label: string;
  /** sort is the server's allowlisted sort key, absent when the column cannot be sorted. */
  sort?: SortKey;
  num?: boolean;
  cell: (r: ActorRow) => string;
}

export function delegationLabel(r: ActorRow): string {
  return r.delegated ? r.pool || "pool" : "none";
}

/** ownerLabel names a wallet's human and what the wallet is for, empty without one. */
export function ownerLabel(r: ActorRow): string {
  return r.owner ? `human ${r.owner.human}, ${r.owner.purpose}` : "";
}

export const ACTOR_COLUMNS: Column[] = [
  { label: "ID", sort: "id", cell: (r) => String(r.id) },
  { label: "Name", sort: "name", cell: (r) => r.name },
  { label: "Kind", sort: "kind", cell: (r) => r.kind },
  { label: "Profile", sort: "profile", cell: (r) => r.profile },
  { label: "State", sort: "state", cell: (r) => r.state },
  { label: "Address", cell: (r) => r.address },
  { label: "Balance", sort: "balance", num: true, cell: (r) => ada(r.balance) },
  { label: "Pending", num: true, cell: (r) => ada(r.pending) },
  { label: "Tokens", sort: "tokens", num: true, cell: (r) => count(r.tokens) },
  { label: "Delegation", cell: delegationLabel },
  { label: "Owner", cell: ownerLabel },
  { label: "Intents", sort: "intents", num: true, cell: (r) => count(r.intents) },
  { label: "Txs", sort: "txs", num: true, cell: (r) => count(r.txs) },
  { label: "Confirmed", sort: "confirmed", num: true, cell: (r) => count(r.confirmed) },
];
