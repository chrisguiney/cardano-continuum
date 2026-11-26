"""chain_stats.py and engine_stats.py: what is measured from the fixtures."""

import sqlite3

import numpy as np

import chain_stats
import engine_stats
import units
from helpers import CHAIN, ENGINE
from unit_features import COLUMNS, unit_features


def test_chain_measures_every_transaction_of_the_fixture(chain):
    """Per-transaction samples have one value for each of the fixture's 100
    transactions; every transaction has an output; the kind shares sum to
    1; and a payment is never empty."""
    count = len(chain.samples["inputs_per_tx"])
    assert count == 100
    assert len(chain.samples["outputs_per_tx"]) == count
    assert chain.samples["inputs_per_tx"].min() >= 0
    assert chain.samples["outputs_per_tx"].min() >= 1
    assert abs(sum(chain.shares["tx_type_mix"].values()) - 1.0) < 1e-9
    assert (chain.samples["output_value_lovelace"] > 0).all()


def test_chain_gaps_and_fractions_are_in_range(chain):
    """Time between a unit's transactions is never negative, and the reuse
    and change fractions are fractions."""
    assert (chain.samples["gap_seconds"] >= 0).all()
    assert 0.0 <= chain.fractions["address_reuse"] <= 1.0
    assert 0.0 <= chain.fractions["change_to_sender"] <= 1.0


def test_chain_measures_per_address_when_no_unit_mapping_is_given(chain):
    """chain_stats without a unit mapping still has senders to count:
    every address is its own unit."""
    assert len(chain.samples["tx_count_per_address"]) > 0


def test_engine_measures_payments_only(engine):
    """An engine export yields one amount per payment and an all-plain mix,
    records how much of its time was idle, and has no input counts: those
    exist only on a chain."""
    assert len(engine.samples["log10_output_value"]) == 200
    assert engine.shares["tx_type_mix"]["plain"] == 1.0
    assert 0.0 < engine.info["idle_fraction_of_records"] <= 1.0
    assert "inputs_per_tx" not in engine.samples


def test_samples_are_sorted_so_a_fit_is_reproducible(chain, engine):
    """DuckDB returns unordered rows in a different order from run to run,
    and sums of floats depend on the order of their terms. Every sample is
    sorted, so the fitted numbers are the same to the last bit on every run."""
    for stats in (chain, engine):
        for name, sample in {**stats.samples, **stats.per_address}.items():
            assert (np.diff(sample) >= 0).all(), name


def test_actor_profiles_come_from_the_store_and_skip_unnamed_actors(tmp_path):
    """Genesis actors are listed only in the run's store.db; an actor with
    an empty profile name belongs to no cluster."""
    store = tmp_path / "store.db"
    db = sqlite3.connect(store)
    db.execute("CREATE TABLE actor (id INTEGER PRIMARY KEY, name TEXT, role TEXT, profile TEXT)")
    db.executemany("INSERT INTO actor VALUES (?, '', '', ?)", [(1, "a"), (2, "a"), (3, "b"), (4, "")])
    db.commit()
    db.close()
    assert engine_stats.actor_profiles(ENGINE, str(store)) == {1: "a", 2: "a", 3: "b"}


def fixture_units_table(slot_seconds: float = 1.0):
    last = chain_stats.last_slot(CHAIN)
    con = chain_stats.load_window(CHAIN, 0, last, units.unit_of(True))
    return unit_features(con, last + 1, slot_seconds)


def test_feature_table_has_the_documented_columns_and_one_row_per_unit():
    table = fixture_units_table()
    assert list(table.columns) == [column.name for column in COLUMNS]
    assert table["unit"].is_unique and (table["tx_count"] >= 1).all()


def test_active_days_count_days_not_slots():
    """active_days buckets slots into days by the slot length. With one-day
    slots every distinct slot is its own day, so no unit can have fewer active
    days than with one-second slots, and a unit spread over several slots of
    one day has more."""
    seconds = fixture_units_table(1.0)
    days = fixture_units_table(86400.0)
    assert (days["active_days"] >= seconds["active_days"]).all()
    assert (days["active_days"] > seconds["active_days"]).any()


def test_engine_type_mix_counts_each_submitted_intent_once(tmp_path):
    """A retried send is one transaction; a script call is script."""
    import duckdb
    import pandas as pd

    import engine_stats
    decisions = pd.DataFrame({
        "seq": [1, 2, 3, 4, 5, 6], "actor": [1, 1, 1, 2, 2, 1],
        "kind": ["IntentChosen", "Submitted", "IntentChosen", "IntentChosen", "Submitted", "Submitted"],
        "intent_id": [None, 10, None, None, 11, 10], "intent_kind": ["send", None, "idle", "script_call", None, None],
    })
    con = duckdb.connect()
    con.register("decision", decisions)
    mix = engine_stats.type_mix(con.execute(engine_stats.SUBMITTED_INTENTS_SQL).fetchall())
    assert mix["plain"] == 0.5 and mix["script"] == 0.5 and mix["gov"] == 0.0
