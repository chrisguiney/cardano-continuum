"""Cluster units into behavioural groups.

One profile fitted to everyone describes an average user who does not
exist. Clustering sorts units into groups that behave alike, so each group
can get its own profile. Pure computation on a unit_features table: no
database, no files.

The steps, each a function below:

    segment_units     split off script users, voters and stakers by a fixed rule
    to_log_features   the remaining ("general") units as a numeric matrix
    Scaler            put every feature on the same scale
    fit_mixtures      fit Gaussian mixtures for a range of k, score by BIC
    elbow             choose k where more clusters stop paying off
    order_by_first    number the clusters the same way on every run
    name_clusters     give each cluster a readable name

Everything is seeded, so a seed and an input give one answer.

Guide: section 7 of guide.ipynb ("Clustering").
https://scikit-learn.org/stable/modules/mixture.html
"""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np
import pandas as pd
from sklearn.mixture import GaussianMixture

from cardano import LOVELACE_PER_ADA

# Columns of to_log_features, in the order the mixture sees them. The
# shares (script, staking, address reuse) are not among them: a unit with
# one or two transactions has a share of exactly 0 or 1, and a Gaussian
# mixture explains such a pinned dimension with tiny-variance components,
# so BIC fell at every k and never converged. Script and staking use enter
# as segments instead; reuse is fitted per cluster as the care trait.
FEATURES = [
    "log_tx_per_day",
    "log_med_gap",
    "log_med_value",
    "log_in_per_tx",
    "log_out_per_tx",
    "log_active_days",
    "log_dormancy",
]

# A rate of zero has no logarithm; a unit in the table always has at least
# one transaction, so this floor is never the value of a real unit.
MIN_TX_PER_DAY = 1e-9
# Durations, amounts and counts are floored at 1 (one second, one lovelace,
# one input) so their log10 is at least 0.
MIN_LOGGED_VALUE = 1.0

# A feature whose standard deviation is below this is constant; it is left
# unscaled (divided by 1) instead of divided by nearly zero.
MIN_FEATURE_SPREAD = 1e-9

# reg_covar is added to every component's variance, which keeps a variance
# from collapsing to zero. Features are standardised, so 0.1 means "no
# cluster may be narrower than about a third of a standard deviation
# (sqrt(0.1) = 0.32) of the whole population in any feature".
# Why so large (scikit-learn's default is 1e-6): real units are not
# Gaussian. Payments pile up on 2 ADA and counts are whole numbers, so with
# a tiny floor the mixture keeps finding ever narrower spikes, the
# likelihood keeps rising and BIC never stops falling. 0.1 leaves a BIC
# curve that flattens. Sensitivity: this is the strongest knob in the
# clustering. Smaller values give more and narrower clusters, larger ones
# fewer and broader; the chosen k moves with it.
REG_COVAR = 0.1

# The elbow is the smallest k whose step to k+1 gains less than this share
# of the whole BIC drop across the searched range. At 5%, a step must still
# deliver a twentieth of everything more clusters could ever deliver.
# Sensitivity, from the shipped BIC curves: 5% picks 5 mixture components
# on both preprod and preview; 2% would pick 7 and 9, 10% would pick 3
# and 4.
ELBOW_FRACTION = 0.05

# A script, voter or staker segment smaller than this stays with the general
# units: a profile fitted from a few dozen units is mostly noise.
MIN_SEGMENT = 50

# How many mixtures are fitted from different random starts for each k; the
# best is kept. EM only finds a local optimum, so one start can be unlucky.
RANDOM_STARTS = 3
MAX_EM_ITERATIONS = 300


def to_log_features(units: pd.DataFrame) -> np.ndarray:
    """The continuous feature matrix of a units table, before scaling: one
    row per unit, one column per entry of FEATURES.

    Every feature is log10-transformed. Behaviour spans orders of magnitude
    (one transaction a month to thousands a day), and a Gaussian mixture
    assumes roughly bell-shaped blobs; on a log scale "ten times busier" is
    the same distance everywhere and the blobs are far closer to bells.
    """
    def log10_floored(column: str, floor: float) -> pd.Series:
        return np.log10(units[column].clip(lower=floor))

    columns = [
        log10_floored("tx_per_day", MIN_TX_PER_DAY),
        log10_floored("median_gap_seconds", MIN_LOGGED_VALUE),
        log10_floored("median_payment_lovelace", MIN_LOGGED_VALUE),
        log10_floored("inputs_per_tx", MIN_LOGGED_VALUE),
        log10_floored("outputs_per_tx", MIN_LOGGED_VALUE),
        log10_floored("active_days", MIN_LOGGED_VALUE),
        # The longest gap is zero for a unit with one transaction, so one
        # second is added before the log instead of flooring.
        np.log10(1.0 + units["longest_gap_seconds"].clip(lower=0.0)),
    ]
    return np.column_stack([np.asarray(column, dtype=float) for column in columns])


