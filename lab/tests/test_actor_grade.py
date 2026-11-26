"""Grading a run's non-wallet actors against the target's actors block."""

import os

import duckdb

import compare
import engine_stats

TARGET_ACTORS = {
    "pools": {"count": 4, "register_rate": 0.0, "retire_rate": 0.0, "update_rate": 0.5},
    "dreps": {"count": 4, "register_rate": 0.0, "retire_rate": 0.25, "vote_rate": 1.0, "voting_share": 0.5},
    "contracts": {"new_rate": 0.01, "top_share": 0.8},
    "proposal_rate": {"info": 1.0, "treasury_withdrawals": 0.5},
}


def by_name(rows):
    return {row.name: row for row in rows}


def measured(**over):
    m = engine_stats.ActorCounts(
        days=10.0, members={"pools": 4, "dreps": 4}, wallets=100,
        events={"pools.update_rate": 20, "pools.retire_rate": 0, "pools.register_rate": 0,
                "dreps.vote_rate": 40, "dreps.retire_rate": 10, "dreps.register_rate": 0,
                "contracts.new_rate": 10, "proposal_rate.info": 10, "proposal_rate.treasury_withdrawals": 5},
        voted={"dreps": 2}, top_share=0.75)
    for k, v in over.items():
        setattr(m, k, v)
    return m


def test_counts_at_the_target_rates_pass():
    rows = by_name(compare.compare_actors(measured(), TARGET_ACTORS))
    for name in ("pools.update_rate", "dreps.vote_rate", "dreps.retire_rate", "contracts.new_rate",
                 "proposal_rate.info", "proposal_rate.treasury_withdrawals", "dreps.voting_share",
                 "contracts.top_share", "pools.count", "dreps.count"):
        assert rows[name].status == "pass", (name, rows[name].detail())


def test_a_rate_far_off_fails_and_a_missing_kind_is_skipped():
    m = measured()
    m.events = {**m.events, "pools.update_rate": 200}
    m.members = {"dreps": 4}
    rows = by_name(compare.compare_actors(m, TARGET_ACTORS))
    assert rows["dreps.vote_rate"].status == "pass"
    assert rows["pools.update_rate"].status == "skipped"
    assert rows["pools.count"].status == "fail"
    m.members = {"pools": 4, "dreps": 4}
    assert by_name(compare.compare_actors(m, TARGET_ACTORS))["pools.update_rate"].status == "fail"


def test_a_target_without_an_actors_block_grades_nothing():
    assert compare.compare_actors(measured(), None) == []


def write_export(root, submissions, deployed, actors):
    """A minimal continuum export: submission and decision tables."""
    con = duckdb.connect()
    os.makedirs(os.path.join(root, "submission"))
    os.makedirs(os.path.join(root, "decision"))
    os.makedirs(os.path.join(root, "actor_transition"))
    rows = ", ".join(f"({i}, {at}, {actor}, {i}, 1, '{kind}', {action}, {contract})"
                     for i, (at, actor, kind, action, contract) in enumerate(submissions))
    con.execute(f"""COPY (SELECT * FROM (VALUES {rows}) t(seq, "at", actor, intent_id, attempt, kind, action, contract))
                    TO '{root}/submission/part-0.parquet' (FORMAT parquet)""")
    drows = ", ".join(f"({i}, {at}, 'Deployed')" for i, at in enumerate(deployed)) or "(0, 0, 'Tick')"
    con.execute(f"""COPY (SELECT * FROM (VALUES {drows}, (99, 864000000, 'Tick')) t(seq, "at", kind))
                    TO '{root}/decision/part-0.parquet' (FORMAT parquet)""")
    con.execute(f"""COPY (SELECT 1::UBIGINT AS actor, NULL::VARCHAR AS profile WHERE false)
                    TO '{root}/actor_transition/part-0.parquet' (FORMAT parquet)""")
    import sqlite3
    db = sqlite3.connect(os.path.join(os.path.dirname(root), "store.db"))
    db.execute("CREATE TABLE actor (id INTEGER PRIMARY KEY, name TEXT, role TEXT, profile TEXT)")
    db.executemany("INSERT INTO actor VALUES (?, '', '', ?)", list(actors.items()))
    db.commit()
    db.close()


