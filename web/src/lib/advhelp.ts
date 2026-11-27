import type { FieldHelp, Term } from "./help";

/**
 * Definitions for the Adversaries view, each saying what the number counts
 * and how continuum computes it from the run's log (engine meter.Measure and
 * the adversary package). "Honest" means an intent no adversary made.
 */
export const ADV_HELP: Record<string, FieldHelp> = {
  // The impact table's metrics.
  honest_intents: { label: "Honest intents", help: "Payments by honest actors that reached a node, each counted once at its first submission. An intent an adversary made, or a refill, is not honest." },
  honest_failure_rate: { label: "Honest failure rate", help: "The share of ended honest intents that failed. An intent ends when it confirms or is released (no attempt left and not confirmed: expired, conflicted or gave up); the rate is released over ended, 0 with none ended. Intents still in flight count in neither." },
  honest_retries: { label: "Retries", help: "Honest submissions after an intent's first: every further attempt at an intent already submitted counts once, whatever the reason." },
  honest_latency_p50_s: { label: "Honest latency, median", help: "The median seconds from an honest intent's first submission to its confirmation, over confirmed honest intents (nearest rank). 0 when none confirmed." },
  honest_latency_p99_s: { label: "Honest latency, 99th percentile", help: "The 99th-percentile seconds from an honest intent's first submission to its confirmation, over confirmed honest intents (nearest rank): how long the slowest few wait. 0 when none confirmed." },
  honest_value_grabbed_ada: { label: "Value grabbed", help: "ADA locked by honest actors at the shared script address that an adversary's attack spent. When a utxo-contention or front-run step confirms, the lovelace of the output it took is added." },
  utxo_growth: { label: "UTxO growth", help: "Outputs the chain's blocks created less the outputs they spent, summed over every applied block, less rolled-back ones. A positive number is a bigger UTxO set; dust floods raise it." },
  node_rejections: { label: "Node rejections", help: "Every rejection a node answered to a submission, from any sender. A double submit or a rival transaction counts each rejected answer." },
  findings: { label: "Findings", help: "Problems the oracle recorded: two nodes disagreeing, a rule broken, a payment made twice." },
  fatal_findings: { label: "Fatal findings", help: "Findings of severity fatal: results can no longer be trusted, for example a node accepted a transaction it must reject." },
  attack_steps: { label: "Attack steps", help: "Attack steps the adversaries made: one per logged attack decision." },
  attack_cost_ada: { label: "Attack cost", help: "ADA the adversaries were charged against their cost ceilings, over all steps." },
  attack_landed: { label: "Attacks landed", help: "Attack steps whose intent confirmed on chain." },
  attack_detected: { label: "Attacks detected", help: "Attack steps a node rejected or the oracle flagged with a finding on one of the step's transactions." },
  // The impact table's columns and the cards' terms.
  metric: { label: "Metric", help: "A number folded from the run's event log. Honest-side metrics say what the attack did to honest actors and the node." },
  this_run: { label: "This run", help: "The metric's value over the run being served, folded from its event log." },
  control: { label: "Control baseline", help: "The same metric in a second run with the same seed and no adversaries, served with --control. It shows what the metric is without the attack." },
  delta: { label: "Delta", help: "This run's value less the control run's: what the attacks changed. Positive means the metric is higher with the attack." },
  success_metric: { label: "Success metric", help: "The impact metric the adversary's profile says measures whether its attack works (for example utxo_growth for a dust flood). The card shows its value in this run and in the control." },
  cost_ceiling: { label: "Cost ceiling", help: "The most lovelace an adversary may be charged in one epoch. A step that would pass it is refused and the adversary idles; the bar shows this epoch's spend against it and starts again each epoch." },
  budget: { label: "Budget", help: "The ADA the adversary's wallet starts with. Its steps spend from it, within the cost ceiling." },
  agenda: { label: "Agenda", help: "The goal behind the adversary's choices: disrupt (hurt the network) or extract (take value)." },
  attempted: { label: "Attempted", help: "Attack steps the adversary made, one per logged attack decision." },
  landed: { label: "Landed", help: "Attack steps whose intent confirmed on chain, so the attack got through." },
  detected: { label: "Detected", help: "Steps a node rejected or the oracle flagged with a finding on one of the step's transactions. Rejected steps are a subset." },
  rejected: { label: "Rejected", help: "Steps a node answered with a rejection, on the first submission or on a rival or second one." },
  mutation: { label: "Mutation", help: "For malformed-cbor, which corpus edit the step applied (truncate, array-count, body-count, reserved, trailing, non-canonical, missing-signer, wrong-network, fee-too-small or steal)." },
  step_cost: { label: "Cost", help: "The lovelace the step was charged against the cost ceiling: fees and any value given away." },
  step_value: { label: "Value grabbed", help: "The honest lovelace in the output a step spends from the shared script address. It counts toward value grabbed only if the step lands." },
  step_txs: { label: "Txs", help: "Transactions the step sent: more than one for a double-spend, which races several payments on the same inputs." },
  // The attacks.
  "dust-flood": { label: "dust-flood", help: "One transaction paying 10 to 30 minimum-size outputs, about half to the shared script address and the rest to honest wallets, so honest coin selection wades through them. Success metric: utxo_growth." },
  "utxo-contention": { label: "utxo-contention", help: "Spends an honest actor's output at the shared script address that is not already being spent, racing the honest unlock for it. Success metric: honest_value_grabbed_ada." },
  "malformed-cbor": { label: "malformed-cbor", help: "Sends a payment mutated from a corpus: broken CBOR, a wrong network, a fee too small, a missing signature, or a steal of an honest output signed only by the adversary. A node that accepts one raises a fatal finding. Success metric: fatal_findings." },
  "front-run": { label: "front-run", help: "Reacts to an honest unlock: spends the same output with a higher fee and sends it ahead of the honest one. It is never chosen on its own score. Success metric: honest_failure_rate." },
  "double-spend": { label: "double-spend", help: "Two or three payments to different payees from the same inputs, sent as a race. Only one can confirm. Success metric: attack_landed." },
  "exunits-block-gap": { label: "exunits-block-gap", help: "Declares script memory between the per-block and per-transaction limits, so a node admits it but no block can hold it. Needs a genesis whose per-transaction limit exceeds the block's, so it is opt-in. Success metric: honest_latency_p99_s." },
};

const GLOSSARY_KEYS = ["honest_failure_rate", "honest_retries", "utxo_growth", "honest_value_grabbed_ada", "attempted", "landed", "detected", "rejected", "cost_ceiling", "success_metric", "control", "delta", "dust-flood", "utxo-contention", "malformed-cbor", "front-run", "double-spend", "exunits-block-gap"];

/** ADV_GLOSSARY is the "Adversaries" section of the help page. */
export const ADV_GLOSSARY: Term[] = GLOSSARY_KEYS.map((k) => ({ term: ADV_HELP[k]!.label, def: ADV_HELP[k]!.help }));
