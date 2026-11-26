"""Fit the non-wallet populations: pools, DReps, contracts, proposals.

Pure computation: an actor_stats.ActorEvents goes in, an ActorFit comes
out, and profile_yaml writes it as the profile file's `populations` and
`network` blocks (internal/profile's Population and Network).

What is fitted:

    pools      head count at the window's end; registrations, retirements
               and parameter updates per pool per day; pledge, margin and
               fixed cost as a point mass plus a continuous rest
    DReps      head count; registrations and retirements per DRep per day;
               votes per DRep per day; share of DReps that voted
    contracts  new contracts per active wallet unit per day; transactions
               per contract (Pareto index) and the busiest 1%'s share
    proposals  submissions per day by governance action type

One machine runs at most MAX_POOLS pools and MAX_DREPS DReps, so the head
counts are cut to those and the scale recorded. Everything else is a rate
per member or a distribution, which a smaller population keeps.

README: "Non-wallet populations".
"""

from __future__ import annotations

from collections import Counter
from dataclasses import asdict, dataclass

import numpy as np

from actor_stats import ActorEvents
from cardano import epoch_of_slot
from estimators import fit_beta, fit_lognormal, fit_pareto_index

# The most forging pools one machine runs: the operator's pod budget
# (BudgetSpec.MaxPools) on a 48 GiB single-node cluster fits about 12
# block producers beside the relays and the engine. Every network's pool
# count is cut to this; the rates per pool are kept.
MAX_POOLS = 12
# The most DReps a run keeps. A DRep costs a key and a vote now and then,
# not a pod, but its votes are what the civic agenda reacts to; 12 keeps
# governance about as busy as the 12 pools keep consensus, and a larger
# number would mostly add idle voters.
MAX_DREPS = 12

# The share of contracts counted as "the busiest" in top_share.
TOP_FRACTION = 0.01


@dataclass(frozen=True)
class SpikeFit:
    """A point mass plus a continuous rest (profile.Spike)."""

    point: float
    share: float
    # "lognormal" or "beta", with its two parameters.
    dist: str
    a: float
    b: float


@dataclass(frozen=True)
class PoolFit:
    chain_count: int
    count: int
    scale: float
    register_rate: float
    retire_rate: float
    update_rate: float
    pledge: SpikeFit
    margin: SpikeFit
    cost: SpikeFit


@dataclass(frozen=True)
class DRepFit:
    chain_count: int
    count: int
    scale: float
    register_rate: float
    retire_rate: float
    vote_rate: float
    voting_share: float


@dataclass(frozen=True)
class ContractFit:
    chain_count: int
    new_contracts: int
    new_rate: float
    usage_alpha: float
    top_share: float


@dataclass(frozen=True)
class ActorFit:
    pools: PoolFit | None
    dreps: DRepFit | None
    contracts: ContractFit | None
    # Proposals per day by action type, in name order.
    proposal_rate: dict[str, float]

    def as_json(self) -> dict:
        return asdict(self)


def scaled_count(chain_count: int, budget: int) -> tuple[int, float]:
    """The head count a run keeps and its scale: the chain's own count up to
    budget, otherwise budget. A population that exists keeps at least one."""
    count = min(chain_count, budget)
    return count, (count / chain_count if chain_count else 1.0)


def lognormal_spike(values: np.ndarray, point: float) -> SpikeFit:
    """The share of values exactly at point, and a log-normal over the rest."""
    at_point = values == point
    mu, sigma = fit_lognormal(values[~at_point])
    return SpikeFit(point=float(point), share=float(at_point.mean()) if len(values) else 0.0,
                    dist="lognormal", a=mu, b=sigma)


def margin_spike(margins: np.ndarray) -> SpikeFit:
    """Margins are fractions with many pools at exactly 0: that share, and a
    beta over the others."""
    at_zero = margins == 0
    a, b = fit_beta(margins[~at_zero])
    return SpikeFit(point=0.0, share=float(at_zero.mean()) if len(margins) else 0.0, dist="beta", a=a, b=b)


def mode(values: np.ndarray) -> float:
    """The most common value, the smallest on a tie; 0 for no values."""
    if not len(values):
        return 0.0
    counts = Counter(values.tolist())
    top = max(counts.values())
    return float(min(v for v, n in counts.items() if n == top))


def _per_member_day(events: int, members: int, days: float) -> float:
    return events / (members * days) if members and days > 0 else 0.0


@dataclass
class _Pool:
    """One pool's history while replaying certificates in chain order."""

    first_slot: int
    last_params: tuple
    retire_epoch: int | None = None


