import { describe, expect, it, vi } from "vitest";
import { Feed, appendDecision, feedUrl, type EventSourceLike } from "../src/lib/feed";
import type { Decision } from "../src/lib/types";

class FakeSource implements EventSourceLike {
  static all: FakeSource[] = [];
  listeners = new Map<string, (e: { data: string; lastEventId: string }) => void>();
  onerror: (() => void) | null = null;
  onopen: (() => void) | null = null;
  closed = false;
  constructor(public url: string) {
    FakeSource.all.push(this);
  }
  addEventListener(t: string, fn: (e: { data: string; lastEventId: string }) => void) {
    this.listeners.set(t, fn);
  }
  close() {
    this.closed = true;
  }
  emit(id: number, extra: Partial<Decision> = {}) {
    const d = { id, at: id, kind: "IntentChosen", actor: 1, ...extra };
    this.listeners.get("decision")?.({ data: JSON.stringify(d), lastEventId: String(id) });
  }
}

function setup() {
  FakeSource.all = [];
  const seen: number[] = [];
  const states: string[] = [];
  const gaps: number[] = [];
  const resets: number[] = [];
  const feed = new Feed({
    base: "/api/decisions",
    filter: {},
    open: (u) => new FakeSource(u),
    onDecision: (d) => seen.push(d.id),
    onGap: () => gaps.push(1),
    onReset: () => resets.push(1),
    onState: (s) => states.push(s),
  });
  return { feed, seen, states, gaps, resets };
}

describe("decision feed", () => {
  it("builds the URL from the filter and the resume point", () => {
    expect(feedUrl("/api/decisions", {}, null)).toBe("/api/decisions");
    expect(feedUrl("/api/decisions", { actor: 7, profile: "hodl", kinds: ["IntentChosen", "Submitted"] }, 41)).toBe(
      "/api/decisions?actor=7&profile=hodl&kind=IntentChosen%2CSubmitted&last_event_id=41",
    );
  });

  it("reconnects with the last event id after the stream drops", () => {
    vi.useFakeTimers();
    const { feed, seen, states } = setup();
    feed.start();
    const first = FakeSource.all[0]!;
    first.onopen?.();
    first.emit(5);
    first.emit(6);
    first.onerror?.();
    expect(first.closed).toBe(true);
    expect(states.at(-1)).toBe("reconnecting");
    vi.advanceTimersByTime(60_000);
    const second = FakeSource.all[1]!;
    expect(second.url).toContain("last_event_id=6");
    second.onopen?.();
    second.emit(6); // replay overlap
    second.emit(7);
    expect(seen).toEqual([5, 6, 7]);
    expect(states.at(-1)).toBe("live");
    feed.stop();
    vi.useRealTimers();
  });

  it("backs off between failed attempts", () => {
    vi.useFakeTimers();
    const { feed } = setup();
    feed.start();
    FakeSource.all[0]!.onerror?.();
    vi.advanceTimersByTime(999);
    expect(FakeSource.all).toHaveLength(1);
    vi.advanceTimersByTime(1);
    expect(FakeSource.all).toHaveLength(2);
    FakeSource.all[1]!.onerror?.();
    vi.advanceTimersByTime(1999);
    expect(FakeSource.all).toHaveLength(2);
    vi.advanceTimersByTime(1);
    expect(FakeSource.all).toHaveLength(3);
    feed.stop();
    vi.useRealTimers();
  });

  it("reports a gap and a filter change starts afresh without a resume id", () => {
    const { feed, gaps, resets } = setup();
    feed.start();
    const first = FakeSource.all[0]!;
    first.emit(3);
    first.listeners.get("gap")?.({ data: "{}", lastEventId: "" });
    expect(gaps).toHaveLength(1);
    feed.setFilter({ profile: "yield" });
    expect(first.closed).toBe(true);
    expect(resets).toHaveLength(1);
    expect(FakeSource.all[1]!.url).toBe("/api/decisions?profile=yield");
    feed.stop();
  });

  it("does not reconnect after stop", () => {
    vi.useFakeTimers();
    const { feed } = setup();
    feed.start();
    FakeSource.all[0]!.onerror?.();
    feed.stop();
    vi.advanceTimersByTime(60_000);
    expect(FakeSource.all).toHaveLength(1);
    vi.useRealTimers();
  });

  it("keeps the newest decisions within the cap", () => {
    let list: Decision[] = [];
    for (let i = 1; i <= 5; i++) list = appendDecision(list, { id: i } as Decision, 3);
    expect(list.map((d) => d.id)).toEqual([5, 4, 3]);
  });
});
