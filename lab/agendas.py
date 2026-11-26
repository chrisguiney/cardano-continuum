"""Which agenda a fitted cluster runs, and its intent weights.

Pure computation. An agenda (internal/agenda) is the policy that turns an
actor's wake-up into an intent; it can only ever choose some intents. A
profile weight on any other intent is dead: the actor never takes it, and
the cluster's transaction mix silently becomes whatever the agenda can do.
So the agenda is chosen from the mix, and the weights are written only for
intents the agenda can choose (the Go test TestShippedWeightsChoosable
fails a profile that breaks this).

    tx_type_mix  --intent_shares-->  intent shares
                 --choose_agenda-->  the agenda that reaches most of them
                 --agenda_weights--> weights over its intents, renormalised

Guide: section 6 of guide.ipynb ("Calibration").
"""

from __future__ import annotations

# The intent each chain transaction kind (population_stats.TX_KINDS) comes
# from. A wallet's governance transactions are nearly all vote delegations
# (DReps cast the votes, and they are fitted as their own population), so
# "gov" maps to delegate_drep. A script transaction that mints is a mint;
# intent_shares splits "script" by the share that do.
KIND_INTENT = {
    "plain": "send",
    "script": "script_call",
    "stake": "delegate",
    "pool": "register_pool",
    "gov": "delegate_drep",
}

# The intents each wallet agenda a fit may pick takes its weights for: the
# intents the chain's transaction kinds map to. Each must be among what
# agenda.Intents returns for that agenda in internal/agenda/agenda.go. An
# agenda's other steps (yield and civic register the stake key first) are
# prerequisites: they get no weight, so the engine uses its default of 1
# for them and the actor can still reach the weighted intents.
WEIGHTED = {
    "hodl": ("send",),
    "dapp": ("send", "script_call", "mint"),
    "yield": ("delegate",),
    "civic": ("delegate_drep",),
}

# Candidates in order of preference: a later agenda must reach more of the
# mix by AGENDA_MARGIN to replace an earlier one. hodl comes first because
# it is the plainest model of a wallet.
CANDIDATES = ("hodl", "dapp", "yield", "civic")

# How much more of a cluster's transactions an agenda must reach before it
# replaces the one before it. Without a margin a holder cluster with 1% of
# script calls would become a script user for one percent of fidelity. At
# 0.05, a cluster becomes a dapp user once a twentieth of its transactions
# call scripts; the shipped fits' holder clusters sit either well below
# that or well above it, so the choice is not sensitive near this value.
AGENDA_MARGIN = 0.05

# The score of each agenda's weighted steps against 1 for idling, from
# internal/agenda: hodl and dapp score every step 0.02 (hodl.go, dapp.go).
# yield and civic score their first step (registering the stake key) 1;
# a staking or voting unit's few transactions on chain are mostly those
# setup steps, so their wake-ups are taken as one transaction in two. Must
# track the Go scores: if one changes, fitted runs act too often or too
# rarely by the same factor.
TX_SCORE = {"hodl": 0.02, "dapp": 0.02, "yield": 1.0, "civic": 1.0}


def intent_shares(mix: dict[str, float], mint_share: float = 0.0) -> dict[str, float]:
    """Share of a cluster's transactions per intent, from its tx_type_mix.

    mint_share is the share of its script transactions that mint
    (population_stats.INFO script_mint_share); they count as mint.
    """
    shares = {intent: 0.0 for intent in KIND_INTENT.values()}
    shares["mint"] = 0.0
    for kind, share in mix.items():
        shares[KIND_INTENT[kind]] += share
    script = shares["script_call"]
    shares["mint"], shares["script_call"] = script * mint_share, script * (1.0 - mint_share)
    return shares


def reach(agenda: str, shares: dict[str, float]) -> float:
    """Share of the transactions an agenda can reproduce."""
    return sum(shares.get(intent, 0.0) for intent in WEIGHTED[agenda])


def choose_agenda(shares: dict[str, float]) -> str:
    """The agenda that reproduces most of a cluster's transactions.

    Walks CANDIDATES in order and keeps an agenda unless a later one
    reaches more of the mix by more than AGENDA_MARGIN. With the shipped
    names this gives script users dapp, stakers yield, voters civic and
    plain payers hodl, but the rule reads the mix, not the name.
    """
    best = CANDIDATES[0]
    for agenda in CANDIDATES[1:]:
        if reach(agenda, shares) > reach(best, shares) + AGENDA_MARGIN:
            best = agenda
    return best


def agenda_weights(agenda: str, shares: dict[str, float]) -> dict[str, float]:
    """Intent weights for an agenda: its weighted intents' shares scaled to
    sum to 1, six decimals each, the last taking the rounding remainder so
    the sum is exactly what internal/profile accepts.

    dapp keeps a zero weight for an intent it never saw (a cluster with no
    mints writes mint: 0): left out, the engine would give it weight 1. An
    agenda none of whose intents occur splits its weight evenly.
    """
    intents = WEIGHTED[agenda]
    total = reach(agenda, shares)
    if total <= 0:
        normalised = {intent: 1.0 / len(intents) for intent in intents}
    else:
        normalised = {intent: shares.get(intent, 0.0) / total for intent in intents}
    if agenda != "dapp":
        normalised = {intent: w for intent, w in normalised.items() if w > 0}
    names = list(normalised)
    rounded = {intent: round(normalised[intent], 6) for intent in names}
    rounded[names[-1]] = round(1.0 - sum(rounded[intent] for intent in names[:-1]), 6)
    return rounded


def tx_probability(agenda: str) -> float:
    """Chance that a wake-up of an actor on this agenda becomes a transaction.

        p = s / (1 + s)       s = TX_SCORE[agenda]

    The actor chooses between idling (score 1) and acting (score s times
    the weight, and the weights sum to 1) in proportion to the scores.
    """
    score = TX_SCORE[agenda]
    return score / (1.0 + score)
