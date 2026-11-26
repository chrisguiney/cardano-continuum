"""export_guide_data.py: the guide's per-network file has a fixed shape,
stays small, and is the same bytes for the same fit."""

import json

import numpy as np
import pandas as pd

import export_guide_data
import fit
from helpers import CHAIN

FIT_ARGS = ["--clusters", "--network", "fixt", "--kmin", "2", "--kmax", "3", "--window-days", "36500",
            "--seed", "4", "--m2pq-version", "t1"]


def fit_and_export(out):
    assert fit.main([CHAIN, "--out", str(out), *FIT_ARGS, "--units-out", str(out / "fixt.units.parquet")]) == 0
    assert export_guide_data.main([str(out / "fixt.target.json"), str(out / "fixt.units.parquet"),
                                   "--out", str(out / "fixt.json")]) == 0
    return out


def test_two_fits_and_exports_are_byte_identical(tmp_path):
    """The fit is seeded and every sample sorted, so running it twice on
    the same chain must give the same files to the byte."""
    a, b = fit_and_export(tmp_path / "a"), fit_and_export(tmp_path / "b")
    for name in ("fixt.yaml", "fixt.target.json", "fixt.json"):
        assert (a / name).read_bytes() == (b / name).read_bytes(), name


def test_export_schema_and_size(tmp_path):
    out = fit_and_export(tmp_path)
    raw = (out / "fixt.json").read_bytes()
    assert len(raw) < export_guide_data.MAX_BYTES
    data = json.loads(raw)
    assert set(data) == {"version", "header", "bic", "clusters", "units", "actors"}
    assert data["header"]["seed"] == 4 and list(data["bic"]) == ["2", "3"]
    shares = 0.0
    for cluster in data["clusters"]:
        for key in ("share", "agenda", "tx_type_mix", "weights", "activity", "patience", "dormancy", "amount",
                    "competence", "care"):
            assert key in cluster, key
        assert set(cluster["amount"]) == {"body", "tail", "tail_share"}
        assert cluster["dormancy"]["onset_per_sec"] > 0
        shares += cluster["share"]
    assert abs(shares - 1) < 1e-5
    names = {cluster["name"] for cluster in data["clusters"]}
    rows = data["units"]["rows"]
    assert rows and all(len(row) == 3 and row[2] in names for row in rows)
    assert set(data["actors"]) == {"pools", "dreps", "contracts", "proposal_rate"}


def test_the_sample_is_stratified_with_a_floor():
    """A cluster of 1% still gets MIN_PER_CLUSTER points; the large one
    gets its proportional share; the total stays near the target."""
    rng = np.random.default_rng(0)
    n_big, n_small = 9900, 100
    units = pd.DataFrame({
        "unit": [f"u{i:05d}" for i in range(n_big + n_small)],
        "tx_per_day": rng.lognormal(0, 1, n_big + n_small),
        "median_payment_lovelace": rng.lognormal(16, 1, n_big + n_small),
        "cluster": ["big"] * n_big + ["small"] * n_small,
    })
    rows = export_guide_data.sample_units(units, 1500, 25, seed=1)
    counts = pd.Series([row[2] for row in rows]).value_counts()
    assert counts["small"] == 25 and counts["big"] == 1485
    assert rows == export_guide_data.sample_units(units.sample(frac=1, random_state=3), 1500, 25, seed=1)


def test_a_full_size_export_stays_under_the_limit():
    """1,500 points and fifteen clusters of parameters fit in the budget."""
    rng = np.random.default_rng(0)
    n = 20_000
    units = pd.DataFrame({"unit": [f"u{i}" for i in range(n)], "tx_per_day": rng.lognormal(0, 1, n),
                          "median_payment_lovelace": rng.lognormal(16, 1, n),
                          "cluster": [f"cluster-{i % 15}" for i in range(n)]})
    rows = export_guide_data.sample_units(units, export_guide_data.SAMPLE_UNITS, export_guide_data.MIN_PER_CLUSTER, 1)
    assert len(export_guide_data.encode({"rows": rows})) < export_guide_data.MAX_BYTES / 2
