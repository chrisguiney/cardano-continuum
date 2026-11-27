import { useEffect, useState } from "react";
import { getJSON } from "./api";

/** Enums mirrors GET /api/meta/enums: the value sets the server validates against. */
export interface Enums {
  actor_kind: string[];
  agenda: string[];
  state: string[];
  intent_kind: string[];
  finding_kind: string[];
  severity: string[];
  impl: string[];
  tier: string[];
  phase: string[];
  attack: string[];
  decision_kind: string[];
  voter_class: string[];
  action_type: string[];
  /** weight is the intent kinds a set_weights control overrides. */
  weight: string[];
  scenarios: ScenarioInfo[];
}

/** ScenarioInfo is one scenario an inject control may name. */
export interface ScenarioInfo {
  name: string;
  /** arg describes the argument after the colon; absent when there is none. */
  arg?: string;
  optional?: boolean;
  example?: string;
  description: string;
}

export interface Option {
  value: string;
  label: string;
}

/** optionsOf builds dropdown options, appending "(n)" where counts has the value. */
export function optionsOf(values: string[], counts?: Record<string, number>): Option[] {
  return values.map((v) => ({ value: v, label: counts && counts[v] !== undefined ? `${v} (${counts[v]})` : v }));
}

let cached: Promise<Enums> | null = null;

/** useEnums fetches the enum lists once per page load; null until they arrive. */
export function useEnums(): Enums | null {
  const [e, setE] = useState<Enums | null>(null);
  useEffect(() => {
    cached ??= getJSON<Enums>("/api/meta/enums");
    cached.then(setE, () => {
      cached = null;
    });
  }, []);
  return e;
}
