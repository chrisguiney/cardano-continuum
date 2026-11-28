// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SidebarProvider, SidebarTrigger } from "../src/components/ui/sidebar";
import { AppSidebar } from "../src/views/AppSidebar";

const caps = { controls: true, explorer: true };

function mount(path = "/findings", newFindings = 3) {
  return render(
    <SidebarProvider>
      <SidebarTrigger />
      <AppSidebar path={path} caps={caps} newFindings={newFindings} />
    </SidebarProvider>,
  );
}

beforeEach(() => localStorage.clear());
afterEach(() => localStorage.clear());

describe("sidebar", () => {
  it("lists headed groups, marks the current page and badges new findings", () => {
    mount("/findings");
    const nav = screen.getByRole("navigation", { name: "Views" });
    const heads = within(nav).getAllByRole("heading").map((h) => h.textContent);
    expect(heads).toEqual(["Network", "Activity", "Testing", "Run"]);
    const links = within(nav).getAllByRole("link").map((a) => a.textContent?.replace(/\d+ new$/, "").trim());
    expect(links[0]).toBe("Overview");
    expect(links[links.length - 1]).toBe("Help");
    const cur = within(nav).getAllByRole("link").filter((a) => a.getAttribute("aria-current") === "page");
    expect(cur.map((a) => a.getAttribute("href"))).toEqual(["/findings"]);
    expect(within(nav).getByLabelText("3 new findings").textContent).toBe("3");
  });

  it("shows no badge when there are no new findings", () => {
    mount("/", 0);
    expect(screen.queryByLabelText(/new findings/)).toBeNull();
  });

  it("collapses to icons, keeps names for assistive tech and remembers the choice", async () => {
    const user = userEvent.setup();
    const { unmount } = mount();
    const btn = screen.getByRole("button", { name: "Collapse sidebar" });
    expect(btn.getAttribute("aria-expanded")).toBe("true");
    await user.click(btn);
    expect(localStorage.getItem("sidebar")).toBe("collapsed");
    expect(screen.queryAllByRole("heading")).toHaveLength(0);
    expect(screen.getByRole("link", { name: /Findings/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Expand sidebar" }).getAttribute("aria-expanded")).toBe("false");
    unmount();
    mount();
    expect(screen.getByRole("button", { name: "Expand sidebar" })).toBeTruthy();
  });

  it("shows a tooltip on a collapsed item on keyboard focus", async () => {
    localStorage.setItem("sidebar", "collapsed");
    const user = userEvent.setup();
    mount("/");
    await user.tab();
    await user.tab();
    expect((await screen.findByRole("tooltip")).textContent).toContain("Overview");
  });

  it("opens the off-canvas sheet from the top bar button and closes it on navigation", async () => {
    const user = userEvent.setup();
    mount("/");
    expect(screen.queryByRole("dialog")).toBeNull();
    await user.click(screen.getByRole("button", { name: "Open menu" }));
    const sheet = await screen.findByRole("dialog");
    const link = within(sheet).getByRole("link", { name: /Nodes/ });
    await user.click(link);
    expect(screen.queryByRole("dialog")).toBeNull();
  });
});
