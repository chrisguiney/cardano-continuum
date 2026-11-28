// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { EngineProvider, StartupBanner } from "../src/views/startup";
import { nextPhase, waitedText, type EngineStatus } from "../src/lib/engine";
import { Overview } from "../src/views/Overview";

const st = (engine: EngineStatus["engine"]): EngineStatus => ({ engine, since: "2026-01-01T00:00:00Z", now: "2026-01-01T00:02:05Z", last_error: engine === "up" ? undefined : "probe engine api: refused" });

describe("nextPhase", () => {
  it.each([
    ["checking", st("starting"), "starting"],
    ["checking", st("up"), "up"],
    ["checking", "missing", "unavailable"],
    ["starting", st("up"), "up"],
    ["starting", "missing", "starting"],
    ["up", "missing", "up"],
    ["up", st("starting"), "unreachable"],
    ["up", st("unreachable"), "unreachable"],
    ["unreachable", st("up"), "up"],
  ] as const)("%s + %j -> %s", (prev, res, want) => {
    expect(nextPhase(prev, res)).toBe(want);
  });
});

describe("waitedText", () => {
  it("reads at a glance", () => {
    expect(waitedText(45_000)).toBe("45 s");
    expect(waitedText(125_000)).toBe("2 min 5 s");
    expect(waitedText(-5)).toBe("0 s");
  });
});

/** fakeApi answers /api/_ui/status from status and every other path with other. */
function fakeApi(status: () => EngineStatus | null, other: () => { code: number; body: unknown }) {
  globalThis.fetch = vi.fn((input: RequestInfo | URL) => {
    const path = String(input);
    const r = path === "/api/_ui/status" ? { code: status() ? 200 : 404, body: status() ?? {} } : other();
    return Promise.resolve(new Response(JSON.stringify(r.body), { status: r.code, headers: { "Content-Type": "application/json" } }));
  }) as typeof fetch;
}

afterEach(() => vi.restoreAllMocks());

describe("startup", () => {
  it("shows one calm banner and no error banners while the engine starts, then clears", async () => {
    let engine = st("starting");
    let overview: { code: number; body: unknown } = { code: 502, body: { error: "engine unavailable" } };
    fakeApi(() => engine, () => overview);
    render(
      <EngineProvider pollMs={20}>
        <StartupBanner />
        <Overview />
      </EngineProvider>,
    );

    const banner = await screen.findByRole("status");
    expect(banner.textContent).toContain("The devnet is starting.");
    expect(banner.textContent).toContain("Everything is working");
    expect(banner.textContent).toContain("Data will appear here automatically.");
    expect(banner.textContent).toContain("starting");
    expect(banner.textContent).toContain("2 min");
    expect(screen.getByRole("heading", { name: "Overview" })).toBeTruthy();
    expect(screen.queryByRole("alert")).toBeNull();
    expect(document.body.textContent).not.toContain("Could not load");

    engine = st("up");
    overview = { code: 200, body: {} };
    await waitFor(() => expect(screen.queryByRole("status")).toBeNull());
  });

  it("still shows a genuine error once the engine has been up", async () => {
    fakeApi(() => st("up"), () => ({ code: 500, body: { error: "boom" } }));
    render(
      <EngineProvider pollMs={20}>
        <StartupBanner />
        <Overview />
      </EngineProvider>,
    );

    expect((await screen.findByRole("alert")).textContent).toContain("boom");
    expect(screen.queryByText(/The devnet is starting/)).toBeNull();
  });

  it("behaves normally where the server has no status route", async () => {
    fakeApi(() => null, () => ({ code: 502, body: { error: "engine unavailable" } }));
    render(
      <EngineProvider pollMs={20}>
        <StartupBanner />
        <Overview />
      </EngineProvider>,
    );

    expect((await screen.findByRole("alert")).textContent).toContain("engine unavailable");
    expect(screen.queryByText(/The devnet is starting/)).toBeNull();
  });
});
