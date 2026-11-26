"""fit.py: the files a fit writes parse as profiles and carry a target."""

import json
import os

import numpy as np
import yaml

import actor_fit
import actor_stats
import chain_stats
import cluster
import fit
import population_fit
import units
from helpers import CHAIN, EXAMPLE, synthetic_stats
from population_stats import KS_STATS, QUANTILE_POINTS
from unit_features import unit_features

DAY = 86_400
# Two pools (one updates), one DRep that votes, two contracts, one proposal.
ACTORS = actor_fit.fit_actors(
    actor_stats.ActorEvents(
        pool_events=[("a", 1, "register", 0, 170_000_000, 0.0, None), ("b", 2, "register", 9, 340_000_000, 0.02, None),
                     ("a", DAY, "register", 5, 170_000_000, 0.01, None)],
        drep_events=[("d", 3, "register")], drep_votes=[("d", DAY)], proposals=[("info", DAY)],
        contract_use=[1, 3], new_contracts=1),
    first_slot=10, last_slot=90 * DAY, days=90.0, units=50, network="preview")
HEADER = {"network": "synthetic", "window": "90 days", "seed": 1, "k": 2, "bic": {"2": 1.0, "3": 2.0},
          "unit": "stake credential", "weighting": "addresses", "mithril2pq_version": "t", "mithril2pq_last_chunk": 1}


def test_single_profile_uses_the_profile_syntax_and_a_full_target(chain):
    """The YAML is what internal/profile parses (distribution calls as
    strings, weights summing to 1, an activity tail above 1), and the
    target holds 1001 quantiles for every KS statistic."""
    text, target = fit.fit_profile(chain, "fixture-fit")
    doc = yaml.safe_load(text)
    profile = doc["profiles"][0]
    assert doc["version"] == 1
    assert profile["traits"]["competence"].startswith("beta(")
    assert profile["traits"]["patience"].startswith("pareto(xm=")
    assert profile["hazards"]["dormancy"].startswith("lognormal(")
    assert abs(sum(profile["weights"].values()) - 1.0) < 1e-6
    assert profile["rates"]["activity_tail"] > 1
    assert set(target["quantiles"]) == set(KS_STATS)
    assert len(target["quantiles"]["outputs_per_tx"]) == QUANTILE_POINTS


def test_cli_writes_the_profile_and_the_target(tmp_path):
    assert fit.main([CHAIN, "--out", str(tmp_path), "--name", "x"]) == 0
    assert (tmp_path / "profile.yaml").exists()
    json.loads((tmp_path / "target.json").read_text())


def test_clustered_profile_is_valid_yaml_with_weights_and_a_header():
    """One profile per cluster in name order, weights summing to 1, a body
    and a tail for each amount, budget and activity tail within their
    limits, a header that records how the fit was made, and a target with
    the overall statistics and each cluster's."""
    small_payers, large_payers = synthetic_stats(1, 16.0), synthetic_stats(2, 13.0)
    overall = synthetic_stats(3, 15.0)
    text, target = fit.fit_cluster_profiles(
        overall, {"high-freq-small-value": small_payers, "low-freq-large-value": large_payers},
        {"high-freq-small-value": 0.7, "low-freq-large-value": 0.3}, dict(HEADER), ACTORS)
    doc = yaml.safe_load(text)
    assert [p["kind"] for p in doc["populations"]] == ["pool", "drep", "contract"]
    assert doc["populations"][0]["count"] == 2 and doc["network"]["proposal_rate"]["info"] > 0
    assert set(target["actors"]) == {"pools", "dreps", "contracts", "proposal_rate"}
    assert "# pools: 2 of 2 (scale 1)" in text and "# agendas: " in text
    assert doc["version"] == 1
    assert [p["name"] for p in doc["profiles"]] == ["high-freq-small-value", "low-freq-large-value"]
    assert abs(sum(p["weight"] for p in doc["profiles"]) - 1.0) < 1e-6
    for profile in doc["profiles"]:
        assert profile["amount"]["body"].startswith("lognormal(")
        assert profile["amount"]["tail"].startswith("pareto(xm=")
        assert 0 < profile["amount"]["tail_share"] <= 1
        assert profile["budget"] >= population_fit.MIN_BUDGET
        assert profile["rates"]["activity_tail"] >= population_fit.MIN_TAIL
    assert text.startswith("# Fitted by lab/fit.py")
    for key in ("network: synthetic", "seed: 1", "k: 2", "bic: 2=1.0", "unit:", "weighting: addresses",
                "mithril2pq_version: t", "mithril2pq_last_chunk: 1", "window: 90 days"):
        assert f"# {key}" in text, key
    assert set(target["clusters"]) == {"high-freq-small-value", "low-freq-large-value"}
    assert "quantiles" in target["overall"]
    # lab/profile_test.go parses the example with the Go profile package.
    if os.environ.get("UPDATE_EXAMPLE"):
        with open(EXAMPLE, "w") as f:
            f.write(text)


def test_clustered_fit_of_the_fixture_runs_end_to_end(tmp_path):
    """From Parquet to files: the feature matrix is finite with one column
    per feature, and the CLI writes at least two profiles whose names match
    the target's clusters and whose header records the seed."""
    last = chain_stats.last_slot(CHAIN)
    con = chain_stats.load_window(CHAIN, 0, last, units.unit_of(True))
    table = unit_features(con, last + 1)
    features = cluster.to_log_features(table)
    assert features.shape == (len(table), len(cluster.FEATURES)) and np.isfinite(features).all()
    code = fit.main([CHAIN, "--out", str(tmp_path), "--clusters", "--network", "fixt", "--kmin", "2", "--kmax", "3",
                     "--window-days", "36500", "--seed", "4", "--m2pq-version", "t1"])
    assert code == 0
    doc = yaml.safe_load((tmp_path / "fixt.yaml").read_text())
    assert len(doc["profiles"]) >= 2
    target = json.loads((tmp_path / "fixt.target.json").read_text())
    assert target["header"]["seed"] == 4
    assert set(target["clusters"]) == {p["name"] for p in doc["profiles"]}