def fit_pools(rows: list[tuple], first_slot: int, last_slot: int, days: float, network: str) -> PoolFit | None:
    """Pools registered at last_slot and their rates over the window.

    A pool is registered from its first registration until a retirement it
    announced takes effect; a registration after the announcement cancels
    it, as the ledger's POOL rule does. With no epoch schedule for the
    network every announced retirement counts as in effect. New pools are
    first registrations in the window, updates are later registrations of
    a known pool, retirements are announcements in the window.
    """
    pools: dict[str, _Pool] = {}
    new = updates = retires = 0
    for pool, slot, what, pledge, cost, margin, retire_epoch in rows:
        in_window = slot >= first_slot
        if what == "register":
            known = pool in pools
            if not known:
                pools[pool] = _Pool(first_slot=slot, last_params=(pledge, cost, margin))
            pools[pool].last_params, pools[pool].retire_epoch = (pledge, cost, margin), None
            new += in_window and not known
            updates += in_window and known
        elif pool in pools:
            pools[pool].retire_epoch = int(retire_epoch)
            retires += in_window
    end_epoch = epoch_of_slot(last_slot, network)
    live = [p for p in pools.values()
            if p.retire_epoch is None or (end_epoch is not None and p.retire_epoch > end_epoch)]
    if not live:
        return None
    params = np.array([p.last_params for p in live], dtype=float)
    count, scale = scaled_count(len(live), MAX_POOLS)
    return PoolFit(
        chain_count=len(live), count=count, scale=scale,
        register_rate=_per_member_day(new, len(live), days),
        retire_rate=_per_member_day(retires, len(live), days),
        update_rate=_per_member_day(updates, len(live), days),
        pledge=lognormal_spike(params[:, 0], 0.0),
        margin=margin_spike(np.nan_to_num(params[:, 2])),
        # Most pools charge exactly the protocol's minimum fixed cost, so the
        # spike sits on the most common cost rather than on zero.
        cost=lognormal_spike(params[:, 1], mode(params[:, 1])),
    )


def fit_dreps(events: list[tuple], votes: list[tuple], first_slot: int, days: float) -> DRepFit | None:
    """DReps registered at the window's end and their rates over it.

    A DRep is registered from its registration to its retirement. Expiry
    for inactivity is not modelled: a DRep that stopped voting still counts.
    """
    registered: dict[str, bool] = {}
    regs = retires = 0
    for drep, slot, what in events:
        registered[drep] = what == "register"
        regs += slot >= first_slot and what == "register"
        retires += slot >= first_slot and what == "retire"
    live = {d for d, on in registered.items() if on}
    if not live:
        return None
    window_votes = [voter for voter, slot in votes if slot >= first_slot]
    voters = set(window_votes) & live
    count, scale = scaled_count(len(live), MAX_DREPS)
    return DRepFit(
        chain_count=len(live), count=count, scale=scale,
        register_rate=_per_member_day(regs, len(live), days),
        retire_rate=_per_member_day(retires, len(live), days),
        vote_rate=_per_member_day(len(window_votes), len(live), days),
        voting_share=len(voters) / len(live),
    )


def top_share(counts: np.ndarray, fraction: float = TOP_FRACTION) -> float:
    """Share of all use that the busiest `fraction` of contracts take (at
    least one contract)."""
    if not len(counts) or counts.sum() == 0:
        return 0.0
    top = max(1, int(round(len(counts) * fraction)))
    return float(np.sort(counts)[::-1][:top].sum() / counts.sum())


def fit_contracts(use: list[int], new_contracts: int, units: int, days: float) -> ContractFit | None:
    """Contracts paid in the window: how fast new ones appear per active
    wallet unit, and how use concentrates.

    Use per contract is fitted as a Pareto above one transaction: alpha
    under 1 means a few contracts take nearly all the traffic. A contract
    paid once says nothing about the tail and is left out of alpha (it is
    in top_share).
    """
    if not use:
        return None
    counts = np.array(use, dtype=float)
    return ContractFit(
        chain_count=len(use), new_contracts=new_contracts,
        new_rate=_per_member_day(new_contracts, units, days),
        usage_alpha=fit_pareto_index(counts, 1.0),
        top_share=top_share(counts),
    )


def proposal_rates(rows: list[tuple], first_slot: int, days: float) -> dict[str, float]:
    """Governance proposals per day in the window, by action type."""
    counts = Counter(action for action, slot in rows if slot >= first_slot)
    return {action: counts[action] / days for action in sorted(counts)} if days > 0 else {}


def fit_actors(events: ActorEvents, first_slot: int, last_slot: int, days: float, units: int,
               network: str) -> ActorFit:
    """Every non-wallet population of one chain window."""
    return ActorFit(
        pools=fit_pools(events.pool_events, first_slot, last_slot, days, network),
        dreps=fit_dreps(events.drep_events, events.drep_votes, first_slot, days),
        contracts=fit_contracts(events.contract_use, events.new_contracts, units, days),
        proposal_rate=proposal_rates(events.proposals, first_slot, days),
    )
