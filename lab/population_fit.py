"""Fit one population: from its statistics to every number a profile needs.

Pure computation: a population_stats.Stats goes in, a PopulationFit comes
out. chain_stats produces the input; profile_yaml writes the output.

What is fitted, and from which statistic:

    payment amounts   output_value_lovelace   log-normal body + Pareto tail
    patience          gap_seconds             Pareto above the median gap
    dormancy          the long gaps           log-normal
    activity          tx_count_per_address    base rate + Pareto tail index
    competence        per-unit change share   beta
    care              per-unit reuse share    beta
    intent weights    tx_type_mix             shares, renormalised
    budget            the amount fit          a multiple of the tail scale

Guide: section 6 of guide.ipynb ("Calibration"); sections 2 and 3 explain
hazards and the four distributions.
"""

from __future__ import annotations

import math
from dataclasses import asdict, dataclass

import numpy as np

from cardano import LOVELACE_PER_ADA, SECONDS_PER_DAY
from estimators import BodyTail, fit_beta, fit_body_and_tail, fit_lognormal, fit_pareto_index
from population_stats import Stats

# A piece of a population is fitted from its own data only when it has at
# least this many samples; a smaller one borrows the overall fit. Twenty is
# roughly where a two-parameter fit stops being dominated by single values.
# It matters only for tiny clusters: lowering it gives them their own, noisy
# numbers, raising it makes more clusters look like the average.
MIN_SAMPLES = 20

# A gap longer than a day is counted as dormancy (the unit went quiet), not
# as patience between payments of one session.
LONG_GAP_SECONDS = SECONDS_PER_DAY
# With fewer long gaps than this, dormancy is fitted from every gap above
# the median instead: a log-normal from under 30 points has a spread that is
# mostly noise.
MIN_LONG_GAPS = 30
# The patience scale cannot go below one second, the chain's clock tick.
MIN_GAP_SCALE_SECONDS = 1.0

# Activity is fitted per unit as a Pareto above a low quantile of the unit
# transaction counts: the engine scales every actor's base rate by a
# Pareto(1, tail) multiplier, so the base rate is that quantile's rate and
# the tail index is the Pareto's. The 10th percentile is "the quietest
# ordinary unit". The base rate is proportional to the count at this
# quantile, so in a population where that count is above one (busy script
# users) raising the quantile raises the base rate with it.
ACTIVITY_FLOOR_Q = 0.1
# profile.Pareto rejects an index at or below 1 for activity_tail (the mean
# multiplier would be infinite), so a heavier fitted tail is capped here.
MIN_TAIL = 1.05
# Lower limit for an active fraction, so a rate over it stays finite.
MIN_ACTIVE_FRACTION = 1e-6

# internal/agenda/hodl.go scores a payment 0.02 against 1 for idling, and
# the engine samples an intent in proportion to score times the profile's
# weight, so a wake-up becomes a payment with probability
# 0.02 w / (1 + 0.02 w) for a send weight w. The base rate is the wake-up
# rate, so it is the payment rate divided by that probability. This must
# track the engine: if hodl.go changes its score, fitted runs send too many
# or too few payments by the same factor.
HODL_SEND_SCORE = 0.02

# The dormancy onset rate must be positive for the engine; this is used
# when a unit is active all the time.
MIN_ONSET = 1e-9

# A wallet's starting balance, in lovelace: BUDGET_TAILS times the payment
# tail scale, so a wallet can make a typical large payment many times
# before the engine caps one to its balance. Clamped to 100 ADA at least
# (enough for fees and minimum outputs) and 1,000,000 ADA at most (so one
# cluster of whales cannot drain a devnet's genesis funds).
BUDGET_TAILS = 20
MIN_BUDGET = 100 * LOVELACE_PER_ADA
MAX_BUDGET = 1_000_000 * LOVELACE_PER_ADA


@dataclass(frozen=True)
class GapFit:
    """The patience trait: a Pareto over seconds between transactions."""

    # Scale in seconds: the median gap (at least one second).
    xm: float
    # Tail index of the gaps above xm.
    alpha: float


@dataclass(frozen=True)
class DormancyFit:
    """How long a dormant unit sleeps: ln(seconds) ~ Normal(mu, sigma)."""

    mu: float
    sigma: float


@dataclass(frozen=True)
class ActivityFit:
    """How often a unit acts while it is awake."""

    # Wake-ups per second of an ordinary unit while active. A wake-up is not
    # a payment: see send_probability.
    base_per_sec: float
    # Pareto index of the per-unit multiplier on that rate (at least MIN_TAIL).
    tail: float
    # Share of days a unit is active on (population_stats.INFO).
    active_fraction: float
    # Chance that one wake-up becomes a payment.
    send_probability: float


