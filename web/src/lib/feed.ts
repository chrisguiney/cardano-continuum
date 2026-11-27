import type { Decision } from "./types";

export interface FeedFilter {
  actor?: number;
  profile?: string;
  kinds?: string[];
  intent?: string;
  /** vote keeps only the decisions about votes. */
  vote?: boolean;
}

export type FeedState = "connecting" | "live" | "reconnecting" | "stopped";

/** The slice of EventSource the feed uses, so tests can inject a fake. */
export interface EventSourceLike {
  addEventListener(type: string, fn: (e: { data: string; lastEventId: string }) => void): void;
  close(): void;
  onerror: (() => void) | null;
  onopen: (() => void) | null;
}

export function feedUrl(base: string, f: FeedFilter, lastId: number | null): string {
  const q = new URLSearchParams();
  if (f.actor !== undefined) q.set("actor", String(f.actor));
  if (f.profile) q.set("profile", f.profile);
  if (f.intent) q.set("intent", f.intent);
  if (f.kinds?.length) q.set("kind", f.kinds.join(","));
  if (f.vote) q.set("vote", "true");
  if (lastId !== null) q.set("last_event_id", String(lastId));
  const s = q.toString();
  return s ? `${base}?${s}` : base;
}

/** appendDecision puts d first and keeps at most cap entries, newest first. */
export function appendDecision(list: Decision[], d: Decision, cap: number): Decision[] {
  const out = [d, ...list];
  return out.length > cap ? out.slice(0, cap) : out;
}

/** IdleMode is how the decisions feed shows idle decisions. */
export type IdleMode = "group" | "hide" | "show";

/** IdleRun is a run of consecutive idle decisions shown as one row. */
export interface IdleRun {
  type: "idle";
  /** key is the first decision's id, stable while the run grows. */
  key: number;
  count: number;
  /** actors is the number of distinct actors in actorIds. */
  actors: number;
  actorIds: ReadonlySet<number>;
  /** from and to are the simulated times of the first and last decision. */
  from: number;
  to: number;
}

/** FeedRow is one row of the decisions feed. */
export type FeedRow = { type: "decision"; d: Decision } | IdleRun;

export const isIdle = (d: Decision): boolean => d.intent === "idle";

function extend(r: IdleRun, d: Decision): IdleRun {
  const actorIds = r.actorIds.has(d.actor) ? r.actorIds : new Set([...r.actorIds, d.actor]);
  return { ...r, count: r.count + 1, actors: actorIds.size, actorIds, from: Math.min(r.from, d.at), to: Math.max(r.to, d.at) };
}

/**
 * foldDecisions adds a batch (oldest first) to rows (newest first) and keeps
 * at most cap rows. Idle decisions arrive far faster than anything else, so
 * in "group" mode each run of them between two other decisions becomes one
 * counting row; "hide" drops them and "show" keeps each.
 */
export function foldDecisions(rows: FeedRow[], batch: Decision[], cap: number, mode: IdleMode): FeedRow[] {
  const out = rows.slice();
  for (const d of batch) {
    if (!isIdle(d) || mode === "show") {
      out.unshift({ type: "decision", d });
      continue;
    }

    if (mode === "hide") continue;

    const head = out[0];
    if (head?.type === "idle") {
      out[0] = extend(head, d);
      continue;
    }

    out.unshift({ type: "idle", key: d.id, count: 1, actors: 1, actorIds: new Set([d.actor]), from: d.at, to: d.at });
  }

  return out.length > cap ? out.slice(0, cap) : out;
}

export interface FeedOptions {
  base: string;
  filter: FeedFilter;
  open: (url: string) => EventSourceLike;
  onDecision: (d: Decision) => void;
  onGap: () => void;
  /** onReset fires when a filter change discards what was shown. */
  onReset: () => void;
  onState: (s: FeedState) => void;
}

const MIN_BACKOFF_MS = 1000;
const MAX_BACKOFF_MS = 15000;

/**
 * Feed follows /api/decisions. EventSource cannot be told a resume point, so
 * on a drop the feed closes it and reopens with last_event_id, backing off
 * between failed attempts; ids at or below the last seen are dropped so a
 * replay overlap never duplicates a row.
 */
export class Feed {
  private src: EventSourceLike | null = null;
  private timer: ReturnType<typeof setTimeout> | null = null;
  private lastId: number | null = null;
  private backoff = MIN_BACKOFF_MS;
  private running = false;

  constructor(private o: FeedOptions) {}

  start(): void {
    this.running = true;
    this.connect();
  }

  stop(): void {
    this.running = false;
    this.teardown();
    this.o.onState("stopped");
  }

  setFilter(f: FeedFilter): void {
    this.o.filter = f;
    this.teardown();
    this.lastId = null;
    this.backoff = MIN_BACKOFF_MS;
    this.o.onReset();
    if (this.running) this.connect();
  }

  private teardown(): void {
    if (this.timer !== null) clearTimeout(this.timer);
    this.timer = null;
    this.src?.close();
    this.src = null;
  }

  private connect(): void {
    this.o.onState(this.lastId === null ? "connecting" : "reconnecting");
    const src = this.o.open(feedUrl(this.o.base, this.o.filter, this.lastId));
    this.src = src;
    src.onopen = () => {
      this.backoff = MIN_BACKOFF_MS;
      this.o.onState("live");
    };
    src.addEventListener("decision", (e) => this.decision(e.data));
    src.addEventListener("gap", () => this.o.onGap());
    src.onerror = () => this.dropped(src);
  }

  private decision(data: string): void {
    let d: Decision;
    try {
      d = JSON.parse(data) as Decision;
    } catch {
      return;
    }
    if (this.lastId !== null && d.id <= this.lastId) return;
    this.lastId = d.id;
    this.o.onDecision(d);
  }

  private dropped(src: EventSourceLike): void {
    if (src !== this.src) return;
    src.close();
    this.src = null;
    this.o.onState("reconnecting");
    const wait = this.backoff;
    this.backoff = Math.min(this.backoff * 2, MAX_BACKOFF_MS);
    this.timer = setTimeout(() => {
      this.timer = null;
      if (this.running) this.connect();
    }, wait);
  }
}
