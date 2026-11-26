"""cluster.py: choosing the number of clusters, labelling and naming."""

import numpy as np
import pandas as pd

import cluster
from helpers import three_blobs, units_with_pinned_shares


def test_bic_is_lowest_at_the_true_number_of_blobs():
    """On three clean blobs the BIC curve covers the searched range and its
    minimum, which the elbow also picks, is at three."""
    fit = cluster.fit_mixtures(three_blobs(), kmin=2, kmax=6, seed=3)
    assert fit.k == 3
    assert sorted(fit.bic) == [2, 3, 4, 5, 6]
    assert fit.bic[3] == min(fit.bic.values())


def test_clustering_is_deterministic_for_a_seed():
    """The same seed gives the same BIC curve, the same assignment and the
    same sample of rows; the sample has no repeats."""
    z = three_blobs()
    first = cluster.fit_mixtures(z, 2, 5, seed=11)
    second = cluster.fit_mixtures(z, 2, 5, seed=11)
    assert first.bic == second.bic
    assert (first.model.predict(z) == second.model.predict(z)).all()
    assert (cluster.sample_rows(1000, 100, 5) == cluster.sample_rows(1000, 100, 5)).all()
    assert len(set(cluster.sample_rows(1000, 100, 5))) == 100


def test_clusters_are_numbered_by_their_first_feature():
    """EM numbers components arbitrarily; renumbering by the first feature's
    centre (lowest first) makes labels comparable between runs."""
    centers = np.array([[3.0, 0], [-1.0, 0], [0.5, 0]])
    labels, order = cluster.order_by_first(np.array([0, 1, 2, 1]), centers)
    assert list(order) == [1, 2, 0]
    assert list(labels) == [2, 0, 1, 0]


def test_pinned_shares_do_not_drive_k_to_the_top_of_the_range():
    """Shares stuck at exactly 0 or 1 are kept out of the mixture, so BIC
    turns upward inside the range instead of buying ever narrower
    components. The script users form a segment on top of the mixture's k."""
    result = cluster.cluster_units(units_with_pinned_shares(), kmin=2, kmax=10, seed=2)
    assert result.fit.interior, result.fit.bic
    assert sorted(result.fit.bic) == list(range(2, 11))
    assert 3 <= result.fit.k <= 5  # three general behaviours; the script group is a segment
    assert result.k == result.fit.k + 1


def test_elbow_is_the_first_flat_step():
    """The elbow is the first k whose next step gains under 5% of the whole
    drop (here 4: the step to 5 gains 10 of 850), and the last k when the
    curve never flattens."""
    bic = {2: 1000.0, 3: 500.0, 4: 200.0, 5: 190.0, 6: 150.0, 7: 160.0}
    assert cluster.elbow(bic) == 4
    assert cluster.elbow({2: 10.0, 3: 5.0, 4: 0.0}) == 4


def test_bic_minimum_and_whether_it_is_interior_are_reported():
    """select="min" takes the BIC minimum. interior is true only when that
    minimum is strictly inside the searched range; a curve still falling at
    the last k is not interior."""
    fit = cluster.fit_mixtures(three_blobs(), kmin=2, kmax=6, seed=3, select="min")
    assert fit.k == fit.k_min == 3 and fit.interior
    still_falling = cluster.MixtureFit(fit.model, 2, {2: 3.0, 3: 2.0, 4: 1.0})
    assert still_falling.k_min == 4 and not still_falling.interior


def test_cluster_names_are_short_unique_and_stable_for_a_seed():
    table = units_with_pinned_shares()
    first = cluster.cluster_units(table, kmin=2, kmax=8, seed=2)
    second = cluster.cluster_units(table, kmin=2, kmax=8, seed=2)
    assert first.names == second.names and (first.labels == second.labels).all()
    assert len(first.names) == first.k == len(set(first.names))
    assert all(len(name) <= 24 and name == name.lower() for name in first.names)
    assert "script-user" in first.names and "holder" in first.names


def test_names_use_absolute_thresholds_in_real_units():
    """A cluster's name depends only on its own units (transactions per
    day, script, governance and stake shares), never on the other clusters, so a name
    means the same on every network. Two clusters with the same behaviour
    still get different names."""
    typical = dict(tx_per_day=0.31, median_gap_seconds=1e5, median_payment_lovelace=5e6, script_share=0.0,
                   stake_share=0.0, gov_share=0.0, inputs_per_tx=1.0, outputs_per_tx=2.0, reuse_share=0.0, active_days=10,
                   longest_gap_seconds=0.0, tx_count=28)

    def five_units(**changes) -> pd.DataFrame:
        return pd.DataFrame([{**typical, **changes}] * 5)

    cases = [(dict(tx_per_day=0.31), "occasional-payer"), (dict(tx_per_day=3.0), "frequent-trader"),
             (dict(tx_per_day=0.002), "holder"), (dict(script_share=0.9), "script-user"),
             (dict(stake_share=0.9), "staker"), (dict(gov_share=0.9), "voter"),
             (dict(gov_share=0.5, stake_share=0.5), "voter")]
    for changes, want in cases:
        assert cluster.name_clusters(five_units(**changes), np.zeros(5, dtype=int)) == [want]
    two_clusters = pd.concat([five_units(), five_units(tx_per_day=0.4, median_payment_lovelace=5e9)],
                             ignore_index=True)
    names = cluster.name_clusters(two_clusters, np.array([0] * 5 + [1] * 5))
    assert len(set(names)) == 2
