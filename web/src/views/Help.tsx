import { ADV_GLOSSARY } from "../lib/advhelp";
import { GOV_GLOSSARY } from "../lib/govhelp";
import { FIELD_HELP, GLOSSARY, VIEW_BLURB, type Term } from "../lib/help";
import { Facts, Heading, Note, Panel, Title } from "./common";
import { Blurb } from "./fields";

const VIEWS: [string, string][] = [
  ["Overview", "overview"], ["Actors", "actors"], ["Pools", "pools"], ["Explorer", "explorer"], ["Governance", "governance"], ["Decisions", "decisions"],
  ["Findings", "findings"], ["Adversaries", "adversaries"], ["Controls", "controls"],
];

const CONTROLS = ["profile", "state", "kind", "delegated", "limit", "sort", "dir", "known", "severity", "finding_kind", "impl", "tier", "phase", "attack", "agenda", "intent", "decision_kind", "voter_class", "action_type", "actor", "search", "theme"];

function Glossary({ id, title, terms }: { id: string; title: string; terms: Term[] }) {
  return (
    <Panel aria-labelledby={id}>
      <Heading id={id}>{title}</Heading>
      <Facts rows={terms.map((g) => [g.term, g.def])} />
    </Panel>
  );
}

/** Help defines the jargon and lists what each filter and control does. */
export function Help() {
  return (
    <>
      <Title>Help</Title>
      <Blurb text={VIEW_BLURB.help!} />
      <Panel aria-labelledby="h-views">
        <Heading id="h-views">Views</Heading>
        <Facts rows={VIEWS.map(([name, key]) => [name, VIEW_BLURB[key]])} />
      </Panel>
      <Glossary id="h-gloss" title="Glossary" terms={GLOSSARY} />
      <Glossary id="h-gov" title="Governance" terms={GOV_GLOSSARY} />
      <Glossary id="h-adv" title="Adversaries" terms={ADV_GLOSSARY} />
      <Panel aria-labelledby="h-ctl">
        <Heading id="h-ctl">Filters and controls</Heading>
        <Note>Dropdowns list every value the server accepts. The page address keeps the filters, so you can share a link to a filtered view.</Note>
        <Facts rows={CONTROLS.map((k) => [FIELD_HELP[k]!.label, FIELD_HELP[k]!.help])} />
      </Panel>
    </>
  );
}
