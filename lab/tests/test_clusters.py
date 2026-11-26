import json
import os
import shutil
import sqlite3
import sys

import duckdb
import numpy as np
import pandas as pd
import pytest
import yaml

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.dirname(HERE))
import cluster  # noqa: E402
import common  # noqa: E402
import fit  # noqa: E402
import grade  # noqa: E402
import units  # noqa: E402

CHAIN = os.path.join(HERE, "fixtures", "chain")
ENGINE = os.path.join(HERE, "fixtures", "engine")
EXAMPLE = os.path.join(os.path.dirname(HERE), "example", "clustered.yaml")

_CHARSET = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"


def bech32_like(prefix: str, raw: bytes) -> str:
    """A bech32-shaped string with a dummy checksum; units ignores checksums."""
    acc = bits = 0
    out = []
    for b in raw:
        acc = (acc << 8) | b
        bits += 8
        while bits >= 5:
            bits -= 5
            out.append(_CHARSET[(acc >> bits) & 31])
    if bits:
        out.append(_CHARSET[(acc << (5 - bits)) & 31])
    return prefix + "1" + "".join(out) + "qqqqqq"


def blobs(n=300, seed=0):
    rng = np.random.RandomState(seed)
    centres = np.array([[0.0] * 10, [6.0] * 10, [-6.0, 6.0] * 5])
    return np.vstack([rng.normal(c, 0.5, size=(n, 10)) for c in centres])


def test_bic_picks_three_blobs():
    f = cluster.fit_gmm(blobs(), kmin=2, kmax=6, seed=3)
    assert f.k == 3
    assert sorted(f.bic) == [2, 3, 4, 5, 6]
    assert f.bic[3] == min(f.bic.values())


def test_clustering_is_deterministic_for_a_seed():
    z = blobs()
    a = cluster.fit_gmm(z, 2, 5, seed=11)
    b = cluster.fit_gmm(z, 2, 5, seed=11)
    assert a.bic == b.bic
    assert (a.model.predict(z) == b.model.predict(z)).all()
    assert (cluster.sample_rows(1000, 100, 5) == cluster.sample_rows(1000, 100, 5)).all()
    assert len(set(cluster.sample_rows(1000, 100, 5))) == 100


def test_relabel_orders_by_first_feature():
    centers = np.array([[3.0, 0], [-1.0, 0], [0.5, 0]])
    labels, order = cluster.relabel(np.array([0, 1, 2, 1]), centers)
    assert list(order) == [1, 2, 0]
    assert list(labels) == [2, 0, 1, 0]


