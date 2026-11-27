import { describe, expect, it } from "vitest";
import { renderToStaticMarkup as render } from "react-dom/server";
import { chances, percent } from "../src/lib/chance";
import { Why } from "../src/views/Decisions";
import { FIELD_HELP, GLOSSARY } from "../src/lib/help";
import type { Decision, Score } from "../src/lib/types";

const sc = (kind: string, value: number, weight?: number, why = ""): Score => ({ kind, value, weight, why });

describe("chances", () => {
  // Same numbers as TestChooseProportional in internal/agenda: send 1, delegate 2 x 3, idle 1.
  it("is score x weight over the total", () => {
    expect(chances([sc("send", 1, 1), sc("delegate", 2, 3), sc("idle", 1, 1)])).toEqual([0.125, 0.75, 0.125]);
  });
  it("treats a missing weight as 1", () => {
    expect(chances([sc("a", 1), sc("b", 3)])).toEqual([0.25, 0.75]);
  });
  it("clamps NaN, negative and infinite to zero", () => {
    expect(chances([sc("a", NaN), sc("b", -2), sc("c", Infinity), sc("d", 2, NaN), sc("e", 2)])).toEqual([0, 0, 0, 0, 1]);
  });
  it("gives every entry zero when nothing is positive (the draw is idle)", () => {
    expect(chances([sc("a", 0), sc("b", 5, 0)])).toEqual([0, 0]);
    expect(chances([])).toEqual([]);
  });
  it("sums to one over positive entries", () => {
    const c = chances([sc("a", 0.3, 1.7), sc("b", 4), sc("c", 0), sc("idle", 1)]);
    expect(c.reduce((x, y) => x + y, 0)).toBeCloseTo(1, 12);
  });
  it("formats percent with one decimal and a dash for zero", () => {
    expect(percent(0.125)).toBe("12.5%");
    expect(percent(0)).toBe("—");
  });
});

describe("why box", () => {
  const d: Decision = {
    id: 7, at: 0, kind: "IntentChosen", actor: 1, intent: "send", clock: "send",
    scores: [sc("idle", 1, 1, "baseline"), sc("send", 3, 1, "rich"), sc("vote", 0, 1, "balance below cost")],
  };
  const html = render(<Why d={d} />);
  it("has the four columns, each with its tooltip text in the DOM", () => {
    for (const k of ["intent", "score", "weight", "chance"]) {
      const h = FIELD_HELP[k]!;
      expect(html).toContain(`<th>${h.label} `);
      const m = new RegExp(`aria-describedby="(why-7-${k})"`).exec(html);
      expect(m).not.toBeNull();
      expect(html).toContain(`id="why-7-${k}"`);
      expect(html).toContain(h.help.replace(/&/g, "&amp;").replace(/'/g, "&#x27;"));
    }
  });
  it("shows chance percentages, highlights the chosen row and dashes the zero", () => {
    expect(html).toContain("25.0%");
    expect(html).toContain("75.0%");
    expect(html).toContain('<tr class="chosen"><td>send (chosen)');
    expect(html).toContain("balance below cost. ");
    expect(html).toContain('id="why-7-z2"');
  });
  it("explains the formula, and the clock for population actors", () => {
    expect(html).toContain("Chance = score x weight / total; idle scores 1 as the baseline.");
    expect(html).toContain("Chosen among send options after its fitted clock fired.");
    expect(render(<Why d={{ ...d, clock: undefined }} />)).not.toContain("options after its fitted clock fired");
  });
  it("glossary shares the help text", () => {
    expect(GLOSSARY.find((g) => g.term === "Chance")!.def).toBe(FIELD_HELP.chance!.help);
  });
});