@dataclass(frozen=True)
class PopulationFit:
    """Every fitted number of one population.

    Field order is the key order of "fit" in target.json.
    """

    amount: BodyTail
    gap: GapFit
    dormancy: DormancyFit
    activity: ActivityFit
    # Beta (a, b) of the per-unit share of transactions with change.
    competence: tuple[float, float]
    # Beta (a, b) of the per-unit share of outputs to reused addresses.
    care: tuple[float, float]
    # Share of each transaction kind (population_stats.TX_KINDS).
    mix: dict[str, float]
    # Starting balance of a wallet, in lovelace.
    budget: int

    def as_json(self) -> dict:
        """The plain-dict form stored under "fit" in target.json."""
        return asdict(self)


def intent_weights(mix: dict[str, float]) -> dict[str, float]:
    """Intent weights of a profile from the transaction-kind shares.

    Kinds that never occur are dropped, the rest are scaled to sum to 1 and
    rounded to six decimals, which is what the profile file holds. Rounding
    each weight separately can leave a sum of 0.999999 or 1.000001, which
    internal/profile rejects, so the last weight takes whatever remains.
    """
    by_intent = {"send": mix["plain"], "script_call": mix["script"],
                 "delegate": mix["stake"], "register_pool": mix["pool"]}
    occurring = {intent: share for intent, share in by_intent.items() if share > 0}
    total = sum(occurring.values())
    normalised = {intent: share / total for intent, share in occurring.items()}
    intents = list(normalised)
    rounded = {intent: round(normalised[intent], 6) for intent in intents}
    rounded[intents[-1]] = round(1.0 - sum(rounded[intent] for intent in intents[:-1]), 6)
    return rounded


def send_probability(weight: float) -> float:
    """Chance that a wake-up of a hodl actor with this send weight is a payment.

        p = s w / (1 + s w)     with s = HODL_SEND_SCORE

    The actor chooses between idling (score 1) and paying (score s times
    its send weight w) in proportion to those scores.
    """
    return HODL_SEND_SCORE * weight / (1.0 + HODL_SEND_SCORE * weight)


@dataclass(frozen=True)
class PaymentActivity:
    """Activity as observed on chain: in payments, before converting to
    the wake-ups the engine schedules."""

    payments_per_sec: float
    tail: float
    active_fraction: float


def fit_payment_activity(stats: Stats) -> PaymentActivity:
    """Base payment rate while active, tail index and active fraction.

    Intuition: a few units send most transactions. The engine models that as
    one base rate times a personal multiplier, where multipliers follow a
    Pareto starting at 1:

        rate of a unit = base * M,     P(M > m) = m^(-tail),  m >= 1

    So the unit with multiplier 1 is the quietest ordinary unit. Its count
    is taken as the ACTIVITY_FLOOR_Q quantile of the per-unit counts
    (xm, at least one transaction), and the tail index is the Pareto index
    of the counts above it (estimators.fit_pareto_index).

    A unit only sends while it is active, a fraction f of the window of T
    seconds, so the rate *while active* is higher than the plain average:

        payments_per_sec = xm / (f * T)

    What could go wrong: counts are whole numbers and xm is usually 1, so
    the tail is fitted from the units with two or more transactions; and a
    window too short to see a unit's second transaction understates
    everyone's rate.
    """
    counts = stats.samples["tx_count_per_address"]
    span_days = stats.info["span_days"]
    active_fraction = max(stats.info.get("active_fraction", 1.0), MIN_ACTIVE_FRACTION)
    floor_count = max(float(np.quantile(counts, ACTIVITY_FLOOR_Q)), 1.0)
    tail = max(fit_pareto_index(counts[counts >= floor_count], floor_count), MIN_TAIL)
    active_seconds = active_fraction * span_days * float(SECONDS_PER_DAY)
    return PaymentActivity(floor_count / active_seconds, tail, active_fraction)


def dormancy_onset(active_fraction: float, mu: float, sigma: float) -> float:
    """Per-second rate of going dormant that leaves a unit active for the
    given fraction of the time.

    Intuition: a unit alternates between being active and sleeping. Going
    dormant at a constant hazard h makes active spells last 1/h seconds on
    average; sleeps are log-normal with mean D = exp(mu + sigma^2 / 2). Over
    a long time the share spent active is

        f = (1/h) / (1/h + D)

    and solving for the hazard gives

        h = (1 - f) / (f * D)

    A unit that is always active (f = 1) gets MIN_ONSET, not zero.
    https://en.wikipedia.org/wiki/Renewal_theory
    """
    mean_dormant_seconds = math.exp(mu + sigma * sigma / 2)
    fraction = min(max(active_fraction, MIN_ACTIVE_FRACTION), 1.0)
    return max((1.0 - fraction) / (fraction * mean_dormant_seconds), MIN_ONSET)