def pinned_units(n=1500, seed=0):
    """Units from three behaviours whose share features sit exactly at 0 or 1,
    as a single-transaction unit's do."""
    rng = np.random.default_rng(seed)
    rows = []
    for tpd, val, script, reuse, size in [(2.0, 5e6, 0.0, 1.0, n), (0.01, 2e9, 0.0, 0.0, n),
                                          (0.3, 3e7, 1.0, 0.0, n // 2), (0.1, 8e7, 0.0, 1.0, n)]:
        m = size
        rows.append(pd.DataFrame({
            "tx_per_day": tpd * rng.lognormal(0, 0.5, m),
            "med_gap_s": 86400 / tpd * rng.lognormal(0, 0.5, m),
            "med_value": val * rng.lognormal(0, 0.5, m),
            "script_share": np.where(rng.random(m) < 0.97, script, 1 - script),
            "stake_share": np.zeros(m),
            "in_per_tx": 1 + 2 * tpd * rng.lognormal(0, 0.4, m), "out_per_tx": 2 * rng.lognormal(0, 0.4, m),
            "reuse": np.where(rng.random(m) < 0.97, reuse, 1 - reuse),
            "active_days": 30 * tpd ** 0.3 * rng.lognormal(0, 0.4, m),
            "max_dormancy_s": 1e5 / tpd * rng.lognormal(0, 0.5, m),
            "n_tx": np.ceil(tpd * 90 * rng.lognormal(0, 0.4, m)),
        }))
    return pd.concat(rows, ignore_index=True)


def test_pinned_shares_do_not_drive_k_to_the_top_of_the_range():
    r = cluster.cluster_units(pinned_units(), kmin=2, kmax=10, seed=2)
    assert r.fit.interior, r.fit.bic
    assert sorted(r.fit.bic) == list(range(2, 11))
    assert 3 <= r.fit.k <= 5  # three general behaviours; the script group is a segment
    assert r.k == r.fit.k + 1


def test_elbow_is_the_first_flat_step_and_min_is_reported():
    bic = {2: 1000.0, 3: 500.0, 4: 200.0, 5: 190.0, 6: 150.0, 7: 160.0}
    assert cluster.elbow(bic) == 4
    assert cluster.elbow({2: 10.0, 3: 5.0, 4: 0.0}) == 4  # never flattens: top of range
    f = cluster.fit_gmm(blobs(), kmin=2, kmax=6, seed=3, select="min")
    assert f.k == f.k_min == 3 and f.interior
    g = cluster.Fit(f.model, 2, {2: 3.0, 3: 2.0, 4: 1.0})
    assert g.k_min == 4 and not g.interior


def test_cluster_names_are_short_unique_and_stable_for_a_seed():
    df = pinned_units()
    a = cluster.cluster_units(df, kmin=2, kmax=8, seed=2)
    b = cluster.cluster_units(df, kmin=2, kmax=8, seed=2)
    assert a.names == b.names and (a.labels == b.labels).all()
    assert len(a.names) == a.k == len(set(a.names))
    assert all(len(n) <= 24 and n == n.lower() for n in a.names)
    assert "script-user" in a.names and "holder" in a.names


def test_names_use_absolute_thresholds_in_real_units():
    base = dict(tx_per_day=0.31, med_gap_s=1e5, med_value=5e6, script_share=0.0, stake_share=0.0,
                in_per_tx=1.0, out_per_tx=2.0, reuse=0.0, active_days=10, max_dormancy_s=0.0, n_tx=28)
    mk = lambda **kw: pd.DataFrame([{**base, **kw}] * 5)
    cases = [(dict(tx_per_day=0.31), "occasional-payer"), (dict(tx_per_day=3.0), "frequent-trader"),
             (dict(tx_per_day=0.002), "holder"), (dict(script_share=0.9), "script-user"),
             (dict(stake_share=0.9), "staker")]
    for kw, want in cases:
        assert cluster.name_clusters(mk(**kw), np.zeros(5, dtype=int)) == [want]
    df = pd.concat([mk(), mk(tx_per_day=0.4, med_value=5e9)], ignore_index=True)
    names = cluster.name_clusters(df, np.array([0] * 5 + [1] * 5))
    assert len(set(names)) == 2


def test_unit_is_the_stake_credential_when_there_is_one():
    pay1, pay2, stake = bytes([1]) * 28, bytes([2]) * 28, bytes([9]) * 28
    a1 = bech32_like("addr_test", bytes([0x00]) + pay1 + stake)
    a2 = bech32_like("addr_test", bytes([0x00]) + pay2 + stake)
    script_stake = bech32_like("addr_test", bytes([0x20]) + pay1 + stake)
    enterprise = bech32_like("addr_test", bytes([0x60]) + pay1)
    assert units.unit_key(a1) == units.unit_key(a2) == "k:" + stake.hex()
    assert units.unit_key(script_stake) == "s:" + stake.hex()
    assert units.unit_key(enterprise) == enterprise
    assert units.unit_key("Ae2tdPwUPEZ5zk", stake=True) == "Ae2tdPwUPEZ5zk"
    assert units.unit_key(a1, stake=False) == a1


def test_chain_units_default_to_addresses():
    st = common.chain_stats(CHAIN)
    assert len(st.samples["tx_count_per_address"]) > 0


def test_feature_table_and_clustered_fit_on_the_fixture(tmp_path):
    last = common.last_slot(CHAIN)
    con = common.prepare(CHAIN, 0, last, units.unit_of(True))
    df = units.features(con, last + 1)
    assert set(df.columns) == {"unit", "n_tx", "tx_per_day", "med_gap_s", "med_value", "script_share",
                               "stake_share", "in_per_tx", "out_per_tx", "reuse", "active_days", "max_dormancy_s"}
    assert df["unit"].is_unique and (df["n_tx"] >= 1).all()
    x = cluster.transform(df)
    assert x.shape == (len(df), len(cluster.FEATURES)) and np.isfinite(x).all()
    code = fit.main([CHAIN, "--out", str(tmp_path), "--clusters", "--network", "fixt", "--kmin", "2", "--kmax", "3",
                     "--window-days", "36500", "--seed", "4", "--m2pq-version", "t1"])
    assert code == 0
    doc = yaml.safe_load((tmp_path / "fixt.yaml").read_text())
    assert len(doc["profiles"]) >= 2
    tgt = json.loads((tmp_path / "fixt.target.json").read_text())
    assert tgt["header"]["seed"] == 4 and set(tgt["clusters"]) == {p["name"] for p in doc["profiles"]}


def synthetic_stats(seed, scale):
    rng = np.random.default_rng(seed)
    st = common.Stats()
    n = 2000
    st.samples["output_value_lovelace"] = rng.lognormal(scale, 1.0, n)
    st.samples["log10_output_value"] = np.log10(st.samples["output_value_lovelace"])
    st.samples["tx_count_per_address"] = np.ceil(rng.pareto(1.5, 300) + 1)
    st.samples["activity_per_day"] = st.samples["tx_count_per_address"] / 90
    st.samples["gap_seconds"] = rng.lognormal(8, 2, n)
    st.samples["log10_gap_seconds"] = np.log10(st.samples["gap_seconds"])
    st.samples["inputs_per_tx"] = rng.integers(1, 4, n).astype(float)
    st.samples["outputs_per_tx"] = rng.integers(1, 4, n).astype(float)
    st.shares["tx_type_mix"] = {"plain": 0.9, "script": 0.1, "stake": 0.0, "pool": 0.0}
    st.fractions = {"address_reuse": 0.3, "change_to_sender": 0.6}
    st.per_address = {"change_to_sender": rng.beta(3, 2, 200), "address_reuse": rng.beta(2, 5, 200)}
    st.info = {"span_days": 90.0, "txs": float(n), "active_fraction": 0.2}
    return st


HEADER = {"network": "synthetic", "window": "90 days", "seed": 1, "k": 2, "bic": {"2": 1.0, "3": 2.0},
          "unit": "stake credential", "weighting": "addresses", "mithril2pq_version": "t", "mithril2pq_last_chunk": 1}


def test_cluster_fit_emits_valid_profile_yaml():
    a, b = synthetic_stats(1, 16.0), synthetic_stats(2, 13.0)
    overall = synthetic_stats(3, 15.0)
    text, target = fit.build_clusters(overall, {"high-freq-small-value": a, "low-freq-large-value": b},
                                      {"high-freq-small-value": 0.7, "low-freq-large-value": 0.3}, HEADER)
    doc = yaml.safe_load(text)
    assert doc["version"] == 1 and [p["name"] for p in doc["profiles"]] == ["high-freq-small-value", "low-freq-large-value"]
    assert abs(sum(p["weight"] for p in doc["profiles"]) - 1.0) < 1e-6
    for p in doc["profiles"]:
        assert p["amount"]["body"].startswith("lognormal(")
        assert p["amount"]["tail"].startswith("pareto(xm=")
        assert 0 < p["amount"]["tail_share"] <= 1
        assert p["budget"] >= fit.MIN_BUDGET
        assert p["rates"]["activity_tail"] >= fit.MIN_TAIL
    assert text.startswith("# Fitted by lab/fit.py")
    for key in ("network: synthetic", "seed: 1", "k: 2", "bic: 2=1.0", "unit:", "weighting: addresses",
                "mithril2pq_version: t", "mithril2pq_last_chunk: 1", "window: 90 days"):
        assert f"# {key}" in text, key
    assert set(target["clusters"]) == {"high-freq-small-value", "low-freq-large-value"}
    assert "quantiles" in target["overall"]
    if os.environ.get("UPDATE_EXAMPLE"):
        with open(EXAMPLE, "w") as f:
            f.write(text)


def test_fit_trunc_lognormal_recovers_a_cut_body():
    rng = np.random.default_rng(8)
    x = rng.lognormal(10.0, 2.0, 400_000)
    xm = float(np.quantile(x, 0.9))
    mu, sigma = fit.fit_trunc_lognormal(x[x < xm], xm)
    assert abs(mu - 10.0) < 0.05 and abs(sigma - 2.0) < 0.05
    naive_mu, naive_sigma = fit.fit_lognormal(x[x < xm])
    assert naive_sigma < 1.8  # the cut sample's own spread is too small


def write_export(dst, profile_hash: bytes):
    shutil.copytree(ENGINE, dst)
    con = duckdb.connect()
    os.makedirs(os.path.join(dst, "run"))
    con.register("h", pd.DataFrame({"profile_hash": [profile_hash]}))
    con.execute(f"COPY (SELECT profile_hash FROM h) TO '{dst}/run/part-000000.parquet' (FORMAT parquet)")


def test_grade_picks_the_target_from_the_run_hash(tmp_path):
    h = bytes(range(32))
    exp = tmp_path / "export"
    write_export(str(exp), h)
    prof = tmp_path / "profiles"
    prof.mkdir()
    (prof / "t.target.json").write_text("{}")
    (prof / "index.json").write_text(json.dumps({h.hex(): {"profile": "t", "target": "t.target.json"}}))
    assert grade.find_target(str(exp), str(prof)) == str(prof / "t.target.json")
    (prof / "index.json").write_text(json.dumps({"00" * 32: {"profile": "x", "target": "x.json"}}))
    with pytest.raises(SystemExit, match="not in"):
        grade.find_target(str(exp), str(prof))


def test_grade_reads_actor_profiles_from_the_store(tmp_path):
    store = tmp_path / "store.db"
    db = sqlite3.connect(store)
    db.execute("CREATE TABLE actor (id INTEGER PRIMARY KEY, name TEXT, role TEXT, profile TEXT)")
    db.executemany("INSERT INTO actor VALUES (?, '', '', ?)", [(1, "a"), (2, "a"), (3, "b"), (4, "")])
    db.commit()
    db.close()
    assert common.actor_profiles(ENGINE, str(store)) == {1: "a", 2: "a", 3: "b"}


def test_grade_grades_each_cluster_separately(tmp_path):
    exp = tmp_path / "run" / "export"
    exp.parent.mkdir()
    write_export(str(exp), bytes(32))
    con = duckdb.connect()
    actors = [r[0] for r in con.execute(f"SELECT DISTINCT actor FROM read_parquet('{exp}/submission/*.parquet') ORDER BY 1").fetchall()]
    assert len(actors) >= 2
    db = sqlite3.connect(exp.parent / "store.db")
    db.execute("CREATE TABLE actor (id INTEGER PRIMARY KEY, name TEXT, role TEXT, profile TEXT)")
    db.executemany("INSERT INTO actor VALUES (?, '', '', ?)", [(a, "a" if i % 2 else "b") for i, a in enumerate(actors)])
    db.commit()
    db.close()
    st_a = common.engine_stats(str(exp), {a for i, a in enumerate(actors) if i % 2})
    eng = common.engine_stats(str(exp))
    tgt = {"version": 1, "header": {},
           "overall": {"quantiles": {k: common.quantiles(eng.samples[k]) for k in common.KS_STATS if k in eng.samples
                                      and len(eng.samples[k])}, "shares": eng.shares, "fractions": eng.fractions},
           "clusters": {}}
    for name, s in (("a", st_a), ("b", None)):
        s = s or eng
        tgt["clusters"][name] = {"share": 0.5, "shares": s.shares, "fractions": s.fractions,
                                 "quantiles": {k: common.quantiles(s.samples[k]) for k in common.KS_STATS
                                               if len(s.samples.get(k, []))}}
    # inputs/outputs per tx do not exist for an engine run; give them any quantiles
    for t in [tgt["overall"], *tgt["clusters"].values()]:
        for k in common.KS_STATS:
            t["quantiles"].setdefault(k, common.quantiles(np.array([1.0, 2.0])))
    path = tmp_path / "t.json"
    path.write_text(json.dumps(tgt))
    out = tmp_path / "report.md"
    code = grade.main([str(exp), "--target", str(path), "--out", str(out)])
    text = out.read_text()
    assert "## Cluster a" in text and "## Cluster b" in text and "## Overall" in text
    assert "| log10_output_value | PASS |" in text
    assert code in (0, 1)


def test_amount_with_a_tie_at_the_quantile_has_no_tail():
    x = np.full(1000, 2_000_000.0)
    bt = fit.fit_body_tail(x)
    assert bt["tail_share"] == 0.0
    st = synthetic_stats(1, 16.0)
    st.samples["output_value_lovelace"] = x
    text = "\n".join(fit.profile_lines("p", fit.fit_params(st)))
    assert "      tail:" not in text and "amount:\n      body: lognormal(" in text


def test_fit_trunc_lognormal_survives_ties_on_the_bound():
    x = np.full(1000, 2_000_000.0)
    mu, sigma = fit.fit_trunc_lognormal(x, 2_000_000.0)
    assert mu <= np.log(2_000_000.0) and 0 < sigma <= 10


def test_dormancy_onset_leaves_the_active_fraction():
    mu, sigma, f = 12.0, 0.5, 0.05
    onset = fit.dormancy_onset(f, mu, sigma)
    active = 1 / onset
    dormant = np.exp(mu + sigma**2 / 2)
    assert abs(active / (active + dormant) - f) < 1e-9
    assert fit.dormancy_onset(1.0, mu, sigma) == fit.MIN_ONSET


def test_activity_rate_is_per_active_second():
    st = synthetic_stats(1, 16.0)
    st.info["active_fraction"] = 0.1
    a = fit.fit_activity(st)
    st.info["active_fraction"] = 1.0
    assert abs(a["base_per_sec"] / fit.fit_activity(st)["base_per_sec"] - 10.0) < 1e-9


def test_wakeup_rate_compensates_for_the_hodl_send_score():
    st = synthetic_stats(1, 16.0)
    fp = fit.fit_params(st)
    p = fit.send_probability(fit.mix_weights(st.shares["tx_type_mix"])["send"])
    assert abs(p - 0.02 * 0.9 / (1 + 0.02 * 0.9)) < 1e-12
    assert abs(fp["activity"]["base_per_sec"] * p - fit.fit_activity(st)["base_per_sec"]) < 1e-18
