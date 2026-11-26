"""The statistics that describe one population of users.

A *population* is any set of transactions we want to describe: a whole
chain, one cluster of units, or one simulated run. Fitting and grading both
work on the same named statistics, so a fit and a grade compare like with
like:

    chain  --chain_stats-->  Stats  --fit-->  profile + target
    run    --engine_stats--> Stats  --grade against target--> report

The names below are also the keys of target.json and the row names of a
grade report, so renaming one changes a file format.

Guide: section 6 of guide.ipynb ("Calibration: fit, run, grade").
"""

from __future__ import annotations

from dataclasses import dataclass, field

import numpy as np


@dataclass(frozen=True)
class Quantity:
    """One documented number (a statistic or a table column): what it
    measures, in what unit, and why the lab cares."""

    name: str
    unit: str
    meaning: str
    why: str


# One value per transaction, per payment, per unit or per gap. Each is a
# one-dimensional sample whose whole shape (not just its mean) is compared.
SAMPLES = (
    Quantity(
        "inputs_per_tx", "count",
        "Spent inputs of each valid transaction.",
        "Shows how fragmented wallets are: a wallet holding many small "
        "outputs spends many inputs per payment.",
    ),
    Quantity(
        "outputs_per_tx", "count",
        "Outputs of each valid transaction, without the collateral return.",
        "Separates simple payments (2 outputs: payee and change) from batch "
        "payouts and script interactions.",
    ),
    Quantity(
        "output_value_lovelace", "lovelace",
        "Size of each payment: an output that leaves the sender's unit.",
        "The amount distribution of a profile is fitted to it. Change is "
        "excluded, see chain_stats.PAYMENT_SQL.",
    ),
    Quantity(
        "log10_output_value", "log10 lovelace",
        "log10 of output_value_lovelace.",
        "Payment sizes span many orders of magnitude; on a log scale the KS "
        "distance weighs a 2 ADA payment and a 2,000 ADA payment alike.",
    ),
    Quantity(
        "tx_count_per_address", "transactions",
        "Transactions sent by each unit in the observation window.",
        "The raw activity count. The name says address for historical "
        "reasons; it is per unit (see units.py).",
    ),
    Quantity(
        "activity_per_day", "transactions per day",
        "tx_count_per_address divided by the window length in days.",
        "Makes windows of different lengths comparable: a few units do most "
        "of the traffic, which this sample's heavy tail records.",
    ),
    Quantity(
        "gap_seconds", "seconds",
        "Time between consecutive transactions of the same unit.",
        "Drives the patience trait and the dormancy fit: short gaps are "
        "bursts, long gaps are sleeps.",
    ),
    Quantity(
        "log10_gap_seconds", "log10 seconds",
        "log10 of gap_seconds, floored at one slot (chain) or 1 ms (engine).",
        "Gaps range from seconds to months; the log keeps both ends visible "
        "and the floor keeps log10(0) out.",
    ),
)

# Category shares that sum to 1.
SHARES = (
    Quantity(
        "tx_type_mix", "share of transactions",
        "Share of plain, script, stake and pool transactions (TX_KINDS).",
        "Becomes the intent weights of a profile: how often an actor pays, "
        "calls a script, delegates or registers a pool.",
    ),
)

# Single numbers between 0 and 1.
FRACTIONS = (
    Quantity(
        "address_reuse", "share of outputs",
        "Share of outputs paid to an address that an earlier output in the "
        "window already paid.",
        "Privacy-careful wallets use a fresh address each time; reuse is the "
        "observable trace of the care trait.",
    ),
    Quantity(
        "change_to_sender", "share of transactions",
        "Share of transactions with an output back to the sender's own unit.",
        "A wallet that selects coins well returns change to itself; it is "
        "the observable trace of the competence trait.",
    ),
)

