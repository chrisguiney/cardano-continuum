// @vitest-environment jsdom
import { describe, expect, it, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { loadStyle, parseStyle, saveStyle } from "../src/lib/appearance";
import { StyleSelect } from "../src/views/StyleSelect";

describe("style choice", () => {
  beforeEach(() => localStorage.clear());

  it("defaults to continuum and ignores junk", () => {
    expect(loadStyle()).toBe("continuum");
    localStorage.setItem("style", "neon");
    expect(loadStyle()).toBe("continuum");
    expect(parseStyle("shadcn")).toBe("shadcn");
  });

  it("saves and reloads", () => {
    saveStyle("shadcn");
    expect(loadStyle()).toBe("shadcn");
  });

  it("selector reports a keyboard choice", async () => {
    const user = userEvent.setup();
    const seen: string[] = [];
    render(<StyleSelect style="continuum" onChange={(s) => seen.push(s)} />);
    const sel = screen.getByRole("combobox", { name: "Style" });
    await user.tab();
    expect(document.activeElement).toBe(sel);
    await user.selectOptions(sel, "shadcn");
    expect(seen).toEqual(["shadcn"]);
    expect(screen.getByRole("button", { name: "About Style" })).toBeTruthy();
  });
});
