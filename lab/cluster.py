"""Cluster units into behavioural groups.

Units that call scripts or send staking certificates are split off first by an
absolute threshold on their share (a categorical segment). The rest are
clustered on continuous, log-transformed, standardised features with a
Gaussian mixture whose k is chosen by BIC over a range. Everything is seeded,
so a seed and an input give one answer.
"""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np
import pandas as pd
from sklearn.mixture import GaussianMixture

# Feature columns of transform() in the order the mixture sees them. The
# shares (script, staking, address reuse) are not among them: a unit with one
# or two transactions has a share of exactly 0 or 1, and a Gaussian mixture
# explains such a pinned dimension with tiny-variance components, so BIC fell
# at every k and never converged. Script and staking use enter as segments
# instead; reuse is fitted per cluster as the care trait.
FEATURES = [
    "log_tx_per_day",
    "log_med_gap",
    "log_med_value",
    "log_in_per_tx",
    "log_out_per_tx",
    "log_active_days",
    "log_dormancy",
]

# reg_covar floors every component's variance in standardised units. Real
# units are not Gaussian (payments pile up on the 2 ADA minimum, counts are
# integers), so a small floor lets BIC keep buying likelihood with narrow
# components; 0.1 leaves a curve that flattens.
REG_COVAR = 0.1
# The elbow is the smallest k whose step to k+1 gains less than this share of
# the whole BIC drop across the searched range.
ELBOW_FRACTION = 0.05
# A segment smaller than this stays with the general units.
MIN_SEGMENT = 50


def transform(df: pd.DataFrame) -> np.ndarray:
    """The continuous feature matrix of a units table, before standardising."""
    cols = [
        np.log10(df["tx_per_day"].clip(lower=1e-9)),
        np.log10(df["med_gap_s"].clip(lower=1.0)),
        np.log10(df["med_value"].clip(lower=1.0)),
        np.log10(df["in_per_tx"].clip(lower=1.0)),
        np.log10(df["out_per_tx"].clip(lower=1.0)),
        np.log10(df["active_days"].clip(lower=1.0)),
        np.log10(1.0 + df["max_dormancy_s"].clip(lower=0.0)),
    ]
    return np.column_stack([np.asarray(c, dtype=float) for c in cols])


def standardise(x: np.ndarray, mean: np.ndarray | None = None, std: np.ndarray | None = None):
    """Zero mean, unit variance per column; constant columns stay at zero."""
    if mean is None:
        mean = x.mean(axis=0)
        std = x.std(axis=0)
    std = np.where(std < 1e-9, 1.0, std)
    return (x - mean) / std, mean, std


def elbow(bic: dict[int, float], fraction: float = ELBOW_FRACTION) -> int:
    """The smallest k after which BIC improves by less than fraction of the
    total drop from the first k to the lowest, or the last k when it never does."""
    ks = sorted(bic)
    floor = fraction * (bic[ks[0]] - min(bic.values()))
    for k, nxt in zip(ks, ks[1:]):
        if bic[k] - bic[nxt] < floor:
            return k
    return ks[-1]


