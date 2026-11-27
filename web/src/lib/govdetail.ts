import type { GovParam } from "./types";

/** govPath is the detail page of a proposal id ("txhash#index"), or null when the id is malformed. */
export function govPath(id: string): string | null {
  const m = /^([0-9a-f]{64})#(\d{1,9})$/.exec(id);
  return m ? `/governance/${m[1]}/${m[2]}` : null;
}

/** govApi is the API path of a detail page. */
export const govApi = (tx: string, index: number) => `/api/governance/${tx}/${index}`;

const GROUPS = ["network", "economic", "technical", "governance"];

/** groupParams splits parameter changes by group in the ledger's order; a parameter outside the groups comes last. */
export function groupParams(ps: GovParam[]): [string, GovParam[]][] {
  const out: [string, GovParam[]][] = [];
  for (const g of [...GROUPS, ""]) {
    const rows = ps.filter((p) => p.group === g);
    if (rows.length) out.push([g || "other", rows]);
  }
  return out;
}

/** securityNames lists the security-group parameters a change touches; stake pools vote on the action when there are any. */
export const securityNames = (ps: GovParam[]) => ps.filter((p) => p.security).map((p) => p.name);

/** voterLink is where a vote's voter is shown: its continuum actor, else nothing. */
export const voterLink = (actor?: number) => (actor === undefined ? null : `/actors/${actor}`);
