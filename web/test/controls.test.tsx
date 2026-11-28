// @vitest-environment jsdom
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderToStaticMarkup } from "react-dom/server";
import { Controls, ScenarioTable, WeightGrid } from "../src/views/Controls";
import type { ScenarioInfo } from "../src/lib/meta";

const scenarios: ScenarioInfo[] = [
  { name: "info_proposal", description: "Proposes an info action." },
  { name: "go_dark", arg: "pool actor id", optional: true, example: "3", description: "Takes a pool offline." },
];

describe("controls page", () => {
  it("offers one number field per weight, empty meaning the profile's weight", () => {
    const html = renderToStaticMarkup(<WeightGrid names={["send", "vote", "mint"]} values={{ vote: "2" }} onChange={() => {}} />);
    expect(html.match(/type="number"/g)).toHaveLength(3);
    for (const n of ["send", "vote", "mint"]) expect(html).toContain(`<span>${n}</span>`);
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
      const body = url === "/api/meta/enums" ? { weight: ["send"], scenarios } : { seq: 9, duplicate: false };
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
