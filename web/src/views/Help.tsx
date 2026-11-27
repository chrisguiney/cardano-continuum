import { Fragment } from "react";
import { ADV_GLOSSARY } from "../lib/advhelp";
import { GOV_GLOSSARY } from "../lib/govhelp";
import { FIELD_HELP, GLOSSARY, VIEW_BLURB, type Term } from "../lib/help";
import { Blurb } from "./fields";

const VIEWS: [string, string][] = [
  ["Overview", "overview"], ["Actors", "actors"], ["Pools", "pools"], ["Explorer", "explorer"], ["Governance", "governance"], ["Decisions", "decisions"],
  ["Findings", "findings"], ["Adversaries", "adversaries"], ["Controls", "controls"],
];

const CONTROLS = ["profile", "state", "kind", "delegated", "limit", "sort", "dir", "known", "severity", "finding_kind", "impl", "tier", "phase", "attack", "agenda", "intent", "decision_kind", "voter_class", "action_type", "actor", "search", "theme"];

function Glossary({ id, title, terms }: { id: string; title: string; terms: Term[] }) {
  return (
    <section className="card" aria-labelledby={id}>
      <h2 id={id}>{title}</h2>
      <dl className="glossary">
        {terms.map((g) => (
          <Fragment key={g.term}>
            <dt>{g.term}</dt>
            <dd>{g.def}</dd>
          </Fragment>
        ))}
      </dl>
    </section>
  );
}

/** Help defines the jargon and lists what each filter and control does. */
export function Help() {
  return (
    <>
      <h1>Help</h1>
      <Blurb text={VIEW_BLURB.help!} />
      <section className="card" aria-labelledby="h-views">
        <h2 id="h-views">Views</h2>
        <dl className="glossary">
          {VIEWS.map(([name, key]) => (
            <Fragment key={name}>
              <dt>{name}</dt>
              <dd>{VIEW_BLURB[key]}</dd>
            </Fragment>
          ))}
        </dl>
      </section>
      <section className="card" aria-labelledby="h-gloss">
        <h2 id="h-gloss">Glossary</h2>
        <dl className="glossary">
          {GLOSSARY.map((g) => (
            <Fragment key={g.term}>
              <dt>{g.term}</dt>
              <dd>{g.def}</dd>
            </Fragment>
          ))}
        </dl>
      </section>
      <Glossary id="h-gov" title="Governance" terms={GOV_GLOSSARY} />
      <Glossary id="h-adv" title="Adversaries" terms={ADV_GLOSSARY} />
      <section className="card" aria-labelledby="h-ctl">
        <h2 id="h-ctl">Filters and controls</h2>
        <p className="muted">Dropdowns list every value the server accepts. The page address keeps the filters, so you can share a link to a filtered view.</p>
        <dl className="glossary">
          {CONTROLS.map((k) => (
            <Fragment key={k}>
              <dt>{FIELD_HELP[k]!.label}</dt>
              <dd>{FIELD_HELP[k]!.help}</dd>
            </Fragment>
          ))}
        </dl>
      </section>
    </>
  );
}
