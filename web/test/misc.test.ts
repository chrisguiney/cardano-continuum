import { describe, expect, it } from "vitest";
import { buildControl, newRequestId } from "../src/lib/controls";
import { ada, count } from "../src/lib/format";
import { layout } from "../src/lib/layout";
import { parseRoute } from "../src/lib/router";

describe("routes", () => {
  it("maps paths to views", () => {
    expect(parseRoute("/")).toEqual({ name: "overview" });
    expect(parseRoute("/actors")).toEqual({ name: "actors" });
    expect(parseRoute("/actors/42")).toEqual({ name: "actor", id: 42 });
    expect(parseRoute("/pools")).toEqual({ name: "pools" });
    expect(parseRoute("/decisions")).toEqual({ name: "decisions" });
    expect(parseRoute("/findings")).toEqual({ name: "findings" });
    expect(parseRoute("/controls")).toEqual({ name: "controls" });
    expect(parseRoute("/actors/x")).toEqual({ name: "notfound" });
    expect(parseRoute("/nope")).toEqual({ name: "notfound" });
  });
});

describe("controls", () => {
  it("builds the payloads the API accepts", () => {
    expect(buildControl("pause", {})).toEqual({ ok: true, body: { request_id: expect.any(String), op: "pause" } });
    expect(buildControl("set_k", { k: "8" })).toMatchObject({ ok: true, body: { op: "set_k", k: 8 } });
    expect(buildControl("inject", { scenario: "partition" })).toMatchObject({ ok: true, body: { scenario: "partition" } });
    expect(buildControl("inject", { scenario: "go_dark", arg: "3" })).toMatchObject({ ok: true, body: { scenario: "go_dark:3" } });
    expect(buildControl("inject", { scenario: "upgrade", arg: "2:ghcr.io/blinklabs-io/dingo:main" })).toMatchObject({ ok: true, body: { scenario: "upgrade:2:ghcr.io/blinklabs-io/dingo:main" } });
    expect(buildControl("inject", { scenario: "go_dark", arg: " " })).toMatchObject({ ok: true, body: { scenario: "go_dark" } });
    expect(buildControl("set_weights", { weights: { send: "2", vote: "0.5", mint: "" } })).toMatchObject({ ok: true, body: { weights: { send: 2, vote: 0.5 } } });
  });

  it("refuses what the server would refuse", () => {
    expect(buildControl("set_k", { k: "0" }).ok).toBe(false);
    expect(buildControl("set_k", { k: "1001" }).ok).toBe(false);
    expect(buildControl("set_k", { k: "1.5" }).ok).toBe(false);
    expect(buildControl("inject", { scenario: "Bad Name" }).ok).toBe(false);
    expect(buildControl("inject", { scenario: "upgrade", arg: "a b" }).ok).toBe(false);
    expect(buildControl("set_weights", { weights: {} }).ok).toBe(false);
    expect(buildControl("set_weights", { weights: { send: "" } }).ok).toBe(false);
    expect(buildControl("set_weights", { weights: { send: "-1" } }).ok).toBe(false);
    expect(buildControl("set_weights", { weights: { send: "x" } }).ok).toBe(false);
    expect(buildControl("set_weights", { weights: { Send: "1" } }).ok).toBe(false);
  });

  it("makes request ids the server accepts", () => {
    expect(newRequestId()).toMatch(/^[A-Za-z0-9_-]{1,64}$/);
    expect(newRequestId()).not.toBe(newRequestId());
  });
});

describe("format", () => {
  it("shows lovelace as ADA", () => {
    expect(ada(5_000_000)).toBe("5.00 ADA");
    expect(ada(350_000)).toBe("0.35 ADA");
    expect(ada(45_000_000_000_000)).toBe("45.00M ADA");
  });
  it("groups counts", () => {
    expect(count(100000)).toBe("100,000");
  });
});

describe("graph layout", () => {
  const nodes = ["a", "b", "c", "d"].map((id) => ({ id }));
  const edges = [{ from: "a", to: "b" }, { from: "b", to: "c" }, { from: "c", to: "d" }];

  it("is deterministic and inside the box", () => {
    const one = layout(nodes, edges, 400, 300);
    const two = layout(nodes, edges, 400, 300);
    expect(one).toEqual(two);
    for (const n of nodes) {
      const p = one.get(n.id)!;
      expect(Number.isFinite(p.x) && Number.isFinite(p.y)).toBe(true);
      expect(p.x).toBeGreaterThanOrEqual(0);
      expect(p.x).toBeLessThanOrEqual(400);
      expect(p.y).toBeGreaterThanOrEqual(0);
      expect(p.y).toBeLessThanOrEqual(300);
    }
  });

  it("keeps nodes apart and ignores edges to unknown nodes", () => {
    const pos = layout(nodes, [...edges, { from: "a", to: "zz" }], 400, 300);
    const a = pos.get("a")!;
    const b = pos.get("b")!;
    expect(Math.hypot(a.x - b.x, a.y - b.y)).toBeGreaterThan(10);
  });

  it("handles none and one", () => {
    expect(layout([], [], 100, 100).size).toBe(0);
    expect(layout([{ id: "x" }], [], 100, 100).get("x")).toEqual({ x: 50, y: 50 });
  });
});
