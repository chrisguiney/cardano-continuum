/** Plain-language help for every control, view and piece of jargon in the UI. */

export interface FieldHelp {
  label: string;
  help: string;
}

export const FIELD_HELP: Record<string, FieldHelp> = {
  profile: { label: "Profile", help: "The recipe an actor was created from (for example hodl or yield). A profile sets how often the actor acts and what it prefers to do. Profiles are loaded from the run's configuration." },
  state: { label: "State", help: "Lifecycle state of the actor. active: acting now. dormant: paused, may return. abandoned: gone for good. Other values (registered, retiring, retired, voting, expired, deployed, used) apply to pools, voters and contracts." },
  kind: { label: "Kind", help: "What sort of participant the actor is: wallet (an ordinary user), pool (a stake pool operator), drep (a delegated voter), contract (a script on chain) or faucet (a funding source)." },
  delegated: { label: "Delegated", help: "Whether the actor has pointed its stake at a pool. Delegating lets a pool use the stake to make blocks; the actor keeps its coins." },
  agenda: { label: "Agenda", help: "The goal that drives an actor's choices, such as hodl (hold), dapp (use apps), yield (earn rewards), civic (vote) or extract and disrupt (adversarial)." },
  severity: { label: "Severity", help: "How serious a finding is. warn: suspicious only. error: a broken rule the run survives. fatal: results can no longer be trusted, or the chain cannot recover." },
  finding_kind: { label: "Finding kind", help: "What the checker found, for example verdict_split (two node programs disagree on a transaction) or intent_paid_twice (a payment was made two times)." },
  known: { label: "Status", help: "New: not yet explained. Known: matches an upstream issue that is already filed, so it is recorded but not shown as new. All: both." },
  impl: { label: "Implementation", help: "Which node program the node runs: dingo, cardano-node, amaru or rogue (a deliberately misbehaving test node)." },
  tier: { label: "Tier", help: "How much of a node is really run. logical: nothing runs, the stake pool is only its on-chain registration. validating: a relay that checks blocks. forging: a block producer holding a stake pool's signing keys." },
  phase: { label: "Phase", help: "Where the node is now. Pending: starting. Syncing: catching up. Forging: making blocks. Following: caught up, never forges. Dark: switched off on purpose." },
  attack: { label: "Attack", help: "The kind of attack an adversary actor runs against the devnet, such as dust-flood (many tiny outputs) or double-spend (two payments from the same coins)." },
  intent: { label: "Intent", help: "What an actor decided to do, such as send, delegate, vote or mint. Choosing an intent is a decision; it may or may not turn into a transaction." },
  decision_kind: { label: "Event type", help: "The type of record in the decision feed: IntentChosen (an actor picked an action), Submitted (a transaction was sent), Confirmed (it landed on chain), Refused, GaveUp and others." },
  voter_class: { label: "Voter", help: "Who cast a governance vote: drep (a delegated representative, voting with the stake delegated to it), spo (a stake pool operator, voting with its pool's stake) or committee (a constitutional committee member, one vote each)." },
  action_type: { label: "Action type", help: "What a governance proposal asks for, such as info (a poll that changes nothing), parameter_change, treasury_withdrawals, no_confidence or hard_fork_initiation. Stake pools may vote only on some types." },
  score: { label: "Score", help: "The agenda's raw preference for this option, compared with idle's fixed 1. It is not a probability: an option scoring 3 is three times as attractive as doing nothing. 0 means the option is not possible right now." },
  weight: { label: "Weight", help: "A multiplier on the score, from the actor's profile or a Controls override. 1 when nothing is set, 0 turns the option off, 2 doubles it." },
  chance: { label: "Chance", help: "The probability this decision picked the option: score x weight, divided by the total over every option listed. For a pool or DRep it is among the options of the intent kind whose fitted clock fired, not among everything the actor could do." },
  zero_score: { label: "Not possible", help: "This option could not be taken at the moment the decision was made, so its chance is zero. The reason is in the Why column." },
  actor: { label: "Actor id", help: "Show only decisions from the actor with this number. Leave empty for all actors." },
  idle: { label: "Idle", help: "Idle decisions (an actor woke and chose to do nothing) outnumber everything else. group: each run of them between two other decisions is one row with a count. hide: leave them out. show each: one row per idle decision." },
  pause: { label: "Pause", help: "Stop the feed from scrolling so you can read it. New decisions are still received and counted, and appear when you resume." },
  limit: { label: "Rows per page", help: "How many actors to load at a time." },
  sort: { label: "Sort by", help: "The column the table is ordered by. Clicking a column heading does the same." },
  dir: { label: "Order", help: "Ascending counts up (a to z, small to large); descending counts down." },
  k: { label: "k (1 to 1000)", help: "Amplification factor: a multiplier on how fast actors act. 1 is the normal rate." },
  weights: { label: "Weights", help: "How much the simulated wallets want each intent, against their profile. Leave a field empty to keep the profile's own weight; 0 turns that intent off, 2 doubles it. Set weights replaces any earlier override as a whole; Reset weights to profile drops it. A current tag shows the override the engine has in force. Pools and DReps act on their own fitted clocks and ignore weights." },
  scenario: { label: "Scenario", help: "A scripted event to start now, such as taking a pool offline or an upgrade wave. The table below says what each does. It is added to the event log, so the run stays replayable." },
  search: { label: "Search", help: "Find a block by hash, height or slot:N, a transaction by hash, an address (addr_test1...), a stake pool by its 56-digit or pool1 id, a node by its name, or an actor as actor:N. The matching view opens." },
  style: { label: "Style", help: "The look of the page. Continuum is the original palette; shadcn/ui is the stock shadcn/ui theme. It is remembered in this browser." },
};