def _enough(sample: np.ndarray, has_fallback: bool) -> bool:
    """Whether a sample is fitted on its own: it has MIN_SAMPLES values, or
    there is nothing to borrow from and it is not empty."""
    return len(sample) >= MIN_SAMPLES or (not has_fallback and len(sample) > 0)


def _need(fallback: PopulationFit | None, what: str) -> PopulationFit:
    """The fallback, or an error naming the statistic that had no samples."""
    if fallback is None:
        raise ValueError(f"cannot fit {what}: the population has no samples and there is no fallback fit")
    return fallback


def fit_gaps(gap_seconds: np.ndarray) -> tuple[GapFit, DormancyFit]:
    """Patience and dormancy from the gaps between a unit's transactions.

    Patience is a Pareto above the median gap: half of all gaps are shorter
    than xm (quick follow-ups) and the tail index says how fast longer waits
    thin out.

    Dormancy is a log-normal over the long gaps only: those above a day,
    or, when there are fewer than MIN_LONG_GAPS of them, those above the
    median.

    What could go wrong: one sample feeds both, so the two overlap; and a
    gap longer than the observation window is never seen, which makes
    dormancy look shorter than it is.
    """
    scale = max(float(np.median(gap_seconds)), MIN_GAP_SCALE_SECONDS)
    long_gaps = gap_seconds[gap_seconds > LONG_GAP_SECONDS]
    if len(long_gaps) < MIN_LONG_GAPS:
        long_gaps = gap_seconds[gap_seconds > scale]
    mu, sigma = fit_lognormal(long_gaps)
    return GapFit(xm=scale, alpha=fit_pareto_index(gap_seconds, scale)), DormancyFit(mu=mu, sigma=sigma)


def wallet_budget(amount: BodyTail) -> int:
    """Starting balance in lovelace: BUDGET_TAILS payment-tail scales,
    kept within [MIN_BUDGET, MAX_BUDGET]."""
    return int(min(max(BUDGET_TAILS * amount.xm, MIN_BUDGET), MAX_BUDGET))


def _fit_activity(stats: Stats, fallback: PopulationFit | None) -> ActivityFit:
    """Wake-up rate from the payment rate.

    The chain shows payments; the engine schedules wake-ups, of which only a
    share p (send_probability) become payments. So

        wake-ups per second = payments per second / p

    A population with no per-unit counts borrows the fallback's payment
    rate (its wake-up rate times its own p) and applies this population's p.
    A population that never sends plain payments has p = 0 and keeps the
    payment rate as it is.
    """
    if len(stats.samples["tx_count_per_address"]):
        observed = fit_payment_activity(stats)
    else:
        borrowed = _need(fallback, "activity").activity
        observed = PaymentActivity(
            borrowed.base_per_sec * (borrowed.send_probability or 1.0), borrowed.tail, borrowed.active_fraction
        )
    p_send = send_probability(intent_weights(stats.shares["tx_type_mix"]).get("send", 0.0))
    wakeups_per_sec = observed.payments_per_sec / p_send if p_send > 0 else observed.payments_per_sec
    return ActivityFit(wakeups_per_sec, observed.tail, observed.active_fraction, p_send)


def fit_population(stats: Stats, fallback: PopulationFit | None = None) -> PopulationFit:
    """Every fitted quantity of one population.

    A piece the population has too few samples for (a tiny cluster has no
    gaps, say) takes the fallback's value, normally the overall fit; with no
    fallback it is fitted from whatever there is.
    """
    has_fallback = fallback is not None

    payments = stats.samples["output_value_lovelace"]
    if _enough(payments, has_fallback):
        amount = fit_body_and_tail(payments)
    else:
        amount = _need(fallback, "payment amounts").amount

    # Two transactions in the same slot are zero seconds apart; a zero has
    # no logarithm and says nothing about waiting, so only positive gaps count.
    gaps = stats.samples["gap_seconds"]
    gaps = gaps[gaps > 0]
    if _enough(gaps, has_fallback):
        gap, dormancy = fit_gaps(gaps)
    else:
        borrowed = _need(fallback, "gaps")
        gap, dormancy = borrowed.gap, borrowed.dormancy

    return PopulationFit(
        amount=amount,
        gap=gap,
        dormancy=dormancy,
        activity=_fit_activity(stats, fallback),
        competence=fit_beta(stats.per_address["change_to_sender"]),
        care=fit_beta(stats.per_address["address_reuse"]),
        mix=stats.shares["tx_type_mix"],
        budget=wallet_budget(amount),
    )
