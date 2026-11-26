"""compare.py and grade.py: when a run passes, fails or is skipped."""

import json
import sqlite3

import duckdb
import numpy as np
import pytest

import compare
import engine_stats
import fit
import grade
from helpers import CHAIN, ENGINE, copy_export_with_run_table
from population_stats import KS_STATS, quantiles


def by_name(rows):
    return {row.name: row for row in rows}


def test_ks_passes_a_sample_against_its_own_quantiles():
    """1001 quantiles reproduce a sample's cumulative curve closely enough
    that the sample is at distance under 0.01 from them."""
    rng = np.random.default_rng(4)
    sample = rng.normal(size=5000)
    row = compare.compare_sample("x", sample, quantiles(sample), 0.1)
    assert row.status == "pass" and row.D < 0.01


def test_ks_fails_a_shifted_sample():
    """A bell curve moved by one standard deviation is far outside D <= 0.1."""
    rng = np.random.default_rng(5)
    sample = rng.normal(size=5000)
    row = compare.compare_sample("x", sample + 1.0, quantiles(sample), 0.1)
    assert row.status == "fail" and row.D > 0.3 and row.p < 1e-6


def test_a_chain_passes_every_ks_statistic_against_its_own_fit(chain):
    _, target = fit.fit_profile(chain, "x")
    rows = by_name(compare.compare_population(chain, target))
    for name in KS_STATS:
        assert rows[name].status == "pass", name


def test_an_engine_run_fails_or_skips_against_an_unrelated_chain_fit(chain, engine):
    """The fixture run was not made from the fixture chain's fit: activity
    and the type mix fail, and what only a chain can measure is skipped,
    not failed."""
    _, target = fit.fit_profile(chain, "x")
    rows = by_name(compare.compare_population(engine, target))
    assert rows["activity_per_day"].status == "fail"
    assert rows["tx_type_mix"].status == "fail"
    assert rows["outputs_per_tx"].status == "skipped"
    assert rows["change_to_sender"].status == "skipped"


def test_cli_writes_a_report_and_exits_1_on_a_failure(tmp_path):
    fit.main([CHAIN, "--out", str(tmp_path)])
    out = tmp_path / "report.md"
    code = grade.main([ENGINE, "--target", str(tmp_path / "target.json"), "--out", str(out)])
    text = out.read_text()
    assert code == 1
    assert "| activity_per_day | FAIL |" in text
    assert "Idle wake-ups" in text


def test_target_is_found_by_the_runs_profile_hash(tmp_path):
    """index.json maps a profile hash to its target; a run whose hash is
    not listed is an error that tells the user to pass --target."""
    profile_hash = bytes(range(32))
    export = tmp_path / "export"
    copy_export_with_run_table(str(export), profile_hash)
    profiles = tmp_path / "profiles"
    profiles.mkdir()
    (profiles / "t.target.json").write_text("{}")
    (profiles / "index.json").write_text(json.dumps({profile_hash.hex(): {"profile": "t", "target": "t.target.json"}}))
    assert grade.find_target(str(export), str(profiles)) == str(profiles / "t.target.json")
    (profiles / "index.json").write_text(json.dumps({"00" * 32: {"profile": "x", "target": "x.json"}}))
    with pytest.raises(SystemExit, match="not in"):
        grade.find_target(str(export), str(profiles))


def target_from(stats) -> dict:
    """A population's own statistics as a target entry."""
    return {
        "shares": stats.shares,
        "fractions": stats.fractions,
        "quantiles": {name: quantiles(stats.samples[name]) for name in KS_STATS if len(stats.samples.get(name, []))},
    }


def test_each_cluster_is_graded_on_its_own_actors(tmp_path):
    """With actors split between profiles a and b in the run's store.db, the
    report has an overall section and one per cluster, and cluster a passes
    against a target built from exactly its own actors."""
    export = tmp_path / "run" / "export"
    export.parent.mkdir()
    copy_export_with_run_table(str(export), bytes(32))
    actors = [row[0] for row in duckdb.connect().execute(
        f"SELECT DISTINCT actor FROM read_parquet('{export}/submission/*.parquet') ORDER BY 1").fetchall()]
    assert len(actors) >= 2
    db = sqlite3.connect(export.parent / "store.db")
    db.execute("CREATE TABLE actor (id INTEGER PRIMARY KEY, name TEXT, role TEXT, profile TEXT)")
    db.executemany("INSERT INTO actor VALUES (?, '', '', ?)", [(a, "a" if i % 2 else "b") for i, a in enumerate(actors)])
    db.commit()
    db.close()

    whole_run = engine_stats.engine_stats(str(export))
    cluster_a = engine_stats.engine_stats(str(export), {a for i, a in enumerate(actors) if i % 2})
    target = {"version": 1, "header": {}, "overall": target_from(whole_run),
              "clusters": {"a": {"share": 0.5, **target_from(cluster_a)}, "b": {"share": 0.5, **target_from(whole_run)}}}
    # inputs/outputs per tx do not exist for an engine run; give them any quantiles
    for entry in [target["overall"], *target["clusters"].values()]:
        for name in KS_STATS:
            entry["quantiles"].setdefault(name, quantiles(np.array([1.0, 2.0])))
    target_path = tmp_path / "t.json"
    target_path.write_text(json.dumps(target))

    out = tmp_path / "report.md"
    code = grade.main([str(export), "--target", str(target_path), "--out", str(out)])
    text = out.read_text()
    assert "## Cluster a" in text and "## Cluster b" in text and "## Overall" in text
    assert "| log10_output_value | PASS |" in text
    assert code in (0, 1)