export const VIEW_BLURB: Record<string, string> = {
  overview: "Live summary of the simulated chain and the population of simulated users.",
  actors: "Every simulated participant: who they are, what they hold and what they are doing.",
  actor: "One participant in full: wallet, holdings, delegation, behaviour, findings and timeline.",
  pools: "The stake pools registered on the chain: who operates each, its terms, delegators, blocks and votes, and which node (if any) runs its keys.",
  nodes: "The node processes the run manages: which program and tier each runs, whether they agree on the tip, and which stake pool's keys a forging node holds.",
  governance: "Governance proposals on the chain, how DReps, stake pools and the committee voted on each, and a live feed of vote decisions.",
  decisions: "Live feed of what actors decide to do, with the scores that explain each choice.",
  findings: "Disagreements and rule breaks the checkers found between node programs and expected results.",
  adversaries: "Simulated attackers: what they try, what it costs them and how honest actors are affected.",
  controls: "Change the running simulation. Every action is logged and asks for confirmation.",
  explorer: "Browse the chain the engine follows: blocks, transactions, addresses and epochs, with the run's own context beside each one.",
  help: "What the words and controls in this app mean.",
};

export interface Term {
  term: string;
  def: string;
}

export const GLOSSARY: Term[] = [
  { term: "Actor", def: "A simulated participant, such as a wallet user, a pool operator or a voter." },
  { term: "Finding", def: "A problem the checkers noticed, such as two node programs giving different answers to the same transaction." },
  { term: "Verdict", def: "A node's answer to a submitted transaction: accepted, or rejected with a reason." },
  { term: "Intent", def: "Something an actor decides to try, such as send, delegate or vote." },
  { term: "Score", def: FIELD_HELP.score!.help },
  { term: "Weight", def: FIELD_HELP.weight!.help },
  { term: "Chance", def: FIELD_HELP.chance!.help },
  { term: "Agenda", def: "The goal that shapes which intents an actor prefers." },
  { term: "Profile", def: "A recipe for creating actors of one type, with their rates and preferences." },
  { term: "Cluster", def: "A group of actors made from the same profile." },
  { term: "Node", def: "A running node program (dingo, cardano-node, amaru or rogue) that the run manages. A node follows the chain; a forging node also holds one stake pool's keys and makes its blocks. The Nodes page lists them." },
  { term: "Stake pool", def: "A registration on the chain that stake is delegated to and that makes blocks with its keys. It has an operator, pledge, margin and cost. A stake pool may have a node running its keys, or none (it exists only on chain). The Stake pools page lists them." },
  { term: "Tier", def: "How much of a node is really run. logical: nothing runs; the stake pool exists only as its on-chain registration. validating: a relay node that checks blocks and holds no keys. forging: a block producer holding a stake pool's signing keys." },
  { term: "Logical, validating, forging", def: "The three tiers. Only a forging node runs a stake pool's keys, so only it links to a stake pool; a logical pool shows as no node (logical)." },
  { term: "Phase", def: "A node's current stage: Pending, Syncing, Forging, Following or Dark." },
  { term: "Known issue", def: "A finding already explained by a filed upstream bug. It is kept but not counted as new." },
  { term: "k", def: "The amplification factor on the Controls page: a multiplier on how fast actors act. Also the name of the chain's security parameter, the number of blocks after which history is treated as final." },
  { term: "Slot", def: "One tick of the chain's clock. A block may be made in a slot." },
  { term: "Epoch", def: "A fixed run of slots after which stake and rewards are recalculated." },
  { term: "Tip", def: "The newest block a node knows. If nodes disagree on it, the chain has forked." },
  { term: "Fork", def: "Two groups of nodes follow different chains." },
  { term: "Delegation", def: "Pointing your stake at a pool so it can make blocks for you; you keep your ADA." },
  { term: "Lovelace", def: "The smallest unit of ADA: one ADA is 1,000,000 lovelace." },
  { term: "Block", def: "A batch of transactions the chain adds in one slot. Each block names the block before it, so blocks form a chain; a block is issued by one pool." },
  { term: "Transaction", def: "A signed instruction that spends outputs and creates new ones, and may also mint tokens, register stake or vote." },
  { term: "UTxO", def: "An unspent transaction output: a coin amount (and maybe tokens) locked to an address. A transaction consumes whole UTxOs and creates new ones; an address's balance is the sum of its UTxOs." },
  { term: "Datum", def: "Data attached to an output for a script to read when the output is spent. It is either stored inline or referred to by its hash." },
  { term: "Redeemer", def: "The argument a transaction gives a script when it spends, mints or votes, together with the execution budget (ExUnits) the script may use." },
  { term: "ExUnits", def: "The execution budget of a script: memory and CPU steps. A block has a limit on the total its transactions may use; the Explorer shows the share used." },
  { term: "Certificate", def: "A statement in a transaction that changes chain state: registering or delegating a stake key, registering or retiring a pool, or registering and delegating to a DRep." },
  { term: "Collateral", def: "Coins a transaction with scripts puts at risk. If a script fails after the transaction is in a block (isValid false), the collateral is spent as the fee and nothing else happens." },
  { term: "Adversary", def: "An actor built to attack the network, to check that honest users stay protected." },
  { term: "Control run", def: "A second run without attacks, used to measure what the attacks changed." },
];
