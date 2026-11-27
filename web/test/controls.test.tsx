import { describe, expect, it } from "vitest";
import { render } from "preact-render-to-string";
import { ScenarioTable, WeightGrid } from "../src/views/Controls";
import type { ScenarioInfo } from "../src/lib/meta";

const scenarios: ScenarioInfo[] = [
  { name: "info_proposal", description: "Proposes an info action." },
  { name: "go_dark", arg: "pool actor id", optional: true, example: "3", description: "Takes a pool offline." },
];

describe("controls page", () => {
  it("offers one number field per weight, empty meaning the profile's weight", () => {
    const html = render(<WeightGrid names={["send", "vote", "mint"]} values={{ vote: "2" }} onChange={() => {}} />);
    expect(html.match(/type="number"/g)).toHaveLength(3);
    for (const n of ["send", "vote", "mint"]) expect(html).toContain(`<span>${n}</span>`);
    expect(html).toContain('value="2"');
    expect(html).toContain('placeholder="profile"');
  });

  it("describes every scenario and its argument", () => {
    const html = render(<ScenarioTable list={scenarios} />);
    expect(html.match(/<tr>/g)).toHaveLength(3);
    expect(html).toContain("Proposes an info action.");
    expect(html).toContain("pool actor id (optional)");
    expect(html).toContain("<code>3</code>");
  });
});
