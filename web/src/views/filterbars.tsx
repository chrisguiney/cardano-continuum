import { FIELD_HELP } from "../lib/help";
import { optionsOf, type Enums } from "../lib/meta";
import { PAGE_SIZES, SORT_KEYS, type FilterName, type PageState, type SortKey } from "../lib/paging";
import type { FindingFilters } from "../lib/findings";
import { SelectField, TextField } from "./fields";

const H = FIELD_HELP as Record<string, { label: string; help: string }>;
const EMPTY: Enums = { actor_kind: [], agenda: [], state: [], intent_kind: [], finding_kind: [], severity: [], impl: [], tier: [], phase: [], attack: [], decision_kind: [] };

const DELEGATED = [{ value: "true", label: "Delegated" }, { value: "false", label: "Not delegated" }];
const ORDER = [{ value: "asc", label: "Ascending" }, { value: "desc", label: "Descending" }];

interface ActorBarProps {
  page: PageState;
  enums: Enums | null;
  profiles: Record<string, number>;
  states: Record<string, number>;
  onFilter: (n: FilterName, v: string) => void;
  onLimit: (n: number) => void;
  onSort: (k: SortKey, desc: boolean) => void;
}

export function ActorFilters({ page, enums, profiles, states, onFilter, onLimit, onSort }: ActorBarProps) {
  const e = enums ?? EMPTY;
  return (
    <>
      <SelectField id="f-profile" help={H.profile!} value={page.profile} options={optionsOf(Object.keys(profiles).sort(), profiles)} onChange={(v) => onFilter("profile", v)} />
      <SelectField id="f-kind" help={H.kind!} value={page.kind} options={optionsOf(e.actor_kind)} onChange={(v) => onFilter("kind", v)} />
      <SelectField id="f-state" help={H.state!} value={page.state} options={optionsOf(e.state, states)} onChange={(v) => onFilter("state", v)} />
      <SelectField id="f-delegated" help={H.delegated!} value={page.delegated} allLabel="Any" options={DELEGATED} onChange={(v) => onFilter("delegated", v)} />
      <SelectField id="f-sort" help={H.sort!} value={page.sort} allLabel={null} options={SORT_KEYS.map((k) => ({ value: k, label: k }))} onChange={(v) => onSort(v as SortKey, page.desc)} />
      <SelectField id="f-dir" help={H.dir!} value={page.desc ? "desc" : "asc"} allLabel={null} options={ORDER} onChange={(v) => onSort(page.sort, v === "desc")} />
      <SelectField id="f-limit" help={H.limit!} value={String(page.limit)} allLabel={null} options={PAGE_SIZES.map((n) => ({ value: String(n), label: String(n) }))} onChange={(v) => onLimit(Number(v))} />
    </>
  );
}

const KNOWN = [{ value: "false", label: "New" }, { value: "true", label: "Known issue" }];

export function FindingFilterBar({ f, enums, onChange }: { f: FindingFilters; enums: Enums | null; onChange: (k: keyof FindingFilters, v: string) => void }) {
  const e = enums ?? EMPTY;
  return (
    <>
      <SelectField id="f-known" help={H.known!} value={f.known} allLabel="All" options={KNOWN} onChange={(v) => onChange("known", v)} />
      <SelectField id="f-severity" help={H.severity!} value={f.severity} options={optionsOf(e.severity)} onChange={(v) => onChange("severity", v)} />
      <SelectField id="f-fkind" help={H.finding_kind!} value={f.kind} options={optionsOf(e.finding_kind)} onChange={(v) => onChange("kind", v)} />
    </>
  );
}

export interface DecisionFilterState {
  actor: string;
  profile: string;
  kind: string;
  intent: string;
}

export function DecisionFilterBar({ f, enums, profiles, onChange }: { f: DecisionFilterState; enums: Enums | null; profiles: Record<string, number>; onChange: (k: keyof DecisionFilterState, v: string) => void }) {
  const e = enums ?? EMPTY;
  return (
    <>
      <TextField id="f-actor" help={H.actor!} value={f.actor} placeholder="any" inputMode="numeric" onChange={(v) => onChange("actor", v)} />
      <SelectField id="f-dprofile" help={H.profile!} value={f.profile} options={optionsOf(Object.keys(profiles).sort())} onChange={(v) => onChange("profile", v)} />
      <SelectField id="f-dkind" help={H.decision_kind!} value={f.kind} options={optionsOf(e.decision_kind)} onChange={(v) => onChange("kind", v)} />
      <SelectField id="f-intent" help={H.intent!} value={f.intent} options={optionsOf(e.intent_kind)} onChange={(v) => onChange("intent", v)} />
    </>
  );
}

export interface PoolFilterState {
  impl: string;
  tier: string;
  phase: string;
}

export function PoolFilterBar({ f, enums, onChange }: { f: PoolFilterState; enums: Enums | null; onChange: (k: keyof PoolFilterState, v: string) => void }) {
  const e = enums ?? EMPTY;
  return (
    <>
      <SelectField id="f-impl" help={H.impl!} value={f.impl} options={optionsOf(e.impl)} onChange={(v) => onChange("impl", v)} />
      <SelectField id="f-tier" help={H.tier!} value={f.tier} options={optionsOf(e.tier)} onChange={(v) => onChange("tier", v)} />
      <SelectField id="f-phase" help={H.phase!} value={f.phase} options={optionsOf(e.phase)} onChange={(v) => onChange("phase", v)} />
    </>
  );
}

export interface AdversaryFilterState {
  attack: string;
  agenda: string;
}

export function AdversaryFilterBar({ f, enums, onChange }: { f: AdversaryFilterState; enums: Enums | null; onChange: (k: keyof AdversaryFilterState, v: string) => void }) {
  const e = enums ?? EMPTY;
  return (
    <>
      <SelectField id="f-attack" help={H.attack!} value={f.attack} options={optionsOf(e.attack)} onChange={(v) => onChange("attack", v)} />
      <SelectField id="f-agenda" help={H.agenda!} value={f.agenda} options={optionsOf(e.agenda)} onChange={(v) => onChange("agenda", v)} />
    </>
  );
}
