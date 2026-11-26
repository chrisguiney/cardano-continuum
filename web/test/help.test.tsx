import { describe, expect, it } from "vitest";
import { render } from "preact-render-to-string";
import { SelectField, HelpTip } from "../src/views/fields";
import { Help } from "../src/views/Help";
import { GLOSSARY, FIELD_HELP, VIEW_BLURB } from "../src/lib/help";
import { optionsOf, type Enums } from "../src/lib/meta";
import { parseQuery, buildQuery } from "../src/lib/query";
import { initialPage, toQuery, fromSearch, setFilter } from "../src/lib/paging";
import { ActorFilters, AdversaryFilterBar, DecisionFilterBar, FindingFilterBar, PoolFilterBar } from "../src/views/filterbars";
import { findingsPath } from "../src/lib/findings";

const enums: Enums = {
  actor_kind: ["contract", "wallet"], agenda: ["hodl", "dapp"], state: ["active", "dormant", "abandoned"],
  intent_kind: ["send", "vote"], finding_kind: ["wrong_class"], severity: ["warn", "error", "fatal"],
  impl: ["dingo", "cardano-node"], tier: ["logical", "forging"], phase: ["Pending", "Forging"],
  attack: ["dust-flood"], decision_kind: ["IntentChosen", "Submitted"],
  voter_class: ["committee", "drep", "spo"], action_type: ["info"],
};

describe("select field", () => {
  const html = render(
    <SelectField id="f-state" label="State" help={FIELD_HELP.state!} value="dormant" allLabel="All" options={optionsOf(enums.state, { active: 5 })} onChange={() => {}} />,
  );
  it("renders a select with an All option and every enum value", () => {
    expect(html).toContain("<select");
    expect(html).toMatch(/<option value[^>]*>All<\/option>/);
    for (const v of enums.state) expect(html).toContain(`value="${v}"`);
    expect(html).toContain("active (5)");
  });
  it("selects the current value", () => {
    expect(html).toMatch(/<option value="dormant" selected/);
  });
  it("has an accessible name and description", () => {
    expect(html).toContain('<label for="f-state"');
    expect(html).toContain('id="f-state"');
    const m = /aria-describedby="([^"]+)"/.exec(html);
    expect(m).not.toBeNull();
    expect(html).toContain(`id="${m![1]}"`);
    expect(html).toContain("Lifecycle");
  });
});

describe("help tip", () => {
  it("is a focusable button tied to its text", () => {
    const html = render(<HelpTip id="t1" label="State" text="what it means" />);
    expect(html).toContain("<button");
    expect(html).toContain('aria-describedby="t1"');
    expect(html).toContain('role="tooltip"');
    expect(html).toContain("what it means");
  });
});

describe("every filter has help and every view a blurb", () => {
  it("covers each filter key", () => {
    for (const k of ["profile", "state", "kind", "delegated", "agenda", "severity", "finding_kind", "known", "impl", "tier", "phase", "attack", "intent", "decision_kind", "limit", "sort", "dir", "actor"]) {
      expect(FIELD_HELP[k], k).toBeTruthy();
      expect(FIELD_HELP[k]!.label.length).toBeGreaterThan(0);
    }
    for (const v of ["overview", "actors", "actor", "pools", "decisions", "findings", "adversaries", "controls"]) expect(VIEW_BLURB[v], v).toBeTruthy();
  });
});

describe("help panel", () => {
  it("renders the glossary", () => {
    const html = render(<Help />);
    for (const t of ["Finding", "Verdict", "Intent", "Agenda", "Cluster", "Tier", "Phase", "Known issue", "k"]) {
      expect(GLOSSARY.map((g) => g.term)).toContain(t);
      expect(html).toContain(`>${t}<`);
    }
  });
});

describe("filters and the query string", () => {
  it("an actor filter change updates the API query and the URL together", () => {
    const p = setFilter(initialPage(), "kind", "wallet");
    expect(toQuery(p)).toContain("kind=wallet");
    expect(fromSearch("?" + toQuery(p)).kind).toBe("wallet");
  });
  it("reads only known values back", () => {
    const p = fromSearch("?state=active&sort=bogus&limit=9999&delegated=maybe");
    expect(p.state).toBe("active");
    expect(p.sort).toBe("id");
    expect(p.limit).toBe(100);
    expect(p.delegated).toBe("");
  });
  it("round-trips simple filters", () => {
    const d = { impl: "", tier: "", phase: "" };
    expect(buildQuery({ ...d, tier: "forging" }, d)).toBe("tier=forging");
    expect(parseQuery("?tier=forging&junk=1", d)).toEqual({ impl: "", tier: "forging", phase: "" });
  });
  it("findings fetch path carries the selected filters", () => {
    expect(findingsPath({ known: "false", kind: "wrong_class", severity: "fatal" })).toBe("/api/findings?limit=100&known=false&kind=wrong_class&severity=fatal");
    expect(findingsPath({ known: "", kind: "", severity: "" })).toBe("/api/findings?limit=100");
  });
});

describe("filter bars", () => {
  const noop = () => {};
  const bars: [string, preact.VNode, string[], string[]][] = [
    ["actors", <ActorFilters page={initialPage()} enums={enums} profiles={{ hodl: 3 }} states={{}} onFilter={noop} onLimit={noop} onSort={noop} />, ["f-profile", "f-kind", "f-state", "f-delegated", "f-sort", "f-dir", "f-limit"], ["hodl (3)", "wallet", "dormant"]],
    ["findings", <FindingFilterBar f={{ known: "false", kind: "", severity: "" }} enums={enums} onChange={noop} />, ["f-known", "f-severity", "f-fkind"], ["fatal", "wrong_class"]],
    ["decisions", <DecisionFilterBar f={{ actor: "", profile: "", kind: "", intent: "" }} enums={enums} profiles={{}} onChange={noop} />, ["f-actor", "f-dprofile", "f-dkind", "f-intent"], ["IntentChosen", "vote"]],
    ["pools", <PoolFilterBar f={{ impl: "", tier: "", phase: "" }} enums={enums} onChange={noop} />, ["f-impl", "f-tier", "f-phase"], ["cardano-node", "forging", "Forging"]],
    ["adversaries", <AdversaryFilterBar f={{ attack: "", agenda: "" }} enums={enums} onChange={noop} />, ["f-attack", "f-agenda"], ["dust-flood", "dapp"]],
  ];
  for (const [name, node, ids, values] of bars) {
    it(`${name}: every control is named and described, options come from the enums`, () => {
      const html = render(node);
      for (const id of ids) {
        expect(html, id).toContain(`<label for="${id}"`);
        expect(html, id).toContain(`id="${id}"`);
        expect(html, id).toContain(`aria-describedby="${id}-help"`);
        expect(html, id).toContain(`id="${id}-help"`);
      }
      for (const v of values) expect(html, v).toContain(v);
    });
  }
  it("free text stays only for the actor id", () => {
    const html = render(<DecisionFilterBar f={{ actor: "", profile: "", kind: "", intent: "" }} enums={enums} profiles={{}} onChange={noop} />);
    expect(html.match(/<input/g)).toHaveLength(1);
  });
});
