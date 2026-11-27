import type { FieldHelp, Term } from "./help";

/** Plain-language help for the terms on the governance action page and list. */
export const GOV_HELP: Record<string, FieldHelp> = {
  governance_action: { label: "Governance action", help: "A proposal anyone can put on chain by paying a deposit: change a protocol parameter, withdraw from the treasury, replace the committee or constitution, start a hard fork, or just poll. DReps, stake pools and the committee vote on it." },
  drep: { label: "DRep", help: "Delegated representative: votes on governance actions for the ADA delegated to it. Its voting power is that stake." },
  spo: { label: "SPO", help: "Stake pool operator: votes with the stake delegated to its pool, but only on the kinds of action the ledger lets pools vote on." },
  committee: { label: "Constitutional committee", help: "A small group that checks actions against the constitution. Each member has one vote, not stake. It does not vote on no-confidence or on replacing itself." },
  guardrails: { label: "Guardrails script", help: "A script in the constitution that a parameter change or treasury withdrawal must pass. The action names the script's hash (policy hash); empty means no guardrail was named." },
  security_group: { label: "Security group", help: "The protocol parameters that protect the network's safety (sizes, fees, block limits, the governance deposit). A change touching one also needs stake pool approval, not only DReps and the committee." },
  ratification: { label: "Ratification", help: "The ledger's check, at each epoch boundary, that an open action has enough yes votes from every class that may vote; a ratified action is enacted at the next boundary. continuum marks an action ratified from the votes it saw, an estimate: the ledger also counts the stake of DReps and pools that did not vote, as no." },
  gov_lifetime: { label: "GovLifetime", help: "How many epochs an action stays open for votes after it is proposed (the govActionValidityPeriod parameter). The action expires after its last epoch." },
  anchor: { label: "Anchor", help: "A link to a document that explains the action or vote, with the document's hash so a change to it can be detected. continuum does not fetch it." },
  deposit: { label: "Deposit", help: "ADA the proposer locks when proposing, returned to the return address when the action ends." },
  return_address: { label: "Return address", help: "The reward address that gets the deposit back. A continuum actor's address links to the actor." },
  prev_action: { label: "Previous action", help: "The earlier action of the same kind this one builds on. Actions of a kind form a chain so two cannot both be enacted from the same starting point." },
  status: { label: "Status", help: "open: still inside its lifetime, votes count. expired: its last epoch passed. ratified: the committee, DRep and (for a security-group change) pool votes the run saw meet the devnet's thresholds, so the ledger enacts it at an epoch boundary; an estimate, since the ledger also counts non-voters' stake as no." },
  eligibility: { label: "Who may vote", help: "The ledger lets each voter class vote on only some kinds of action. A vote from a class that may not vote is rejected." },
  quorum: { label: "Quorum", help: "The share of committee members that must vote yes for the committee to approve an action." },
  group: { label: "Parameter group", help: "Parameters belong to the network, economic, technical or governance group. The group sets which DRep threshold a change must clear." },
  param_current: { label: "Current value", help: "The parameter's value now, from the run's genesis protocol parameters. The run never enacts an update, so these stay current. Empty when unknown." },
  voting_power: { label: "Voting power", help: "The stake behind a vote in continuum's chain view: for a DRep, the stake delegated to it; for a pool, the stake delegated to the pool. A committee member's vote has none." },
  tally: { label: "Tally", help: "Each voter's latest vote, counted by class. The yes share is yes over yes plus no, weighted by stake when known; abstentions count for neither side." },
  hard_fork: { label: "Protocol version", help: "The major and minor version the network moves to if the hard fork is enacted." },
  parameter_change: { label: "Parameter change", help: "Changes one or more protocol parameters. Needs DReps and the committee, and stake pools too when it touches the security group." },
  treasury_withdrawals: { label: "Treasury withdrawal", help: "Pays ADA out of the treasury to reward addresses. DReps and the committee vote; stake pools do not." },
  update_committee: { label: "Update committee", help: "Removes members, adds members with the epoch their term ends, and sets the quorum. DReps and stake pools vote; the committee does not vote on its own replacement." },
  new_constitution: { label: "New constitution", help: "Replaces the constitution: its anchor and, optionally, a guardrails script. DReps and the committee vote; stake pools do not." },
  hard_fork_initiation: { label: "Hard fork initiation", help: "Starts a move to a new protocol version. DReps, the committee and stake pools all vote." },
  no_confidence: { label: "No confidence", help: "Says the current committee is not trusted: if enacted there is no committee until a new one is set. DReps and stake pools vote; the committee does not." },
  info: { label: "Info", help: "A poll. It changes nothing when it ends, and needs every vote yes to pass, so it never does." },
};

export const GOV_GLOSSARY: Term[] = ["governance_action", "drep", "spo", "committee", "guardrails", "security_group", "ratification", "gov_lifetime", "anchor"].map((k) => ({ term: GOV_HELP[k]!.label, def: GOV_HELP[k]!.help }));
