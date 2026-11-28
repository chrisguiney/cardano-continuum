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
  /** protocol_version is the version in force at the tip; absent before the engine logs one. */
  protocol_version?: ProtocolVersion;
  /** pending_fork is a ratified hard fork waiting for the boundary that enacts it. */
  pending_fork?: PendingFork;
  /** scheduled_fork is the hard fork the run proposes by itself, until its version is in force. */
  scheduled_fork?: ScheduledFork;
}
/** ScheduledFork is the hard fork a run proposes by itself: its version and the first epoch it may be proposed in. */
export interface ScheduledFork {
  to: ProtocolVersion;
  epoch: number;
}
/** ProtocolVersion is a Cardano protocol version. */
export interface ProtocolVersion {
  major: number;
  minor: number;
}
/** PendingFork is a ratified hard fork: its proposal, the version it moves to and the first epoch at that version. */
export interface PendingFork {
  proposal: string;
  version: ProtocolVersion;
  at: number;
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
/** NodeRow is one node process the operator runs (a ContinuumPool object), from GET /api/nodes. */
export interface NodeRow {
  name: string;
  /** label is spec.poolId: shared by a forging node and its relays; a stake pool id only when it is 56 hex digits. */
  label: string;
  impl: string;
  tier: string;
  phase: string;
  dark: boolean;
  tip: Point;
  /** slot_lag is how many slots the tip trails the furthest node's tip. */
  slot_lag: number;
  image?: string;
  fork: boolean;
  /** last_block is the newest block the node's own keys made; null when none was seen. */
  last_block: Point | null;
  /** stake_pool is the pool id (hex) whose keys the node runs; absent for a node without keys or one that has not forged yet. */
  stake_pool?: string;
  /** forged counts the blocks on the selected chain, up to the node's tip, that its stake pool keys issued; null when the tip height or chain index is unknown. */
  forged: number | null;
  /** verified counts the other blocks on the chain up to the node's tip, validated and adopted from other producers; null as forged. */
  verified: number | null;
  findings: number;
  known_findings: number;
  /** rollbacks counts the times the node moved its selected chain back to a point behind its tip, as the engine's chain-sync follower saw it. */
  rollbacks: number;
  /** deepest_rollback is the rollback that dropped the most blocks; null when none. */
  deepest_rollback: RollbackRow | null;
  /** last_rollback is the newest rollback; null when none. */
  last_rollback: RollbackRow | null;
  /** recent_rollbacks are the newest rollbacks, newest first. */
  recent_rollbacks: RollbackRow[];
}
/** RollbackRow is one rollback of a node's selected chain. */
export interface RollbackRow {
  /** from is the tip the node held when it rolled back. */
  from: Point;
  /** to is the point it rolled back to; its height is 0 when beyond. */
  to: Point;
  blocks: number;
  slots: number;
  /** beyond reports the target older than the blocks the follower keeps, so blocks and slots are lower bounds. */
  beyond: boolean;
}
export interface ImplTip {
  impl: string;
  slot: number;
  hash: string;
  height?: number;
  nodes: number;
}
export interface Fork {
  slot: number;
  height?: number;
  depth?: number;
  reason?: "diverged" | "behind";
  groups: { hash: string; nodes: string[] }[];
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
export interface NodesView {
  nodes: NodeRow[];
  tips: ImplTip[];
  forks: Fork[];
  topology: { mode: string; nodes: TopoNode[]; edges: Edge[] };
}
/** PoolReg is a stake pool's latest registration certificate as the explorer read it. */
export interface PoolReg {
  pledge: number;
  cost: number;
  margin: number;
  reward_account: string;
  owners: number;
  metadata_url?: string;
  slot: number;
}
/** NodeRef names a node from a stake pool's row. */
export interface NodeRef {
  name: string;
  impl: string;
  tier: string;
  phase: string;
}
/** StakePoolRow is one on-chain stake pool, from GET /api/pools. */
export interface StakePoolRow {
  id: string;
  bech32: string;
  /** actor is the pool actor operating it; null when no actor does. */
  actor: number | null;
  actor_name?: string;
  profile?: string;
  actor_state?: string;
  /** registration is null for a genesis pool or when the server has no explorer. */
  registration: PoolReg | null;
  retire_epoch: number | null;
  delegators: number;
  /** delegated_lovelace sums the delegators' settled lovelace in the engine's planner ledger. */
  delegated_lovelace: number;
  /** blocks is null when the server has no explorer. */
  blocks: number | null;
  votes: number;
  /** nodes run the pool's keys; empty when the pool exists only on chain. */
  nodes: NodeRef[];
}
export interface StakePoolsView {
  pools: StakePoolRow[];
}
export interface ImplVerdict {
  impl: string;
  verdict: string;
  build?: string;
  dirty: boolean;
  /** detail is the node's own text for each failure it named. */
  detail?: string;
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
  // weight is the multiplier the draw applied to value; absent on older logs
  // and on scores that were not drawn from.
  weight?: number;
}
export interface Decision {
  id: number;
  at: number;
  kind: string;
  actor: number;
  profile?: string;
  intent?: string;
  scores?: Score[];
  // clock is the intent kind whose fitted clock fired, for pools and DReps.
  clock?: string;
  // position is the market position the scores read, for a market wallet.
  position?: Position;
  body?: unknown;
}
/** MarketHolding is a wallet's currencies, cost basis per ADA in its primary currency, and last exchange. */
export interface MarketHolding {
  primary: string;
  extra?: string[];
  basis: number;
  units: number;
  venue?: string;
}
/** Position is the market position a decision's scores read. */
export interface Position {
  currency: string;
  gain: number;
  trend: number;
}
/** MarketPoint is one Quote record: ADA/USD, each currency's mid, each venue's bid and ask by "exchange/currency". */
export interface MarketPoint {
  at: number;
  ada: number;
  mids: Record<string, number>;
  venues: Record<string, [number, number]>;
}
export interface MarketFlow {
  exchange: string;
  currency: string;
  side: string;
  trades: number;
  lovelace: number;
  fiat: number;
}
export interface Drawdown {
  peak_at: number;
  trough_at: number;
  high: number;
  low: number;
  depth: number;
  hodlers: number;
  sellers: number;
}
/** MarketsView is /api/markets. */
export interface MarketsView {
  /** seeded is the date of the FX and ADA snapshot the server's default rates came from. */
  seeded?: string;
  quotes: MarketPoint[] | null;
  flows: MarketFlow[] | null;
  births: Record<string, number>;
  drawdown: Drawdown;
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
  traits: { competence: number; care: number; patience: number; learning_rate: number; loss_aversion?: number };
  /** market is a market wallet's currencies and cost basis. */
  market?: MarketHolding;
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
export interface Tally {
  yes: number;
  no: number;
  abstain: number;
  yes_power: number;
  no_power: number;
  abstain_power: number;
}
export interface ProposalRow {
  id: string;
  type: string;
  proposer?: number;
  profile?: string;
  deposit_lovelace: number;
  slot: number;
  epoch?: number;
  expires_epoch?: number;
  status: string;
  spo_may_vote: boolean;
  tally: Record<string, Tally>;
  /** summary is one line on what the action asks for; empty when the explorer has not decoded it. */
  summary?: string;
}
export interface VoteRow {
  slot: number;
  at: number;
  tx: string;
  proposal: string;
  type: string;
  voter: string;
  voter_id: string;
  actor?: number;
  profile?: string;
  choice: string;
  power: number;
}
export interface PoolVotes {
  actor: number;
  profile?: string;
  votes: VoteRow[];
}
export interface GovernanceView {
  epoch?: number;
  lifetime_epochs: number;
  proposals: ProposalRow[];
  votes: VoteRow[];
  pools: PoolVotes[];
  /** protocol_version is the version in force at the tip; absent before the engine logs one. */
  protocol_version?: ProtocolVersion;
  /** pending_fork is a ratified hard fork waiting for the boundary that enacts it. */
  pending_fork?: PendingFork;
  /** scheduled_fork is the hard fork the run proposes by itself, until its version is in force. */
  scheduled_fork?: ScheduledFork;
  /** treasury is the treasury as the engine's chain view reckons it; absent before the engine reports one. */
  treasury?: TreasuryView;
}

/** TreasuryView is the treasury now and its recent moves, oldest first: enacted withdrawals paid out at a boundary, or a rollback of one. */
export interface TreasuryView {
  now_lovelace: number;
  moves: { epoch: number; before_lovelace: number; after_lovelace: number }[];
}

/** GovParam is one protocol parameter a parameter-change action sets. */
export interface GovParam {
  field: string;
  name: string;
  group: string;
  security: boolean;
  unit: string;
  desc: string;
  new: string;
  current?: string;
}
export interface GovAnchor {
  url: string;
  hash: string;
}
export interface GovRef {
  tx: string;
  index: number;
}
export interface GovActorRef {
  id: number;
  name: string;
  role: string;
}
export interface GovPayee {
  address: string;
  lovelace: number;
  actor?: GovActorRef;
}
export interface GovMember {
  credential: string;
  epoch?: number;
}
/** GovContent is the proposal procedure decoded; only the fields of its kind are set. */
export interface GovContent {
  tx: string;
  index: number;
  slot: number;
  kind: string;
  return_address: string;
  return_actor?: GovActorRef;
  deposit: number;
  anchor?: GovAnchor;
  prev_action?: GovRef;
  params?: GovParam[];
  policy_hash?: string;
  withdrawals?: GovPayee[];
  total?: number;
  removed?: string[];
  added?: GovMember[];
  quorum?: string;
  constitution?: { anchor: GovAnchor; script_hash?: string };
  version?: { major: number; minor: number };
}
export interface VoterRule {
  class: string;
  allowed: boolean;
  reason: string;
  rule: string;
}
export interface GovVote extends VoteRow {
  anchor?: GovAnchor;
}
/** GovDetail is /api/governance/{tx}/{index}. */
export interface GovDetail {
  id: string;
  epoch?: number;
  lifetime_epochs: number;
  proposal: ProposalRow;
  summary?: string;
  content?: GovContent;
  voters: VoterRule[];
  votes: GovVote[];
  /** treasury_lovelace is the treasury now, which a withdrawal draws on. */
  treasury_lovelace?: number;
}
