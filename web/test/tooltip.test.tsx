// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HelpTip } from "../src/views/fields";

describe("help tip in the browser", () => {
  it("opens on keyboard focus and closes on Escape", async () => {
    const user = userEvent.setup();
    render(<HelpTip id="t1" label="State" text="what it means" />);
    expect(screen.queryByRole("tooltip")).toBeNull();
    await user.tab();
    const btn = screen.getByRole("button", { name: "About State" });
    expect(document.activeElement).toBe(btn);
    expect((await screen.findByRole("tooltip")).textContent).toContain("what it means");
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("tooltip")).toBeNull();
  });

  it("is described by its text while closed", () => {
    render(<HelpTip id="t2" label="Kind" text="wallet or contract" />);
    expect(screen.getByRole("button", { name: "About Kind" }).getAttribute("aria-describedby")).toBe("t2");
    expect(document.getElementById("t2")!.textContent).toBe("wallet or contract");
  });
});
