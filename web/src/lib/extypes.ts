/** Shapes of the /api/explorer responses. */

export interface PoolRef {
  name: string;
  impl: string;
}

export interface Limits {
  max_mem: number;
  max_steps: number;
  max_bytes: number;
}

export interface BlockRow {
  height: number;
  slot: number;
  epoch: number;
  time_ms: number;
  hash: string;
  prev: string;
  issuer: string;
  pool?: PoolRef;
  txs: number;
  size: number;
  fees: number;
  ex_mem: number;
  ex_steps: number;
}

export interface BlockPage {
  total: number;
  offset: number;
  limit: number;
  rows: BlockRow[];
  limits: Limits;
}

export interface Header {
  proto_major: number;
  proto_minor: number;
  body_hash: string;
  body_size: number;
  issuer_vkey: string;
  vrf_key: string;
  opcert_hot_vkey: string;
  opcert_seq: number;
  kes_period: number;
}

export interface Placed {
  pool: string;
  state: "on" | "fork" | "behind" | "unknown";
  on_block: boolean;
  hash?: string;
}

export interface BlockDetail extends Omit<BlockRow, "txs"> {
  header: Header;
  txs: TxRow[];
  next?: string;
  evicted: boolean;
  stub: boolean;
  pools: Placed[];
  fork: boolean;
}

export interface TxRow {
  hash: string;
  fee: number;
}

export interface Asset {
  policy: string;
  name: string;
  name_text?: string;
  quantity: string;
}

export interface Datum {
  kind: "hash" | "inline";
  hash: string;
  hex?: string;
}

export interface Script {
  hash: string;
  language: string;
  size: number;
}

export interface Output {
  tx?: string;
  index: number;
  address: string;
  lovelace: number;
  assets?: Asset[];
  datum?: Datum;
  script_ref?: Script;
}

export interface Input {
  tx: string;
  index: number;
  resolved?: Output;
}

export interface Cert {
  kind: string;
  credential?: string;
  pool?: string;
  drep?: string;
  amount?: number;
  epoch?: number;
  anchor?: string;
}

export interface Redeemer {
  tag: string;
  index: number;
  mem: number;
  steps: number;
  data_hex?: string;
}

export interface Vote {
  voter: string;
  voter_hash: string;
  action_tx: string;
  action_index: number;
  choice: string;
}

export interface Proposal {
  index: number;
  kind: string;
  deposit: number;
  return_address: string;
  anchor?: string;
}

export interface ActorRef {
  id: number;
  name: string;
  role: string;
}

export interface TxFinding {
  id: string;
  kind: string;
  severity: string;
  summary: string;
}

export interface TxContext {
  actor?: ActorRef;
  intent: number;
  kind?: string;
  amount: number;
  attack?: string;
  verdict?: string;
  reason?: string;
  findings: TxFinding[];
}

export interface TxDetail {
  hash: string;
  block: string;
  height: number;
  slot: number;
  index: number;
  evicted: boolean;
  valid: boolean;
  size: number;
  fee: number;
  ttl?: number;
  valid_from?: number;
  inputs: Input[];
  outputs: Output[];
  reference_inputs: Input[];
  collateral: Input[];
  collateral_return?: Output;
  total_collateral?: number;
  mint: Asset[];
  certificates: Cert[];
  withdrawals: { address: string; lovelace: number }[];
  required_signers: string[];
  metadata?: Record<string, unknown>;
  redeemers: Redeemer[];
  scripts: Script[];
  votes: Vote[];
  proposals: Proposal[];
  cbor_hex?: string;
  continuum?: TxContext;
}

export interface TxRef {
  hash: string;
  block: string;
  height: number;
  slot: number;
}

export interface AddressDetail {
  address: string;
  stake?: string;
  balance: number;
  assets: Asset[];
  utxos: Output[];
  utxo_total: number;
  tx_count: number;
  history: TxRef[];
  offset: number;
  limit: number;
  actor?: ActorRef;
  stake_actor?: ActorRef;
}

export interface EpochPool {
  pool: string;
  resolved?: PoolRef;
  blocks: number;
  expected: number;
}

export interface EpochView {
  epoch: number;
  blocks: number;
  txs: number;
  fees: number;
  size: number;
  first_height: number;
  last_height: number;
  pools: EpochPool[];
}

export interface SearchResult {
  kind: string;
  id: string;
  route: string;
}
