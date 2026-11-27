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
  impl: { label: "Implementation", help: "Which node program the pool runs: dingo, cardano-node, amaru or rogue (a deliberately misbehaving test node)." },
  tier: { label: "Tier", help: "How much of a pool is really run. logical: only its on-chain registration. validating: a relay that checks blocks. forging: a block producer with signing keys." },
  phase: { label: "Phase", help: "Where the pool is now. Pending: starting. Syncing: catching up. Forging: making blocks. Following: caught up, never forges. Dark: switched off on purpose." },
  attack: { label: "Attack", help: "The kind of attack an adversary actor runs against the devnet, such as dust-flood (many tiny outputs) or double-spend (two payments from the same coins)." },
  intent: { label: "Intent", help: "What an actor decided to do, such as send, delegate, vote or mint. Choosing an intent is a decision; it may or may not turn into a transaction." },
  decision_kind: { label: "Event type", help: "The type of record in the decision feed: IntentChosen (an actor picked an action), Submitted (a transaction was sent), Confirmed (it landed on chain), Refused, GaveUp and others." },
  voter_class: { label: "Voter", help: "Who cast a governance vote: drep (a delegated representative, voting with the stake delegated to it), spo (a stake pool operator, voting with its pool's stake) or committee (a constitutional committee member, one vote each)." },
  action_type: { label: "Action type", help: "What a governance proposal asks for, such as info (a poll that changes nothing), parameter_change, treasury_withdrawals, no_confidence or hard_fork_initiation. Stake pools may vote only on some types." },
  actor: { label: "Actor id", help: "Show only decisions from the actor with this number. Leave empty for all actors." },
  idle: { label: "Idle", help: "Idle decisions (an actor woke and chose to do nothing) outnumber everything else. group: each run of them between two other decisions is one row with a count. hide: leave them out. show each: one row per idle decision." },
  pause: { label: "Pause", help: "Stop the feed from scrolling so you can read it. New decisions are still received and counted, and appear when you resume." },
  limit: { label: "Rows per page", help: "How many actors to load at a time." },
  sort: { label: "Sort by", help: "The column the table is ordered by. Clicking a column heading does the same." },
  dir: { label: "Order", help: "Ascending counts up (a to z, small to large); descending counts down." },
  k: { label: "k (1 to 1000)", help: "Amplification factor: a multiplier on how fast actors act. 1 is the normal rate." },
  weights: { label: "Weights", help: "Overrides how much actors want each intent, as name=value pairs such as send=2, vote=0.5. A higher value makes that intent more likely; 0 turns it off." },
  scenario: { label: "Scenario", help: "Name of a scripted event to start now, for example a network partition. It is added to the event log, so the run stays replayable." },
  search: { label: "Search", help: "Find a block by hash, height or slot:N, a transaction by hash, an address (addr_test1...), a pool by its 56-digit id or name, or an actor as actor:N. The matching view opens." },
  theme: { label: "Theme", help: "Switch between the light and dark colour schemes. Your choice is remembered in this browser." },
};

export const VIEW_BLURB: Record<string, string> = {
  overview: "Live summary of the simulated chain and the population of simulated users.",
  actors: "Every simulated participant: who they are, what they hold and what they are doing.",
  actor: "One participant in full: wallet, holdings, delegation, behaviour, findings and timeline.",
  pools: "The stake pools running the chain, which node program each runs, and whether they agree on the tip.",
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
  { term: "Agenda", def: "The goal that shapes which intents an actor prefers." },
  { term: "Profile", def: "A recipe for creating actors of one type, with their rates and preferences." },
  { term: "Cluster", def: "A group of actors made from the same profile." },
  { term: "Tier", def: "How much of a pool is really run: logical, validating or forging." },
  { term: "Phase", def: "The pool's current stage: Pending, Syncing, Forging, Following or Dark." },
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
