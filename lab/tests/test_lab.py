import json
import os
import subprocess
import sys

import numpy as np
import pytest
import yaml

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.dirname(HERE))
import common  # noqa: E402
import fit  # noqa: E402
import grade  # noqa: E402

CHAIN = os.path.join(HERE, "fixtures", "chain")
ENGINE = os.path.join(HERE, "fixtures", "engine")


@pytest.fixture(scope="module")
def chain():
    return common.chain_stats(CHAIN)


@pytest.fixture(scope="module")
def engine():
    return common.engine_stats(ENGINE)


def test_chain_counts_match_the_fixture(chain):
    n = len(chain.samples["inputs_per_tx"])
    assert n == 100
    assert len(chain.samples["outputs_per_tx"]) == n
    assert chain.samples["inputs_per_tx"].min() >= 0
    assert chain.samples["outputs_per_tx"].min() >= 1
    assert abs(sum(chain.shares["tx_type_mix"].values()) - 1.0) < 1e-9
    assert (chain.samples["output_value_lovelace"] > 0).all()


def test_chain_gaps_are_nonnegative(chain):
    assert (chain.samples["gap_seconds"] >= 0).all()
    assert 0.0 <= chain.fractions["address_reuse"] <= 1.0
    assert 0.0 <= chain.fractions["change_to_sender"] <= 1.0


def test_engine_stats(engine):
    assert len(engine.samples["log10_output_value"]) == 200
    assert engine.shares["tx_type_mix"]["plain"] == 1.0
    assert 0.0 < engine.info["idle_fraction_of_records"] <= 1.0
    assert "inputs_per_tx" not in engine.samples


def test_fit_output_parses_as_yaml_with_profile_syntax(chain, tmp_path):
    text, target = fit.build(chain, "fixture-fit")
    doc = yaml.safe_load(text)
    p = doc["profiles"][0]
    assert doc["version"] == 1
    assert p["traits"]["competence"].startswith("beta(")
    assert p["traits"]["patience"].startswith("pareto(xm=")
    assert p["hazards"]["dormancy"].startswith("lognormal(")
    assert abs(sum(p["weights"].values()) - 1.0) < 1e-6
    assert p["rates"]["activity_tail"] > 1
    assert set(target["quantiles"]) == set(common.KS_STATS)
    assert len(target["quantiles"]["outputs_per_tx"]) == common.QUANTILE_POINTS


def test_fit_cli_writes_both_files(tmp_path):
    assert fit.main([CHAIN, "--out", str(tmp_path), "--name", "x"]) == 0
    assert (tmp_path / "profile.yaml").exists()
    json.loads((tmp_path / "target.json").read_text())


def test_fit_pareto_recovers_alpha():
    rng = np.random.default_rng(1)
    x = 10.0 * (1 - rng.random(200_000)) ** (-1 / 2.5)
    assert abs(fit.fit_pareto(x, 10.0) - 2.5) < 0.05


def test_fit_lognormal_recovers_parameters():
    rng = np.random.default_rng(2)
    mu, sigma = fit.fit_lognormal(rng.lognormal(3.0, 0.7, 100_000))
    assert abs(mu - 3.0) < 0.02 and abs(sigma - 0.7) < 0.02


def test_fit_beta_recovers_parameters():
    rng = np.random.default_rng(3)
    a, b = fit.fit_beta(rng.beta(5, 3, 100_000))
    assert abs(a - 5) < 0.2 and abs(b - 3) < 0.15


def test_ks_identical_samples_pass():
    rng = np.random.default_rng(4)
    x = rng.normal(size=5000)
    r = grade.ks(x, common.quantiles(x), 0.1)
    assert r["status"] == "pass" and r["D"] < 0.01


def test_ks_shifted_samples_fail():
    rng = np.random.default_rng(5)
    x = rng.normal(size=5000)
    r = grade.ks(x + 1.0, common.quantiles(x), 0.1)
    assert r["status"] == "fail" and r["D"] > 0.3 and r["p"] < 1e-6


def test_grade_a_run_against_its_own_fit_passes_ks(chain):
    _, target = fit.build(chain, "x")
    rows = {r["name"]: r for r in grade.grade(chain, target, 0.1, 0.1, 0.1)}
    for name in common.KS_STATS:
        assert rows[name]["status"] == "pass", name


def test_grade_flags_the_engine_against_the_chain_fit(chain, engine):
    _, target = fit.build(chain, "x")
    rows = {r["name"]: r for r in grade.grade(engine, target, 0.1, 0.1, 0.1)}
    assert rows["activity_per_day"]["status"] == "fail"
    assert rows["tx_type_mix"]["status"] == "fail"
    assert rows["outputs_per_tx"]["status"] == "skipped"
    assert rows["change_to_sender"]["status"] == "skipped"


def test_grade_cli_report_and_exit_code(chain, tmp_path):
    fit.main([CHAIN, "--out", str(tmp_path)])
    out = tmp_path / "report.md"
    code = grade.main([ENGINE, "--target", str(tmp_path / "target.json"), "--out", str(out)])
    text = out.read_text()
    assert code == 1
    assert "| activity_per_day | FAIL |" in text
    assert "Idle wake-ups" in text


def test_samples_are_sorted_so_a_fit_is_reproducible(chain, engine):
    """DuckDB returns unordered rows in a different order from run to run,
    and sums of floats depend on the order of their terms. Every sample is
    sorted, so the fitted numbers are the same to the last bit on every run."""
    for st in (chain, engine):
        for name, x in {**st.samples, **st.per_address}.items():
            assert (np.diff(x) >= 0).all(), name
