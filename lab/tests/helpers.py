"""Fixture paths and synthetic inputs shared by the lab tests."""

import os
import shutil

import duckdb
import numpy as np
import pandas as pd

from population_stats import Stats

HERE = os.path.dirname(os.path.abspath(__file__))
LAB = os.path.dirname(HERE)
CHAIN = os.path.join(HERE, "fixtures", "chain")
ENGINE = os.path.join(HERE, "fixtures", "engine")
EXAMPLE = os.path.join(LAB, "example", "clustered.yaml")


def synthetic_stats(seed: int, log_payment_scale: float) -> Stats:
    """Statistics of a made-up population with known shapes: log-normal
    payments around exp(log_payment_scale) lovelace, Pareto activity,
    log-normal gaps, 90% plain and 10% script transactions over 90 days."""
    rng = np.random.default_rng(seed)
    stats = Stats()
    n = 2000
    stats.samples["output_value_lovelace"] = rng.lognormal(log_payment_scale, 1.0, n)
    stats.samples["log10_output_value"] = np.log10(stats.samples["output_value_lovelace"])
    stats.samples["tx_count_per_address"] = np.ceil(rng.pareto(1.5, 300) + 1)
    stats.samples["activity_per_day"] = stats.samples["tx_count_per_address"] / 90
    stats.samples["gap_seconds"] = rng.lognormal(8, 2, n)
    stats.samples["log10_gap_seconds"] = np.log10(stats.samples["gap_seconds"])
    stats.samples["inputs_per_tx"] = rng.integers(1, 4, n).astype(float)
    stats.samples["outputs_per_tx"] = rng.integers(1, 4, n).astype(float)
    stats.shares["tx_type_mix"] = {"plain": 0.9, "script": 0.1, "stake": 0.0, "pool": 0.0}
    stats.fractions = {"address_reuse": 0.3, "change_to_sender": 0.6}
    stats.per_address = {"change_to_sender": rng.beta(3, 2, 200), "address_reuse": rng.beta(2, 5, 200)}
    stats.info = {"span_days": 90.0, "txs": float(n), "active_fraction": 0.2}
    return stats


def three_blobs(n: int = 300, seed: int = 0) -> np.ndarray:
    """Three well separated Gaussian blobs in ten dimensions."""
    rng = np.random.RandomState(seed)
    centres = np.array([[0.0] * 10, [6.0] * 10, [-6.0, 6.0] * 5])
    return np.vstack([rng.normal(centre, 0.5, size=(n, 10)) for centre in centres])


def units_with_pinned_shares(n: int = 1500, seed: int = 0) -> pd.DataFrame:
    """A units table of four behaviours (frequent small payers, rare large
    payers, script users, occasional payers) whose share columns sit exactly
    at 0 or 1, as a single-transaction unit's do."""
    rng = np.random.default_rng(seed)
    behaviours = [(2.0, 5e6, 0.0, 1.0, n), (0.01, 2e9, 0.0, 0.0, n), (0.3, 3e7, 1.0, 0.0, n // 2), (0.1, 8e7, 0.0, 1.0, n)]
    tables = []
    for tx_per_day, payment, script, reuse, m in behaviours:
        tables.append(pd.DataFrame({
            "tx_per_day": tx_per_day * rng.lognormal(0, 0.5, m),
            "median_gap_seconds": 86400 / tx_per_day * rng.lognormal(0, 0.5, m),
            "median_payment_lovelace": payment * rng.lognormal(0, 0.5, m),
            "script_share": np.where(rng.random(m) < 0.97, script, 1 - script),
            "stake_share": np.zeros(m),
            "inputs_per_tx": 1 + 2 * tx_per_day * rng.lognormal(0, 0.4, m),
            "outputs_per_tx": 2 * rng.lognormal(0, 0.4, m),
            "reuse_share": np.where(rng.random(m) < 0.97, reuse, 1 - reuse),
            "active_days": 30 * tx_per_day ** 0.3 * rng.lognormal(0, 0.4, m),
            "longest_gap_seconds": 1e5 / tx_per_day * rng.lognormal(0, 0.5, m),
            "tx_count": np.ceil(tx_per_day * 90 * rng.lognormal(0, 0.4, m)),
        }))
    return pd.concat(tables, ignore_index=True)


def copy_export_with_run_table(destination: str, profile_hash: bytes) -> None:
    """A copy of the engine fixture with a run table carrying a profile
    hash, as a real `continuum export` has."""
    shutil.copytree(ENGINE, destination)
    os.makedirs(os.path.join(destination, "run"))
    con = duckdb.connect()
    con.register("h", pd.DataFrame({"profile_hash": [profile_hash]}))
    con.execute(f"COPY (SELECT profile_hash FROM h) TO '{destination}/run/part-000000.parquet' (FORMAT parquet)")
