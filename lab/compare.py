"""Compare a run's statistics with a target: the arithmetic of grading.

Pure computation. grade.py reads the files and writes the report.

Three kinds of statistic are compared, each with the simplest distance
that fits its shape:

    a sample (payment sizes, gaps, ...)   KS distance D
    category shares (transaction mix)     total variation distance TV
    a single fraction (address reuse)     absolute difference

Each passes when its distance is at most a bound. All three bounds are
distances between 0 and 1 where 0 means identical.

Guide: section 6 of guide.ipynb ("Grading with the KS statistic").
"""

from __future__ import annotations

from dataclasses import asdict, dataclass

import numpy as np
from scipy import stats as sps

from population_stats import FRACTION_STATS, KS_STATS, SHARE_STATS, Stats

# A KS statistic passes when D <= 0.1: at no value do the run's and the
# target's cumulative curves differ by more than ten percentage points.
#
# Why a bound on D and not a p-value: a p-value answers "could these two
# samples come from exactly the same distribution?". A simulation is never
# exactly the chain, and with thousands of samples the p-value detects any
# difference, however small, so it would fail everything. D measures how
# large the difference is (an effect size), which is what calibration
# cares about. https://en.wikipedia.org/wiki/Effect_size
#
# Why 0.1: a convention, not a derivation. It is loose enough that sampling
# noise alone passes (two samples of 1,000 from the same distribution have
# D around 0.04) and tight enough that a visibly misplaced curve fails (a
# bell curve shifted by a quarter of its standard deviation has D = 0.1).
# The verdict is a hard cut, so a statistic with D = 0.11 fails and one
# with 0.09 passes: read D itself, not only the verdict. Override with
# --dmax.
KS_PASS_DISTANCE = 0.1
# The share and fraction bounds use the same ten percentage points:
# override with --tvmax and --fmax.
SHARE_PASS_DISTANCE = 0.1
FRACTION_PASS_DISTANCE = 0.1


@dataclass(frozen=True)
class Bounds:
    """The largest distance at which each kind of statistic still passes."""

    ks: float = KS_PASS_DISTANCE
    share: float = SHARE_PASS_DISTANCE
    fraction: float = FRACTION_PASS_DISTANCE


def verdict(distance: float, bound: float) -> str:
    """"pass" when the distance is within the bound, else "fail"."""
    return "pass" if distance <= bound else "fail"


@dataclass(frozen=True)
class Skipped:
    """A statistic that could not be compared, and why."""

    name: str
    reason: str
    status: str = "skipped"

    def detail(self) -> str:
        return self.reason


@dataclass(frozen=True)
class KsRow:
    """A sample compared with the target's quantiles."""

    name: str
    status: str
    # The KS distance: the largest gap between the two cumulative curves.
    D: float
    # The KS p-value, reported for reference; it does not decide.
    p: float
    # Number of values in the run's sample.
    n: int
    median: float
    target_median: float

    def detail(self) -> str:
        return (f"D={self.D:.3f} p={self.p:.2g} n={self.n} "
                f"median {self.median:.4g} vs target {self.target_median:.4g}")


@dataclass(frozen=True)
class ShareRow:
    """Category shares compared by total variation distance."""

    name: str
    status: str
    TV: float
    got: dict[str, float]
    want: dict[str, float]

    def detail(self) -> str:
        return f"TV={self.TV:.3f}; got {_shares_text(self.got)}; want {_shares_text(self.want)}"


@dataclass(frozen=True)
class FractionRow:
    """One fraction compared by absolute difference."""

    name: str
    status: str
    diff: float
    got: float
    want: float

    def detail(self) -> str:
        return f"diff={self.diff:.3f}; got {self.got:.3f}, want {self.want:.3f}"


@dataclass(frozen=True)
class CountRow:
    """A count of events against what the target's rate predicts.

    The run is short and its populations small, so a fitted rate predicts
    a handful of events: the count passes when it is inside the central
    99% of a Poisson (a rate) or binomial (a share of members) with the
    predicted mean. A KS-style distance would fail on noise alone.
    """

    name: str
    status: str
    count: int
    expected: float
    low: int
    high: int

    def detail(self) -> str:
        return f"{self.count} in the run, target predicts {self.expected:.3g} (99% range {self.low}..{self.high})"


Row = Skipped | KsRow | ShareRow | FractionRow | CountRow


def _within(name: str, count: int, expected: float, dist) -> Row:
    low, high = int(dist.ppf(0.005)), int(dist.isf(0.005))
    return CountRow(name, "pass" if low <= count <= high else "fail", count, expected, low, high)


