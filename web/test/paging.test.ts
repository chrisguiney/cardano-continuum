import { describe, expect, it } from "vitest";
import { clampOffset, initialPage, nextPage, pageInfo, prevPage, setFilter, setSort, toQuery } from "../src/lib/paging";

describe("actor paging state", () => {
  it("starts at the first page with a bounded limit", () => {
    const s = initialPage();
    expect(s.offset).toBe(0);
    expect(s.limit).toBeGreaterThan(0);
    expect(s.limit).toBeLessThanOrEqual(500);
  });

  it("encodes only what is set", () => {
    const q = new URLSearchParams(toQuery({ ...initialPage(), limit: 50, offset: 100, sort: "txs", desc: true, profile: "hodl" }));
    expect(Object.fromEntries(q)).toEqual({ limit: "50", offset: "100", sort: "txs", dir: "desc", profile: "hodl" });
    expect(toQuery(initialPage())).not.toContain("profile");
  });

  it("returns to page one when sort or filter change", () => {
    const s = { ...initialPage(), offset: 300 };
    expect(setSort(s, "name").offset).toBe(0);
    expect(setFilter(s, "state", "active").offset).toBe(0);
    expect(setFilter(s, "state", "active").state).toBe("active");
  });

  it("toggles direction on the same column and resets it on a new one", () => {
    let s = setSort(initialPage(), "txs");
    expect(s.sort).toBe("txs");
    expect(s.desc).toBe(false);
    s = setSort(s, "txs");
    expect(s.desc).toBe(true);
    s = setSort(s, "name");
    expect(s.desc).toBe(false);
  });

  it("pages within the total and never past the end", () => {
    const s = { ...initialPage(), limit: 100 };
    expect(prevPage(s).offset).toBe(0);
    const n = nextPage(s, 250);
    expect(n.offset).toBe(100);
    expect(nextPage(nextPage(n, 250), 250).offset).toBe(200);
    expect(nextPage({ ...s, offset: 200 }, 250).offset).toBe(200);
    expect(nextPage(s, 0).offset).toBe(0);
  });

  it("clamps an offset left past the end by a shrinking filter", () => {
    expect(clampOffset({ ...initialPage(), limit: 100, offset: 900 }, 250).offset).toBe(200);
    expect(clampOffset({ ...initialPage(), limit: 100, offset: 900 }, 0).offset).toBe(0);
  });

  it("describes the visible range for 100k actors without fetching them", () => {
    expect(pageInfo({ ...initialPage(), limit: 100, offset: 99900 }, 100000)).toEqual({ from: 99901, to: 100000, page: 1000, pages: 1000 });
    expect(pageInfo(initialPage(), 0)).toEqual({ from: 0, to: 0, page: 1, pages: 1 });
  });
});
