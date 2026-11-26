"""Cluster units into behavioural groups with a Gaussian mixture.

Features are log-transformed where heavy-tailed, standardised, and fitted with
k components chosen by BIC over a range. Everything is seeded, so a seed and
an input give one answer.
"""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np
import pandas as pd
from sklearn.mixture import GaussianMixture

# Feature columns of units.features in the order the mixture sees them.
FEATURES = [
    "log_tx_per_day",
    "log_med_gap",
    "log_med_value",
    "script_share",
    "stake_share",
    "log_in_per_tx",
    "log_out_per_tx",
    "reuse",
    "log_active_days",
    "log_dormancy",
]


def transform(df: pd.DataFrame) -> np.ndarray:
    """The feature matrix of a units table, before standardising."""
    cols = [
        np.log10(df["tx_per_day"].clip(lower=1e-9)),
        np.log10(df["med_gap_s"].clip(lower=1.0)),
        np.log10(df["med_value"].clip(lower=1.0)),
        df["script_share"],
        df["stake_share"],
        np.log10(df["in_per_tx"].clip(lower=1.0)),
        np.log10(df["out_per_tx"].clip(lower=1.0)),
        df["reuse"],
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


@dataclass
class Fit:
    model: GaussianMixture
    k: int
    bic: dict[int, float]


def fit_gmm(z: np.ndarray, kmin: int = 2, kmax: int = 8, seed: int = 1,
            covariance: str = "diag", n_init: int = 3) -> Fit:
    """Fit mixtures for k in kmin..kmax and keep the one with the lowest BIC."""
    best, bic = None, {}
    for k in range(kmin, kmax + 1):
        m = GaussianMixture(n_components=k, covariance_type=covariance, n_init=n_init,
                            random_state=seed, reg_covar=1e-4, max_iter=300).fit(z)
        bic[k] = float(m.bic(z))
        if best is None or bic[k] < bic[best.n_components]:
            best = m
    return Fit(best, best.n_components, bic)


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


def _level(z: float, words: tuple[str, str, str], cut: float = 0.5) -> str:
    return words[0] if z < -cut else words[2] if z > cut else words[1]


def name_clusters(z_centers: np.ndarray, raw_centers: np.ndarray) -> list[str]:
    """A name per cluster from its centroid: frequency and value, then
    script/staking when the share is high, then further traits to break ties.

    z_centers are centroids in standardised space; raw_centers the same
    centroids in transform() space.
    """
    ix = {f: i for i, f in enumerate(FEATURES)}
    names: list[str] = []
    for z, raw in zip(z_centers, raw_centers):
        n = (f"{_level(z[ix['log_tx_per_day']], ('low', 'mid', 'high'))}-freq-"
             f"{_level(z[ix['log_med_value']], ('small', 'mid', 'large'))}-value")
        if raw[ix["script_share"]] > 0.25:
            n += "-script"
        if raw[ix["stake_share"]] > 0.25:
            n += "-staking"
        names.append(n)
    extra = [("log_dormancy", "dormant", "steady"), ("reuse", "reuser", "fresh"),
             ("log_out_per_tx", "fanout", "pair")]
    for _ in range(len(extra)):
        dup = {n for n in names if names.count(n) > 1}
        if not dup:
            break
        feat, hi, lo = extra[_]
        for i, n in enumerate(names):
            if n in dup:
                names[i] = f"{n}-{hi if z_centers[i][ix[feat]] > 0 else lo}"
    seen: dict[str, int] = {}
    for i, n in enumerate(names):
        seen[n] = seen.get(n, 0) + 1
        if seen[n] > 1:
            names[i] = f"{n}-{seen[n]}"
    return names
