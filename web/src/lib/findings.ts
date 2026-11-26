import type { Finding } from "./types";

export interface Badge {
  text: string;
  title: string;
  tone: "warn";
}

// reproBadge flags a finding whose node ran a dirty build: the image held
// uncommitted source, so its replay command cannot rebuild what ran.
export function reproBadge(f: Pick<Finding, "reproducible">): Badge | null {
  if (f.reproducible) return null;
  return {
    text: "not reproducible",
    title: "A node ran a build with uncommitted source; its commit does not reproduce the image.",
    tone: "warn",
  };
}

export interface KnownGroup {
  ref: string;
  rows: Finding[];
}

// groupKnown collects the findings an upstream issue explains by issue,
// largest group first, so they can sit collapsed beside what is new.
export function groupKnown(rows: Finding[]): KnownGroup[] {
  const by = new Map<string, Finding[]>();
  for (const f of rows) if (f.known) by.set(f.known, [...(by.get(f.known) ?? []), f]);
  return [...by].map(([ref, rs]) => ({ ref, rows: rs })).sort((a, b) => b.rows.length - a.rows.length || a.ref.localeCompare(b.ref));
}

// knownSummary is the one-line count of known findings by issue.
export function knownSummary(counts: Record<string, number> | undefined): string {
  return Object.entries(counts ?? {})
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
    .map(([ref, n]) => `${ref}: ${n}`)
    .join(", ");
}

export interface FindingFilters {
  /** "false" new only, "true" known only, "" both. */
  known: string;
  kind: string;
  severity: string;
}

/** findingsPath is the API request for the selected filters. */
export function findingsPath(f: FindingFilters): string {
  const q = new URLSearchParams({ limit: "100" });
  for (const k of ["known", "kind", "severity"] as const) if (f[k]) q.set(k, f[k]);
  return `/api/findings?${q.toString()}`;
}