@dataclass
class Fit:
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
        so the curve turned upward rather than running off the end."""
        ks = sorted(self.bic)
        return ks[0] < self.k_min < ks[-1]


def fit_gmm(z: np.ndarray, kmin: int = 2, kmax: int = 15, seed: int = 1,
            covariance: str = "diag", n_init: int = 3, reg_covar: float = REG_COVAR,
            select: str = "elbow") -> Fit:
    """Fit mixtures for k in kmin..kmax and keep the elbow of the BIC curve
    (select="elbow") or its minimum (select="min").

    Fit.bic holds the whole curve; Fit.interior says whether the minimum is
    inside the range.
    """
    models, bic = {}, {}
    for k in range(kmin, kmax + 1):
        models[k] = GaussianMixture(n_components=k, covariance_type=covariance, n_init=n_init,
                                    random_state=seed, reg_covar=reg_covar, max_iter=300).fit(z)
        bic[k] = float(models[k].bic(z))
    k = elbow(bic) if select == "elbow" else min(bic, key=bic.get)
    return Fit(models[k], k, bic)


def sample_rows(n: int, cap: int, seed: int) -> np.ndarray:
    """Sorted indices of at most cap of n rows, drawn without replacement."""
    if n <= cap:
        return np.arange(n)
    return np.sort(np.random.RandomState(seed).choice(n, size=cap, replace=False))


def relabel(labels: np.ndarray, centers: np.ndarray) -> tuple[np.ndarray, np.ndarray]:
    """Renumber components by their first-feature centre so labels are stable
    across fits that differ only in component order."""
    order = np.argsort(centers[:, 0], kind="stable")
    remap = np.empty(len(order), dtype=int)
    remap[order] = np.arange(len(order))
    return remap[labels], order


# Absolute thresholds, in real units, for the cluster names. A cluster is
# named from the median of its units' raw features, not from its centroid
# relative to the other clusters, so a name means the same on every network.
SCRIPT_SHARE = 0.5      # mean share of a unit's txs that call a script
STAKE_SHARE = 0.5       # mean share that are staking certificates
FREQUENT_PER_DAY = 1.0  # median txs per day at or above: frequent-trader
HOLDER_PER_DAY = 0.05   # below this (one tx per 20 days): holder
SMALL_LOVELACE = 10_000_000       # median payment below 10 ADA
LARGE_LOVELACE = 1_000_000_000    # median payment of 1,000 ADA or more


def base_name(tx_per_day: float, script: float, stake: float) -> str:
    """The behaviour class of one cluster; script use outranks staking, which
    outranks activity."""
    if script >= SCRIPT_SHARE:
        return "script-user"
    if stake >= STAKE_SHARE:
        return "staker"
    if tx_per_day >= FREQUENT_PER_DAY:
        return "frequent-trader"
    if tx_per_day >= HOLDER_PER_DAY:
        return "occasional-payer"
    return "holder"


def name_clusters(df: pd.DataFrame, labels: np.ndarray) -> list[str]:
    """A short unique name per cluster 0..k-1 from its units' raw features.

    Clusters sharing a base name are told apart by payment size
    (small/mid/large, thresholds above), then by a numeric suffix in cluster
    order. Cluster order is fixed by relabel, so names are stable for a seed.
    """
    k = int(labels.max()) + 1
    base, size = [], []
    for i in range(k):
        g = df[labels == i]
        base.append(base_name(float(g["tx_per_day"].median()), float(g["script_share"].mean()),
                              float(g["stake_share"].mean())))
        v = float(g["med_value"].median())
        size.append("small" if v < SMALL_LOVELACE else "large" if v >= LARGE_LOVELACE else "mid")
    names = list(base)
    for i, b in enumerate(base):
        if base.count(b) > 1:
            names[i] = f"{b}-{size[i]}"
    seen: dict[str, int] = {}
    for i, n in enumerate(names):
        seen[n] = seen.get(n, 0) + 1
        if seen[n] > 1:
            names[i] = f"{n}-{seen[n]}"
    return names


GENERAL, SCRIPT, STAKER = 0, 1, 2


def segment(df: pd.DataFrame) -> np.ndarray:
    """GENERAL, SCRIPT or STAKER per unit: script use (share at or above
    SCRIPT_SHARE) outranks staking (STAKE_SHARE), as in base_name. A segment
    with fewer than MIN_SEGMENT units is folded into GENERAL."""
    seg = np.full(len(df), GENERAL)
    seg[(df["stake_share"] >= STAKE_SHARE).to_numpy()] = STAKER
    seg[(df["script_share"] >= SCRIPT_SHARE).to_numpy()] = SCRIPT
    for s in (SCRIPT, STAKER):
        if (seg == s).sum() < MIN_SEGMENT:
            seg[seg == s] = GENERAL
    return seg


@dataclass
class Result:
    """Cluster labels (0..k-1 over all units), their names, and the mixture."""

    labels: np.ndarray
    names: list[str]
    fit: Fit
    k: int


def cluster_units(df: pd.DataFrame, kmin: int = 2, kmax: int = 15, seed: int = 1,
                  max_units: int = 100_000, select: str = "elbow") -> Result:
    """Segment, fit a mixture on a seeded sample of the general units, assign
    every unit and name the clusters. Mixture components come first in order
    of their first feature, then the script and staker segments."""
    seg = segment(df)
    gi = np.flatnonzero(seg == GENERAL)
    x = transform(df.iloc[gi])
    sample = sample_rows(len(gi), max_units, seed)
    _, mean, std = standardise(x[sample])
    f = fit_gmm((x[sample] - mean) / np.where(std < 1e-9, 1.0, std), kmin, kmax, seed, select=select)
    z = (x - mean) / np.where(std < 1e-9, 1.0, std)
    gl, _ = relabel(f.model.predict(z), f.model.means_)
    labels = np.zeros(len(df), dtype=int)
    labels[gi] = gl
    k = f.k
    for s in (SCRIPT, STAKER):
        if (seg == s).any():
            labels[seg == s] = k
            k += 1
    labels = np.unique(labels, return_inverse=True)[1]  # drop components no unit landed in
    return Result(labels, name_clusters(df, labels), f, int(labels.max()) + 1)