@dataclass(frozen=True)
class Scaler:
    """Standardisation learned from one sample and applied to any rows.

        z = (x - mean) / spread        per feature

    After it every feature has mean 0 and standard deviation 1 on the
    sample it was learned from. Without it the feature with the largest
    numbers (log payment size) would dominate every distance.
    https://en.wikipedia.org/wiki/Standard_score
    """

    mean: np.ndarray
    spread: np.ndarray

    @classmethod
    def learn(cls, rows: np.ndarray) -> "Scaler":
        spread = rows.std(axis=0)
        return cls(rows.mean(axis=0), np.where(spread < MIN_FEATURE_SPREAD, 1.0, spread))

    def apply(self, rows: np.ndarray) -> np.ndarray:
        return (rows - self.mean) / self.spread


def elbow(bic: dict[int, float], fraction: float = ELBOW_FRACTION) -> int:
    """The smallest k after which BIC improves by less than fraction of the
    total drop from the first k to the lowest, or the last k when it never
    does.

    Intuition: on real data BIC often keeps creeping down as k grows,
    because no number of Gaussians describes it exactly. The elbow asks
    where the curve stops falling steeply: the first k whose next step is
    not worth it.

        stop at the first k with   BIC(k) - BIC(k+1)  <  fraction * (BIC(k_first) - min BIC)

    https://en.wikipedia.org/wiki/Elbow_method_(clustering)
    """
    ks = sorted(bic)
    smallest_worthwhile_gain = fraction * (bic[ks[0]] - min(bic.values()))
    for k, next_k in zip(ks, ks[1:]):
        if bic[k] - bic[next_k] < smallest_worthwhile_gain:
            return k
    return ks[-1]


@dataclass
class MixtureFit:
    """The chosen mixture and the BIC of every k tried."""

    model: GaussianMixture
    k: int
    bic: dict[int, float]

    @property
    def k_min(self) -> int:
        """The k with the lowest BIC."""
        return min(self.bic, key=self.bic.get)

    @property
    def interior(self) -> bool:
        """True when the BIC minimum is strictly inside the searched range,
        so the curve turned upward rather than running off the end. False
        means BIC alone found no natural number of clusters, and the elbow
        is a judgement call."""
        ks = sorted(self.bic)
        return ks[0] < self.k_min < ks[-1]


def fit_mixtures(z: np.ndarray, kmin: int = 2, kmax: int = 15, seed: int = 1,
                 covariance: str = "diag", n_init: int = RANDOM_STARTS, reg_covar: float = REG_COVAR,
                 select: str = "elbow") -> MixtureFit:
    """Fit a Gaussian mixture for every k in kmin..kmax and keep one.

    **Gaussian mixture.** Assume the cloud of units is k overlapping blobs.
    Blob j has a weight w_j (its share of the units), a centre mu_j and a
    spread Sigma_j, and the density of a unit with features x is

        p(x) = sum_j  w_j * Normal(x; mu_j, Sigma_j),      sum_j w_j = 1

    The fit maximises the likelihood  L = prod_i p(x_i)  with the EM
    algorithm: guess the blobs, compute for each unit how likely it is to
    belong to each blob, re-estimate the blobs from those memberships,
    repeat. A unit is then assigned to its most likely blob.

    covariance="diag" gives each blob its own variance per feature and no
    tilt (no correlations inside a blob): 2d parameters per blob for d
    features instead of d + d(d+1)/2, which keeps small blobs estimable.
    reg_covar is added to every variance (see REG_COVAR).

    **BIC.** More blobs always fit better, so the likelihood alone would
    pick the largest k. The Bayesian information criterion charges for
    every parameter:

        BIC = p * ln(n) - 2 * ln(L)       lower is better

    with p the number of free parameters (here k*2d + k - 1) and n the
    number of units. The first term grows with k, the second shrinks; BIC
    is lowest where an extra blob no longer buys its cost in fit.

    select="elbow" keeps the elbow of the BIC curve, select="min" its
    minimum. MixtureFit.bic holds the whole curve.

    What could go wrong: EM finds a local optimum (hence n_init starts);
    blobs are a modelling convenience, not a claim that users come in
    k kinds; and k depends on REG_COVAR and ELBOW_FRACTION as much as on
    the data.

    https://scikit-learn.org/stable/modules/generated/sklearn.mixture.GaussianMixture.html
    https://en.wikipedia.org/wiki/Mixture_model#Gaussian_mixture_model
    https://en.wikipedia.org/wiki/Bayesian_information_criterion
    """
    models, bic = {}, {}
    for k in range(kmin, kmax + 1):
        models[k] = GaussianMixture(n_components=k, covariance_type=covariance, n_init=n_init,
                                    random_state=seed, reg_covar=reg_covar, max_iter=MAX_EM_ITERATIONS).fit(z)
        bic[k] = float(models[k].bic(z))
    chosen_k = elbow(bic) if select == "elbow" else min(bic, key=bic.get)
    return MixtureFit(models[chosen_k], chosen_k, bic)


