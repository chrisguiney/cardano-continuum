"""guide_lib's market toy: seeded, bounded, and the shapes the guide claims."""

import numpy as np

import guide_lib


def test_rate_path_is_seeded_and_starts_at_start():
    a, b = guide_lib.rate_path(3, 2), guide_lib.rate_path(3, 2)
    assert np.array_equal(a.ada, b.ada)
    assert not np.array_equal(a.ada, guide_lib.rate_path(4, 2).ada)
    assert a.ada[0] == a.params["ada"]["start"]
    bid, ask = a.quotes(a.params["exchanges"][0], "USD")
    assert (bid < ask).all()


def test_extra_currencies_are_geometric():
    drawn = guide_lib.extra_currencies(1, 20000, 0.15, 6)
    shares = np.bincount(drawn) / len(drawn)
    assert abs(shares[0] - 0.85) < 0.01
    assert shares[0] > shares[1] > shares[2]


def test_factors_stay_in_bounds_and_hodl_damps():
    p = guide_lib.rate_path(4, 6)
    b, d = p.params["births"], p.params["deaths"]
    bf, df, hf = guide_lib.birth_factor(p, "EUR"), guide_lib.death_factor(p, "EUR"), guide_lib.death_factor(p, "EUR", hodl=True)
    assert bf.min() >= b["min"] and bf.max() <= b["max"]
    assert df.min() >= d["min"] and df.max() <= d["max"]
    assert (np.abs(np.log(hf)) <= np.abs(np.log(df)) + 1e-12).all()


def test_drawdown_split_hodlers_never_sell_and_depth_sells_more():
    shallow = guide_lib.drawdown_split(1, depth=-0.12)
    deep = guide_lib.drawdown_split(1, depth=-0.5)
    assert guide_lib.sell_score(-0.5, 1, 0, True) == 0
    assert deep["sold"] > shallow["sold"]
    assert guide_lib.largest_drawdown(np.array([1.0, 2, 1, 0.5, 3]))[:2] == (1, 3)