# Supporting numbers stored under Stats.info. Engine-only entries are marked.
INFO = (
    Quantity("span_days", "days", "Length of the observation window.", "Turns counts into rates."),
    Quantity(
        "active_fraction", "share of days",
        "Average share of the window's days on which a unit sent a transaction.",
        "The rest of the time the unit is treated as dormant (chain only).",
    ),
    Quantity("txs", "transactions", "Number of transactions measured.", "Sample size (chain only)."),
    Quantity("payments", "payments", "Number of payments measured.", "Sample size (engine only)."),
    Quantity(
        "idle_fraction_of_records", "share of decision records",
        "Share of an engine run's decisions that were to do nothing.",
        "Explains why wake-ups far outnumber transactions (engine only).",
    ),
)

# Statistics compared with a two-sample KS test, in report order.
KS_STATS = [
    "outputs_per_tx",
    "inputs_per_tx",
    "log10_output_value",
    "activity_per_day",
    "log10_gap_seconds",
]
# Share-valued statistics compared by total variation distance.
SHARE_STATS = [s.name for s in SHARES]
# Scalar fractions compared by absolute difference.
FRACTION_STATS = [s.name for s in FRACTIONS]

# Transaction kinds of tx_type_mix, in report order. A transaction is the
# first that applies: "script" when it carries script data (it ran a Plutus
# script), "pool" when it has a pool certificate, "stake" when it has a
# stake certificate, "plain" otherwise.
TX_KINDS = ["plain", "script", "stake", "pool"]

# A target stores each KS statistic as this many evenly spaced quantiles
# (0%, 0.1%, ..., 100%) instead of the raw sample. 1001 points pin the
# cumulative curve to within 0.001, a hundred times finer than the pass
# bound D <= 0.1, in a file of kilobytes instead of gigabytes. Fewer points
# would start to show up in D once they approach 1 / 0.1 = 10.
QUANTILE_POINTS = 1001


@dataclass
class Stats:
    """The statistics of one population. A statistic that could not be
    measured is absent (an engine run has no inputs_per_tx, for example).

    Every array in samples and per_address is sorted ascending, so nothing
    computed from it depends on the order the rows were read in.
    """

    # Keyed by the names in SAMPLES.
    samples: dict[str, np.ndarray] = field(default_factory=dict)
    # Keyed by the names in SHARES; each value maps a category to its share.
    shares: dict[str, dict[str, float]] = field(default_factory=dict)
    # Keyed by the names in FRACTIONS.
    fractions: dict[str, float] = field(default_factory=dict)
    # The same fractions measured per unit (one value per unit with enough
    # transactions). Their spread across units is what the beta traits fit.
    per_address: dict[str, np.ndarray] = field(default_factory=dict)
    # Keyed by the names in INFO.
    info: dict[str, float] = field(default_factory=dict)


def sorted_sample(values) -> np.ndarray:
    """A sample as a sorted float array.

    DuckDB returns the rows of an unordered query in whatever order its
    threads finish, and a floating-point sum depends on the order of its
    terms. Sorting fixes the order, so a fit of the same data gives the same
    numbers to the last bit on every run.
    """
    return np.sort(np.array(list(values), dtype=float))


def quantiles(sample: np.ndarray, points: int = QUANTILE_POINTS) -> list[float]:
    """Evenly spaced quantiles of a sample, from its minimum to its maximum.

    The q-quantile is the value with a share q of the sample at or below it,
    so this list is the sample's cumulative curve read sideways.
    https://en.wikipedia.org/wiki/Quantile
    """
    levels = np.linspace(0.0, 1.0, points)
    return [float(value) for value in np.quantile(sample, levels)]


def with_chain_tables(engine: Stats, chain: Stats) -> Stats:
    """Replace what an engine export can only guess at with what the devnet
    chain actually recorded.

    The engine knows the payments it asked for, not the transactions the
    node built from them: input and output counts, the on-chain amounts and
    the transaction kinds exist only in chain tables. Updates and returns
    engine.
    """
    for name in ("inputs_per_tx", "outputs_per_tx", "log10_output_value", "output_value_lovelace"):
        engine.samples[name] = chain.samples[name]
    engine.shares["tx_type_mix"] = chain.shares["tx_type_mix"]
    engine.fractions.update(chain.fractions)
    return engine
