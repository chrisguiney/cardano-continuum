// Shapes of the JSON served by internal/httpapi (types.go).

export interface Point {
  slot: number;
  hash: string;
  height?: number;
}
export interface Invariant {
  name: string;
  ok: boolean;
  detail?: string;
}
export interface Snapshot {
  last_seq: number;
  sim_time_ms: number;
  tip: Point | null;
  blocks: number;
  rollbacks: number;
  epoch: number | null;
  tx_per_sec: number;
  submit_per_sec: number;
  ada: { fees_lovelace: number; submitted_lovelace: number; confirmed_lovelace: number };
  txs: { submitted: number; confirmed: number; retries: number; refused: number; released: number; gave_up: number; skipped: number };
  population: { total: number; by_profile: Record<string, number>; by_state: Record<string, number> };
  invariants: Invariant[];
}
export interface ActorRow {
  id: number;
  name: string;
  role: string;
  profile: string;
  state: string;
  intents: number;
  txs: number;
  retries: number;
  confirmed: number;
  released: number;
  kind: string;
  address: string;
  balance: number;
  pending: number;
  tokens: number;
  delegated: boolean;
  pool: string;
  drep: string;
}
export interface ActorPage {
  total: number;
  offset: number;
  limit: number;
  rows: ActorRow[];
}
export interface TimelineEntry {
  seq: number;
  at: number;
  kind: string;
  detail?: string;
  intent?: number;
}
export interface Timeline {
  actor: number;
  profile: string;
  role: string;
  state: string;
  born_ms: number;
  dropped: number;
  entries: TimelineEntry[];
}
export interface PoolRow {
  id: string;
  pool_id: string;
  impl: string;
  tier: string;
  phase: string;
  dark: boolean;
  tip: Point;
  image?: string;
  fork: boolean;
  stake: number | null;
  blocks_made: number | null;
  blocks_expected: number | null;
}
export interface ImplTip {
  impl: string;
  slot: number;
  hash: string;
  height?: number;
  pools: number;
}
export interface Fork {
  slot: number;
  height?: number;
  depth?: number;
  reason?: "diverged" | "behind";
  groups: { hash: string; pools: string[] }[];
}
export interface TopoNode {
  id: string;
  impl: string;
  tier: string;
  phase: string;
  fork: boolean;
}
export interface Edge {
  from: string;
  to: string;
}
export interface PoolsView {
  pools: PoolRow[];
  tips: ImplTip[];
  forks: Fork[];
  topology: { mode: string; nodes: TopoNode[]; edges: Edge[] };
}
export interface ImplVerdict {
  impl: string;
  verdict: string;
  build?: string;
  dirty: boolean;
}
export interface Finding {
  id: string;
  seq: number;
  kind: string;
  severity: string;
  summary: string;
  tx_cbor_hex: string;
  verdicts: ImplVerdict[];
  reproducible: boolean;
  provenance?: string;
  replay_command?: string;
  // known is the upstream issue (repo#number) that explains the finding.
  known?: string;
}
export interface FindingPage {
  total: number;
  offset: number;
  limit: number;
  rows: Finding[];
  known_total?: number;
  known_counts?: Record<string, number>;
}
export interface Score {
  kind: string;
  value: number;
  target?: number;
  proposal?: string;
  why: string;
}
export interface Decision {
  id: number;
  at: number;
  kind: string;
  actor: number;
  profile?: string;
  intent?: string;
  scores?: Score[];
  body?: unknown;
}
export interface Receipt {
  seq: number;
  duplicate: boolean;
}

export interface Token {
  policy: string;
  name: string;
  name_text?: string;
  quantity: number;
}
export interface ActorFinding {
  id: string;
  seq: number;
  kind: string;
  severity: string;
  summary: string;
}
export interface ActorDetail {
  id: number;
  name: string;
  profile: string;
  kind: string;
  agenda: string;
  has_identity: boolean;
  wallet: { payment_addr: string; stake_addr: string; payment_key_hash: string; stake_key_hash: string; drep_id?: string; pool_id?: string };
  holdings: { settled_lovelace: number; reserved_lovelace: number; unconfirmed_lovelace: number; utxos: number; in_flight: number; tokens: Token[] };
  delegation: { stake_registered: boolean; pool?: string; pool_actor?: number; drep?: string };
  lifecycle: { state: string; born_ms: number; changed_ms: number; frustration: number; hazard: number };
  traits: { competence: number; care: number; patience: number; learning_rate: number };
  activity: {
    intents: number; submitted: number; confirmed: number; refused: number; released: number; gave_up: number; retries: number;
    last_ms: number; lost_lovelace: number; recent: Decision[];
  };
  findings: ActorFinding[];
  adversary?: { spent: number; ceiling: number; metric: string };
  timeline: TimelineEntry[];
}
export interface AdversaryCost {
  spent: number;
  ceiling: number;
  epoch: number;
  epoch_spent: number;
  by_epoch: { epoch: number; spent: number }[];
}
export interface AdversaryRow {
  id: number;
  name: string;
  profile: string;
  attack: string;
  agenda: string;
  budget: number;
  cost: AdversaryCost;
  metric: { name: string; value: number; control: number | null };
  attempted: number;
  landed: number;
  detected: number;
  rejected: number;
  recent: { seq: number; at: number; intent: number; mutation?: string; cost: number; value?: number; txs: number }[];
  findings: ActorFinding[];
}
export interface ImpactRow {
  metric: string;
  value: number;
  control: number | null;
  delta: number | null;
}
export interface AdversaryView {
  has_control: boolean;
  adversaries: AdversaryRow[];
  impact: ImpactRow[];
}
