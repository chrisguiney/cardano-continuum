import { FIELD_HELP, GLOSSARY, VIEW_BLURB } from "../lib/help";
import { Blurb } from "./fields";

const VIEWS: [string, string][] = [
  ["Overview", "overview"], ["Actors", "actors"], ["Pools", "pools"], ["Decisions", "decisions"],
  ["Findings", "findings"], ["Adversaries", "adversaries"], ["Controls", "controls"],
];

const CONTROLS = ["profile", "state", "kind", "delegated", "limit", "sort", "dir", "known", "severity", "finding_kind", "impl", "tier", "phase", "attack", "agenda", "intent", "decision_kind", "voter_class", "action_type", "actor", "theme"];

/** Help defines the jargon and lists what each filter and control does. */
export function Help() {
  return (
    <>
      <h1>Help</h1>
      <Blurb text={VIEW_BLURB.help!} />
      <section class="card" aria-labelledby="h-views">
        <h2 id="h-views">Views</h2>
        <dl class="glossary">
          {VIEWS.map(([name, key]) => (
            <>
              <dt>{name}</dt>
              <dd>{VIEW_BLURB[key]}</dd>
            </>
          ))}
        </dl>
      </section>
      <section class="card" aria-labelledby="h-gloss">
        <h2 id="h-gloss">Glossary</h2>
        <dl class="glossary">
          {GLOSSARY.map((g) => (
            <>
              <dt>{g.term}</dt>
              <dd>{g.def}</dd>
            </>
          ))}
        </dl>
      </section>
      <section class="card" aria-labelledby="h-ctl">
        <h2 id="h-ctl">Filters and controls</h2>
        <p class="muted">Dropdowns list every value the server accepts. The page address keeps the filters, so you can share a link to a filtered view.</p>
        <dl class="glossary">
          {CONTROLS.map((k) => (
            <>
              <dt>{FIELD_HELP[k]!.label}</dt>
              <dd>{FIELD_HELP[k]!.help}</dd>
            </>
          ))}
        </dl>
      </section>
    </>
  );
}
