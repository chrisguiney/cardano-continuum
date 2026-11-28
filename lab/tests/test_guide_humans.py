"""guide_lib's humans toy and humans_run's summary: seeded, and the shapes the guide claims."""

import json
import os

import numpy as np

import guide_lib
import humans_run
from helpers import LAB


def test_humans_params_read_the_shipped_tiers():
    p = guide_lib.humans_params()
    assert [t["name"] for t in p["tiers"]] == ["low", "mid", "high", "whale"]
    assert abs(sum(t["share"] for t in p["tiers"]) - 1) < 1e-9
    assert p["invest"]["share"][0] == "beta"


def test_wallets_per_human_is_seeded_and_grows_with_tier():
    a, tiers = guide_lib.wallets_per_human(1, 5000)
    b, _ = guide_lib.wallets_per_human(1, 5000)
    assert np.array_equal(a, b)
    assert a.min() >= 1 and a.max() <= guide_lib.humans_params()["max_wallets"]
    by = {t: a[np.array(tiers) == t].mean() for t in ("low", "mid", "high", "whale")}
    assert by["low"] < by["mid"] < by["high"] < by["whale"]
    none, _ = guide_lib.wallets_per_human(1, 500, scale=0)
    assert (none == 1).all()


def test_household_never_overdraws_and_respects_the_buffer():
    p = guide_lib.humans_params()
    mid = p["tiers"][1]
    pay, ratio, fiat = guide_lib.tier_means(mid)
    b = guide_lib.budget_of(mid, p["invest"], pay, ratio, 0.2)
    h = guide_lib.household(3, b, fiat, 36)
    assert (h["fiat"] >= -1e-9).all()
    assert h["invested"].sum() > 0
    broke = guide_lib.household(3, guide_lib.budget_of({**mid, "layoff": 1, "hire": 0}, p["invest"], pay, ratio, 0.2), 0, 12)
    assert broke["invested"].sum() == 0 and broke["short"].sum() > 0


def test_worked_example_adds_up():
    w = guide_lib.worked_example("mid", 0.15)
    assert abs(w["left"] - (w["pay"] - w["bills"])) < 1e-9
    assert abs(w["each"] * w["steps"] - w["share"] * w["left"]) < 1e-9
    assert abs(w["ada_each"] - w["each"] / 0.15) < 1e-9


def test_committed_run_summary_has_every_chart():
    summary, source = guide_lib.humans_run()
    assert "seed-7" in source
    assert summary["humans"] == sum(summary["wallets_per_human"].values())
    assert set(summary["fiat_months"]) == set(summary["tiers"])
    assert summary["bought"] and summary["price"]


def test_summarise_reads_a_log_and_a_store(tmp_path):
    """A two-record log and a one-row store, written the way the engine writes them."""
    import sqlite3
    import struct

    def frame(rec):
        payload = b"\x02" + json.dumps(rec, separators=(",", ":")).encode()
        return struct.pack(">I", len(payload)) + b"\0\0\0\0" + payload

    with open(tmp_path / "events.log", "wb") as f:
        f.write(frame({"seq": 1, "at": 1000, "class": 2, "kind": "Income", "body": {"human": 1, "cents": 500, "employed": True}}))
        f.write(frame({"seq": 2, "at": 400000, "class": 2, "kind": "Quote", "body": {"ada": 0.2}}))
    con = sqlite3.connect(tmp_path / "store.db")
    con.execute("CREATE TABLE actor (id INTEGER, identity TEXT)")
    owner = {"human": 1, "tier": "mid", "wallets": [3, 4], "money": {"fiat0": 100, "income": 1000}}
    con.execute("INSERT INTO actor VALUES (3, ?)", (json.dumps({"owner": owner}),))
    con.commit()
    con.close()
    s = humans_run.summarise(str(tmp_path))
    assert s["wallets_per_human"] == {"2": 1}
    assert s["fiat_months"]["mid"][-1] == [300000, 0.6]
    assert s["price"] == [[300000, 0.2]]
    assert os.path.exists(os.path.join(LAB, "tests", "fixtures", "humans", "run.json"))