def sample_rows(n: int, cap: int, seed: int) -> np.ndarray:
    """Sorted indices of at most cap of n rows, drawn without replacement.

    Fitting 14 mixtures on millions of units is slow and adds nothing: the
    blobs are stable long before that. The mixture is fitted on a seeded
    sample and then assigns every unit.
    """
    if n <= cap:
        return np.arange(n)
    return np.sort(np.random.RandomState(seed).choice(n, size=cap, replace=False))


def order_by_first(labels: np.ndarray, centers: np.ndarray) -> tuple[np.ndarray, np.ndarray]:
    """Renumber components by their first-feature centre (activity, quietest
    first), so labels are stable across fits that differ only in the
    arbitrary order EM numbered the components in. Returns the new labels
    and the old component index at each new position."""
    order = np.argsort(centers[:, 0], kind="stable")
    new_label_of = np.empty(len(order), dtype=int)
    new_label_of[order] = np.arange(len(order))
    return new_label_of[labels], order


# Absolute thresholds, in real units, for the cluster names. A cluster is
# named from the median of its units' raw features, not from its centroid
# relative to the other clusters, so a name means the same on every
# network. They only choose names (and, for the two shares, segments); no
# fitted number depends on the activity or payment thresholds.
#
# A unit is a script user, a voter or a staker when at least half of its
# transactions are of that kind: "mostly". At 0.5 a unit cannot be two of
# them unless it is exactly half and half, where script wins, then
# governance, as in chain_stats.tx_kind. Sensitivity: the
# script threshold matters a great deal upward. In the shipped preprod
# window 21,272 of 37,188 units are at or above 0.5 and 22,325 at or above
# 0.3, but only 5,665 at or above 0.7: many units mix script calls with
# plain payments about evenly.
SCRIPT_SHARE = 0.5
GOV_SHARE = 0.5
STAKE_SHARE = 0.5
FREQUENT_PER_DAY = 1.0  # median txs per day at or above: frequent-trader
HOLDER_PER_DAY = 0.05   # below this (one tx per 20 days): holder
SMALL_LOVELACE = 10 * LOVELACE_PER_ADA       # median payment below 10 ADA
LARGE_LOVELACE = 1_000 * LOVELACE_PER_ADA    # median payment of 1,000 ADA or more


def behaviour_name(tx_per_day: float, script_share: float, stake_share: float, gov_share: float = 0.0) -> str:
    """The behaviour class of one cluster; script use outranks governance,
    which outranks staking, which outranks activity."""
    if script_share >= SCRIPT_SHARE:
        return "script-user"
    if gov_share >= GOV_SHARE:
        return "voter"
    if stake_share >= STAKE_SHARE:
        return "staker"
    if tx_per_day >= FREQUENT_PER_DAY:
        return "frequent-trader"
    if tx_per_day >= HOLDER_PER_DAY:
        return "occasional-payer"
    return "holder"


def payment_size_name(median_payment_lovelace: float) -> str:
    """small, mid or large, by the cluster's median payment."""
    if median_payment_lovelace < SMALL_LOVELACE:
        return "small"
    if median_payment_lovelace >= LARGE_LOVELACE:
        return "large"
    return "mid"


def _number_repeats(names: list[str]) -> list[str]:
    """Append -2, -3, ... to the second and later uses of a name."""
    uses: dict[str, int] = {}
    numbered = list(names)
    for i, name in enumerate(names):
        uses[name] = uses.get(name, 0) + 1
        if uses[name] > 1:
            numbered[i] = f"{name}-{uses[name]}"
    return numbered