def test_actor_counts_read_an_export(tmp_path):
    root = str(tmp_path / "run" / "export")
    os.makedirs(os.path.dirname(root))
    subs = [(1000, 1, "update_pool", "NULL", "NULL"), (2000, 2, "vote", "NULL", "NULL"),
            (3000, 2, "vote", "NULL", "NULL"), (4000, 9, "propose", "'info'", "NULL"),
            (5000, 5, "lock", "NULL", "1"), (6000, 6, "lock", "NULL", "1"), (7000, 7, "lock", "NULL", "NULL")]
    write_export(root, subs, deployed=[500], actors={1: "pools", 2: "dreps", 3: "dreps", 5: "w", 6: "w", 7: "w", 9: "network"})
    m = engine_stats.actor_counts(root, os.path.join(os.path.dirname(root), "store.db"), TARGET_ACTORS)
    assert m.members == {"pools": 1, "dreps": 2}
    assert m.wallets == 3
    assert abs(m.days - 10.0) < 1e-9
    assert m.events["pools.update_rate"] == 1 and m.events["dreps.vote_rate"] == 2
    assert m.events["proposal_rate.info"] == 1 and m.events["contracts.new_rate"] == 1
    assert m.voted == {"dreps": 1}
    # The busiest 1% of 2 contracts is one contract, with 2 of 3 calls.
    assert abs(m.top_share - 2 / 3) < 1e-9


def test_overall_grades_wallets_only():
    """Pools, DReps and the proposer are graded in the actors block; their
    transactions are already in the clusters' fitted mixes, so the overall
    wallet statistics leave them out."""
    import grade
    profiles = {1: "pools", 2: "dreps", 3: "network", 4: "holder", 5: "staker"}
    assert grade.wallet_actors(profiles, TARGET_ACTORS) == {4, 5}
    assert grade.wallet_actors({}, TARGET_ACTORS) is None
    assert grade.wallet_actors(profiles, None) is None


def test_births_are_not_members_at_start(tmp_path):
    root = str(tmp_path / "run" / "export")
    os.makedirs(os.path.dirname(root))
    write_export(root, [(1000, 1, "update_pool", "NULL", "NULL")], deployed=[], actors={1: "pools", 2: "pools"})
    con = duckdb.connect()
    con.execute(f"""COPY (SELECT 2::UBIGINT AS actor, 'pools' AS profile)
                    TO '{root}/actor_transition/part-0.parquet' (FORMAT parquet)""")
    m = engine_stats.actor_counts(root, os.path.join(os.path.dirname(root), "store.db"), TARGET_ACTORS)
    assert m.members["pools"] == 1 and m.events["pools.register_rate"] == 1


def test_output_value_leaves_out_token_quantities(tmp_path):
    """A mint's submitted amount is a token quantity, not lovelace, so the
    payment sizes leave mints and burns out."""
    root = str(tmp_path / "run" / "export")
    os.makedirs(os.path.dirname(root))
    write_export(root, [(1000, 5, "mint", "NULL", "NULL"), (2000, 5, "lock", "NULL", "NULL")], deployed=[], actors={5: "w"})
    con = duckdb.connect()
    con.execute(f"""COPY (SELECT * FROM (VALUES (0, 1000, 5, 0, 1, 'mint', 1000), (1, 2000, 5, 1, 1, 'lock', 5000000),
                    (2, 3000, 5, 2, 1, NULL, 2000000)) t(seq, "at", actor, intent_id, attempt, kind, amount))
                    TO '{root}/submission/part-0.parquet' (FORMAT parquet)""")
    con = engine_stats._open_export(root, None)
    values = sorted(engine_stats._column(con, engine_stats.LOVELACE_AMOUNT_SQL))
    assert list(values) == [2000000, 5000000]