def compare_actors(counts, target_actors: dict | None, fraction_bound: float = FRACTION_PASS_DISTANCE) -> list[Row]:
    """Rows for the target's actors block: each population's head count,
    per-member rates and voting share, the contract deployment rate and
    usage concentration, and each proposal rate.

    counts is engine_stats.ActorCounts; None (an export that cannot say)
    skips every row, and a target without the block grades nothing.
    """
    if not target_actors:
        return []
    rows: list[Row] = []
    if counts is None:
        return [Skipped("actors", "export has no submission kinds; re-export the run")]
    for name in ("pools", "dreps"):
        pop = target_actors.get(name)
        if not pop:
            continue
        members = counts.members.get(name, 0)
        rows.append(CountRow(f"{name}.count", "pass" if members == pop["count"] else "fail",
                             members, pop["count"], pop["count"], pop["count"]))
        for rate in sorted(k for k in pop if k.endswith("_rate")):
            key = f"{name}.{rate}"
            if members == 0:
                rows.append(Skipped(key, f"no {name} in the run"))
                continue
            expected = pop[rate] * members * counts.days
            rows.append(_within(key, counts.events.get(key, 0), expected, sps.poisson(expected)))
        if "voting_share" in pop and members:
            rows.append(_within(f"{name}.voting_share", counts.voted.get(name, 0), pop["voting_share"] * members,
                                sps.binom(members, pop["voting_share"])))
    contracts = target_actors.get("contracts")
    if contracts:
        expected = contracts["new_rate"] * counts.wallets * counts.days
        rows.append(_within("contracts.new_rate", counts.events.get("contracts.new_rate", 0), expected, sps.poisson(expected)))
        if counts.top_share is None:
            rows.append(Skipped("contracts.top_share", "no script calls in the run"))
        else:
            diff = abs(counts.top_share - contracts["top_share"])
            rows.append(FractionRow("contracts.top_share", verdict(diff, fraction_bound), diff,
                                    counts.top_share, contracts["top_share"]))
    for kind, rate in sorted(target_actors.get("proposal_rate", {}).items()):
        key = f"proposal_rate.{kind}"
        expected = rate * counts.days
        rows.append(_within(key, counts.events.get(key, 0), expected, sps.poisson(expected)))
    return rows


def _shares_text(shares: dict[str, float]) -> str:
    return ", ".join(f"{category} {share:.3f}" for category, share in shares.items())


def as_table(rows: list[Row]) -> list[dict]:
    """Rows as plain dicts, for pandas.DataFrame or JSON."""
    return [asdict(row) for row in rows]


def compare_sample(name: str, sample: np.ndarray, target_quantiles: list[float], bound: float) -> Row:
    """Two-sample Kolmogorov-Smirnov distance between a run's sample and
    the target.

    Intuition: draw each sample as a cumulative curve F(x), "the share of
    values at or below x". D is the largest vertical gap between the two
    curves:

        D = max over x of | F_run(x) - F_target(x) |

    D = 0 means identical curves, D = 1 means no overlap at all. It reacts
    to any difference in shape or position, needs no assumption about the
    distribution, and does not change if both samples are log-transformed.

    The target is stored as quantiles, which stand in for the chain's
    sample: QUANTILE_POINTS evenly spaced quantiles reproduce its curve to
    within 0.001.

    What could go wrong: the p-value treats the quantile points as if they
    were that many independent observations, so it is only a rough guide;
    with a handful of values in the run (n in the report) D is mostly
    noise, since a sample of n values cannot have a curve smoother than
    steps of 1/n; and D is least sensitive in the far tails, where both
    curves are near 0 or 1.

    https://en.wikipedia.org/wiki/Kolmogorov%E2%80%93Smirnov_test
    https://docs.scipy.org/doc/scipy/reference/generated/scipy.stats.ks_2samp.html
    """
    if len(sample) == 0:
        return Skipped(name, "no samples")
    result = sps.ks_2samp(sample, np.asarray(target_quantiles))
    return KsRow(
        name=name,
        status=verdict(result.statistic, bound),
        D=float(result.statistic),
        p=float(result.pvalue),
        n=int(len(sample)),
        median=float(np.median(sample)),
        target_median=float(np.median(target_quantiles)),
    )


def total_variation(a: dict[str, float], b: dict[str, float]) -> float:
    """Total variation distance between two sets of category shares.

        TV = (1/2) * sum over categories of | a_c - b_c |

    Intuition: the share of transactions you would have to relabel to turn
    one mix into the other. 0 is the same mix, 1 is no category in common.
    A category missing from one side counts as a share of zero.
    https://en.wikipedia.org/wiki/Total_variation_distance_of_probability_measures
    """
    return 0.5 * sum(abs(a.get(category, 0.0) - b.get(category, 0.0)) for category in set(a) | set(b))


def compare_population(stats: Stats, target: dict, bounds: Bounds = Bounds()) -> list[Row]:
    """One row per statistic, in report order: the KS statistics, then the
    shares, then the fractions.

    target is one population's entry of a target file (see
    profile_yaml.target_of). A statistic missing on either side is skipped,
    not failed: an engine run without chain tables has no input counts.
    """
    rows: list[Row] = []
    for name in KS_STATS:
        if name not in target["quantiles"]:
            rows.append(Skipped(name, "target has no samples"))
            continue
        sample = stats.samples.get(name, np.array([]))
        rows.append(compare_sample(name, sample, target["quantiles"][name], bounds.ks))
    for name in SHARE_STATS:
        got, want = stats.shares[name], target["shares"][name]
        distance = total_variation(got, want)
        rows.append(ShareRow(name, verdict(distance, bounds.share), distance, got, want))
    for name in FRACTION_STATS:
        if name not in stats.fractions:
            rows.append(Skipped(name, "needs chain tables"))
            continue
        got, want = stats.fractions[name], target["fractions"][name]
        difference = abs(got - want)
        rows.append(FractionRow(name, verdict(difference, bounds.fraction), difference, got, want))
    return rows


def any_failed(rows: list[Row]) -> bool:
    return any(row.status == "fail" for row in rows)