def name_clusters(units: pd.DataFrame, labels: np.ndarray) -> list[str]:
    """A short unique name per cluster 0..k-1 from its units' raw features.

    Clusters sharing a behaviour name are told apart by payment size
    (small/mid/large), then by a numeric suffix in cluster order. Cluster
    order is fixed by order_by_first, so names are stable for a seed.
    """
    k = int(labels.max()) + 1
    behaviours, sizes = [], []
    for cluster in range(k):
        members = units[labels == cluster]
        behaviours.append(behaviour_name(
            float(members["tx_per_day"].median()),
            float(members["script_share"].mean()),
            float(members["stake_share"].mean()),
            float(members["gov_share"].mean()),
        ))
        sizes.append(payment_size_name(float(members["median_payment_lovelace"].median())))
    names = [
        f"{behaviour}-{size}" if behaviours.count(behaviour) > 1 else behaviour
        for behaviour, size in zip(behaviours, sizes)
    ]
    return _number_repeats(names)


GENERAL, SCRIPT, STAKER, VOTER = 0, 1, 2, 3
# Segments in the order their clusters are numbered after the mixture's.
SEGMENTS = (SCRIPT, STAKER, VOTER)


def segment_units(units: pd.DataFrame) -> np.ndarray:
    """GENERAL, SCRIPT, VOTER or STAKER per unit.

    Script users, voters and stakers are told apart by a fixed rule, not by
    the mixture: "what kind of transactions does this unit send" is a
    category, and categories make poor Gaussian features (see FEATURES).
    Script use (share at or above SCRIPT_SHARE) outranks governance
    (GOV_SHARE), which outranks staking (STAKE_SHARE), as in
    behaviour_name. A segment with fewer than MIN_SEGMENT units is folded
    into GENERAL.
    """
    segments = np.full(len(units), GENERAL)
    segments[(units["stake_share"] >= STAKE_SHARE).to_numpy()] = STAKER
    segments[(units["gov_share"] >= GOV_SHARE).to_numpy()] = VOTER
    segments[(units["script_share"] >= SCRIPT_SHARE).to_numpy()] = SCRIPT
    for segment in SEGMENTS:
        if (segments == segment).sum() < MIN_SEGMENT:
            segments[segments == segment] = GENERAL
    return segments


@dataclass
class Clustering:
    """The cluster of every unit, and how it was found."""

    # Cluster index 0..k-1 of each unit, in table order.
    labels: np.ndarray
    # Name of each cluster, by index.
    names: list[str]
    # The mixture over the general units, with its BIC curve.
    fit: MixtureFit
    # Number of clusters: mixture components that got units, plus segments.
    k: int
    # How many general units the mixture was fitted on, of how many.
    sampled_units: int
    general_units: int


def _label_general_units(features: np.ndarray, kmin: int, kmax: int, seed: int, max_units: int,
                         select: str) -> tuple[np.ndarray, MixtureFit, int]:
    """Fit the mixture on a seeded sample of the rows and assign every row.
    Returns the labels, the fit and the sample size."""
    sample = sample_rows(len(features), max_units, seed)
    scaler = Scaler.learn(features[sample])
    fit = fit_mixtures(scaler.apply(features[sample]), kmin, kmax, seed, select=select)
    labels, _ = order_by_first(fit.model.predict(scaler.apply(features)), fit.model.means_)
    return labels, fit, len(sample)


def cluster_units(units: pd.DataFrame, kmin: int = 2, kmax: int = 15, seed: int = 1,
                  max_units: int = 100_000, select: str = "elbow") -> Clustering:
    """Segment, fit a mixture on a seeded sample of the general units, assign
    every unit and name the clusters.

    units is a unit_features table. Mixture components come first, in order
    of activity, then the script, staker and voter segments.
    """
    segments = segment_units(units)
    general_rows = np.flatnonzero(segments == GENERAL)
    general_labels, fit, sampled = _label_general_units(
        to_log_features(units.iloc[general_rows]), kmin, kmax, seed, max_units, select)
    labels = np.zeros(len(units), dtype=int)
    labels[general_rows] = general_labels
    next_label = fit.k
    for segment in SEGMENTS:
        if (segments == segment).any():
            labels[segments == segment] = next_label
            next_label += 1
    # A component can end up with no unit; renumber so labels are 0..k-1
    # with no holes.
    labels = np.unique(labels, return_inverse=True)[1]
    return Clustering(labels, name_clusters(units, labels), fit, int(labels.max()) + 1, sampled, len(general_rows))
