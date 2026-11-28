// @vitest-environment jsdom
import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderToStaticMarkup } from "react-dom/server";
import { Controls, ScenarioTable, WeightGrid } from "../src/views/Controls";
import type { ScenarioInfo } from "../src/lib/meta";
import { buildControl, formFromState, type ControlBody, type ControlState } from "../src/lib/controls";

const scenarios: ScenarioInfo[] = [
  { name: "info_proposal", description: "Proposes an info action." },
  { name: "go_dark", arg: "pool actor id", optional: true, example: "3", description: "Takes a pool offline." },
];

describe("controls page", () => {
  it("offers one number field per weight, empty meaning the profile's weight", () => {
    const html = renderToStaticMarkup(<WeightGrid names={["send", "vote", "mint"]} values={{ vote: "2" }} onChange={() => {}} />);
    expect(html.match(/type="number"/g)).toHaveLength(3);
    for (const n of ["send", "vote", "mint"]) expect(html).toContain(`>${n}</label>`);
    expect(html).toContain('value="2"');
    expect(html).toContain('placeholder="profile"');
  });

  it("describes every scenario and its argument", () => {
    const html = renderToStaticMarkup(<ScenarioTable list={scenarios} />);
    expect(html.match(/<tr[ >]/g)).toHaveLength(3);
    expect(html).toContain("Proposes an info action.");
    expect(html).toContain("pool actor id (optional)");
    expect(html).toMatch(/<code[^>]*>3<\/code>/);
  });
});

describe("controls confirm dialog", () => {
  it("asks before sending, and Cancel sends nothing", async () => {
    const sent: string[] = [];
    vi.stubGlobal("fetch", async (url: string, init?: RequestInit) => {
      if (init?.method === "POST") sent.push(String(init.body));
      // useEnums caches its first answer for the module, so every test here serves the same lists.
      const body = url === "/api/meta/enums" ? { weight: ["send", "vote"], scenarios } : url === "/api/controls" ? { paused: false, k: 1, weights: {}, last: null } : { seq: 9, duplicate: false };
      return new Response(JSON.stringify(body), { status: 200 });
    });
    const user = userEvent.setup();
    render(<Controls />);
    await user.click(screen.getByRole("button", { name: "Pause" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog.textContent).toContain("Pause the run?");
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(sent).toEqual([]);

    await user.click(screen.getByRole("button", { name: "Pause" }));
    await user.click(await screen.findByRole("button", { name: "Confirm" }));
    expect((await screen.findByRole("status")).textContent).toBe("Accepted (log seq 9).");
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(sent).toHaveLength(1);
    expect(JSON.parse(sent[0]!).op).toBe("pause");
    vi.unstubAllGlobals();
  });
});

describe("controls state", () => {
  it("turns the engine's state into form values, an unset weight left empty", () => {
    const state: ControlState = { paused: false, k: 3, weights: { send: 2, vote: 0 }, last: null };
    expect(formFromState(state)).toEqual({ k: "3", weights: { send: "2", vote: "0" } });
  });

  it("builds a clear_weights control with no fields of its own", () => {
    const b = buildControl("clear_weights", { weights: { send: "2" } });
    expect(b.ok && b.body.op).toBe("clear_weights");
    expect(b.ok && Object.keys(b.body).sort()).toEqual(["op", "request_id"]);
  });
});

/** engine fakes the API: GET /api/controls answers its state, and a POST applies through apply. */
function engine(start: ControlState, apply: (b: ControlBody, s: ControlState) => ControlState) {
  let state = start;
  const posts: ControlBody[] = [];
  vi.stubGlobal("fetch", async (url: string, init?: RequestInit) => {
    if (init?.method === "POST") {
      const b = JSON.parse(String(init.body)) as ControlBody;
      posts.push(b);
      state = { ...apply(b, state), last: { request_id: b.request_id, op: b.op, seq: 10 + posts.length, at: 0 } };
      return new Response(JSON.stringify({ seq: 10 + posts.length, duplicate: false }), { status: 202 });
    }
    if (url === "/api/meta/enums") return new Response(JSON.stringify({ weight: ["send", "vote"], scenarios }), { status: 200 });
    if (url === "/api/controls") return new Response(JSON.stringify(state), { status: 200 });
    return new Response("{}", { status: 404 });
  });
  return posts;
}

const field = (id: string) => document.getElementById(id) as HTMLInputElement;
const weight = (name: string) => screen.getByLabelText(name, { selector: "input" }) as HTMLInputElement;

describe("controls form", () => {
  it("starts from the engine's k, weights and paused state", async () => {
    engine({ paused: true, k: 3, weights: { send: 2 }, last: null }, (_b, s) => s);
    render(<Controls />);
    await waitFor(() => expect(field("c-k").value).toBe("3"));
    expect(weight("send").value).toBe("2");
    expect(weight("vote").value).toBe("");
    expect(weight("vote").placeholder).toBe("profile");
    expect(screen.getByTestId("current-send").textContent).toContain("current 2");
    expect(screen.queryByTestId("current-vote")).toBeNull();
    expect(screen.getByTestId("run-state").textContent).toContain("Paused");
    vi.unstubAllGlobals();
  });

  it("after a post shows what the engine applied, not what was typed", async () => {
    // The engine caps k at 5 here, so the form must show 5, not 7.
    const posts = engine({ paused: false, k: 1, weights: {}, last: null }, (b, s) => (b.op === "set_k" ? { ...s, k: Math.min(b.k ?? 1, 5) } : s));
    const user = userEvent.setup();
    render(<Controls />);
    await waitFor(() => expect(field("c-k").value).toBe("1"));
    await user.clear(field("c-k"));
    await user.type(field("c-k"), "7");
    await user.click(screen.getByRole("button", { name: "Set k" }));
    await user.click(await screen.findByRole("button", { name: "Confirm" }));
    await waitFor(() => expect(field("c-k").value).toBe("5"));
    expect(posts.map((p) => p.op)).toEqual(["set_k"]);
    vi.unstubAllGlobals();
  });

  it("Clear form goes back to the engine's values, and Reset weights sends clear_weights", async () => {
    const posts = engine({ paused: false, k: 2, weights: { send: 2 }, last: null }, (b, s) => (b.op === "clear_weights" ? { ...s, weights: {} } : s));
    const user = userEvent.setup();
    render(<Controls />);
    await waitFor(() => expect(weight("send").value).toBe("2"));
    await user.type(weight("vote"), "4");
    await user.clear(weight("send"));
    await user.click(screen.getByRole("button", { name: "Clear form" }));
    expect(weight("send").value).toBe("2");
    expect(weight("vote").value).toBe("");

    await user.click(screen.getByRole("button", { name: "Reset weights to profile" }));
    await user.click(await screen.findByRole("button", { name: "Confirm" }));
    await waitFor(() => expect(weight("send").value).toBe(""));
    expect(screen.queryByTestId("current-send")).toBeNull();
    expect(posts.map((p) => p.op)).toEqual(["clear_weights"]);
    vi.unstubAllGlobals();
  });
});
