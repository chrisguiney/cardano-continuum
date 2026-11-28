import { describe, expect, it } from "vitest";

// The test tsconfig has no Node types; the file read is the only Node use.
// @ts-expect-error node:fs has no declarations here
const { readFileSync } = (await import("node:fs")) as { readFileSync(p: URL, enc: string): string };
const css = readFileSync(new URL("../src/index.css", import.meta.url), "utf8");

// blocks returns the custom-property names each top-level rule whose
// selector matches defines.
function blocks(match: RegExp): string[][] {
  const out: string[][] = [];
  for (const m of css.replace(/\/\*[\s\S]*?\*\//g, "").matchAll(/([^{};]+)\{([^{}]*)\}/g)) {
    if (!match.test(m[1]!)) continue;
    out.push([...m[2]!.matchAll(/(--[\w-]+)\s*:/g)].map((v) => v[1]!).sort());
  }
  return out;
}

describe("style palettes", () => {
  it("define the same variables in all four blocks", () => {
    const four = {
      "continuum light": blocks(/continuum"\]:not\(\[data-theme="dark"\]\)/),
      "continuum dark": blocks(/continuum"\]\[data-theme="dark"\]/),
      "shadcn light": blocks(/shadcn"\]:not\(\[data-theme="dark"\]\)/),
      "shadcn dark": blocks(/shadcn"\]\[data-theme="dark"\]/),
    };
    for (const [name, found] of Object.entries(four)) expect(found, name).toHaveLength(1);
    const ref = four["continuum light"][0]!;
    expect(ref.length).toBeGreaterThan(20);
    for (const [name, found] of Object.entries(four)) expect(found[0], name).toEqual(ref);
  });
});
