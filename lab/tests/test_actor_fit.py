"""actor_fit.py and actor_stats.py: the non-wallet populations.

Pools and DReps are replayed from their certificates, so the cases below
are small histories whose answer can be counted by hand.
"""

import os

import duckdb
import numpy as np
import pandas as pd
import pytest

import actor_fit
import actor_stats
import chain_stats
import units
from cardano import epoch_of_slot

DAY = 86_400


def register(pool, slot, pledge=0, cost=170_000_000, margin=0.0):
    return (pool, slot, "register", pledge, cost, margin, None)


def retire(pool, slot, epoch):
    return (pool, slot, "retire", None, None, None, epoch)


def test_pools_are_replayed_from_their_certificates():
    """a registers before the window and updates in it; b is new in the
    window; c retired before the window; d announced a retirement for an
    epoch after the window's end, so it still counts; e re-registered after
    announcing, which cancels the retirement."""
    first, last = 100 * DAY, 190 * DAY
    end_epoch = epoch_of_slot(last, "preview")
    rows = [
        register("a", 10, pledge=5), register("c", 20), retire("c", 30, 0), register("d", 40), register("e", 50),
        register("a", first + 5, pledge=7), register("b", first + 6, cost=340_000_000),
        retire("d", first + 7, end_epoch + 2), retire("e", first + 8, 0), register("e", first + 9),
    ]
    fit = actor_fit.fit_pools(rows, first, last, 90.0, "preview")
    assert fit.chain_count == 4  # a, b, d, e
    assert fit.register_rate == pytest.approx(1 / (4 * 90))   # b
    assert fit.update_rate == pytest.approx(2 / (4 * 90))     # a, e
    assert fit.retire_rate == pytest.approx(2 / (4 * 90))     # d, e announced
    assert fit.pledge.share == pytest.approx(0.75)            # b, d, e pledge nothing; a pledges 7
    assert fit.cost.point == 170_000_000 and fit.cost.share == pytest.approx(0.75)


def test_pool_count_is_cut_to_one_machine_and_the_scale_recorded():
    rows = [register(f"p{i:03d}", i) for i in range(300)]
    fit = actor_fit.fit_pools(rows, 0, 90 * DAY, 90.0, "mainnet")
    assert (fit.count, fit.chain_count) == (actor_fit.MAX_POOLS, 300)
    assert fit.scale == pytest.approx(actor_fit.MAX_POOLS / 300)
    assert actor_fit.scaled_count(5, actor_fit.MAX_POOLS) == (5, 1.0)


def test_dreps_count_rates_and_voting_share():
    """x and y are registered at the end (z registered and retired in the
    window); x votes twice in the window and once before it."""
    first = 1000
    events = [("x", 10, "register"), ("y", first + 1, "register"), ("z", first + 2, "register"),
              ("z", first + 3, "retire")]
    votes = [("x", 5), ("x", first + 4), ("x", first + 5)]
    fit = actor_fit.fit_dreps(events, votes, first, 10.0)
    assert fit.chain_count == 2
    assert fit.register_rate == pytest.approx(2 / 20) and fit.retire_rate == pytest.approx(1 / 20)
    assert fit.vote_rate == pytest.approx(2 / 20)
    assert fit.voting_share == 0.5


def test_contract_use_concentration():
    """One contract with 99 of 108 transactions dominates: the top 1% (one
    contract) takes 99/108, and alpha is well under 1."""
    use = [99] + [1] * 9
    fit = actor_fit.fit_contracts(use, new_contracts=4, units=100, days=10.0)
    assert fit.top_share == pytest.approx(99 / 108)
    assert fit.new_rate == pytest.approx(4 / 1000)
    assert actor_fit.fit_contracts([], 0, 1, 1.0) is None


def test_proposal_rates_by_action_type_in_the_window():
    rates = actor_fit.proposal_rates([("info", 5), ("info", 15), ("info", 16), ("treasury_withdrawals", 20)], 10, 2.0)
    assert rates == {"info": 1.0, "treasury_withdrawals": 0.5}


def bech32(data: bytes, hrp: str = "addr") -> str:
    """A bech32-shaped string for data (checksum left as filler: the lab's
    decoder does not verify it)."""
    alphabet = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"
    bits = "".join(f"{byte:08b}" for byte in data)
    bits += "0" * (-len(bits) % 5)
    return hrp + "1" + "".join(alphabet[int(bits[i:i + 5], 2)] for i in range(0, len(bits), 5)) + "qqqqqq"


def address(kind: int, payment: bytes, stake: bytes = b"") -> str:
    return bech32(bytes([kind << 4 | 1]) + payment + stake)


def test_script_addresses_and_contract_keys():
    """The SQL rule for a script payment credential agrees with the CIP-19
    header, and one script paid at two stake parts is one contract."""
    script, key, stake_a, stake_b = b"\x11" * 28, b"\x22" * 28, b"\x33" * 28, b"\x44" * 28
    cases = [address(1, script, stake_a), address(1, script, stake_b), address(7, script),
             address(0, key, stake_a), address(6, key)]
    con = duckdb.connect()
    con.register("a", pd.DataFrame({"address": cases}))
    rows = con.execute(
        f"SELECT regexp_matches(address, '{actor_stats.SCRIPT_ADDRESS_RE}'), {actor_stats.contract_key('address')} "
        "FROM a").fetchall()
    is_script = [row[0] for row in rows]
    assert is_script == [units.bech32_payload(a)[0] >> 4 in (1, 3, 5, 7) for a in cases]
    assert rows[0][1] == rows[1][1] and rows[0][1] != rows[3][1]


def write_table(root, table: str, frame: pd.DataFrame) -> None:
    os.makedirs(root / table, exist_ok=True)
    con = duckdb.connect()
    con.register("t", frame)
    con.execute(f"COPY t TO '{root / table / 'part-000000.parquet'}' (FORMAT parquet)")


def test_actor_events_from_tables(tmp_path):
    """Certificates, votes and proposals are dated by their transaction's
    block, and only valid transactions count."""
    h = [bytes([i]) * 32 for i in range(6)]
    write_table(tmp_path, "block", pd.DataFrame({"hash": h[:4], "slot": [10, 20, 30, 40], "chunk": [0, 0, 1, 1],
                                                 "tx_count": [1, 1, 1, 1]}))
    write_table(tmp_path, "tx", pd.DataFrame({"hash": h[:5], "block_hash": h[:4] + [h[3]], "block_index": [0] * 5,
                                              "valid": [True, True, True, True, False]}))
    op = b"\x09" * 28
    write_table(tmp_path, "pool_registration", pd.DataFrame({
        "tx_hash": [h[0], h[4]], "cert_index": [1, 0], "operator": [op, op], "pledge": [5, 6], "cost": [7, 8],
        "margin_numerator": [1, 1], "margin_denominator": [4, 2]}))
    write_table(tmp_path, "certificate", pd.DataFrame({
        "tx_hash": [h[0], h[1], h[2]], "cert_index": [0, 0, 0],
        "cert_type": ["pool_registration", "drep_registration", "pool_retirement"],
        "pool_key_hash": [op, None, op], "retirement_epoch": [None, None, 3]}))
    write_table(tmp_path, "certificate_credential", pd.DataFrame({
        "tx_hash": [h[1]], "cert_index": [0], "role": ["drep"], "is_script": [False], "hash": [b"\x05" * 28]}))
    write_table(tmp_path, "vote", pd.DataFrame({"tx_hash": [h[3]], "voter_role": ["drep_key"], "voter_hash": [b"\x05" * 28]}))
    write_table(tmp_path, "proposal", pd.DataFrame({"tx_hash": [h[3]], "action_type": ["info"]}))
    con = duckdb.connect()
    con.execute("CREATE TEMP TABLE paid_output (hash BLOB, address VARCHAR)")
    events = actor_stats.actor_events(con, str(tmp_path), 15, 40, {})
    assert events.pool_events == [("09" * 28, 10, "register", 5, 7, 0.25, None),
                                  ("09" * 28, 30, "retire", None, None, None, 3)]
    assert events.drep_events == [("05" * 28, 20, "register")]
    assert events.drep_votes == [("05" * 28, 40)]
    assert events.proposals == [("info", 40)]
    assert events.contract_use == [] and events.new_contracts == 0


def test_the_fixture_without_governance_tables_still_fits(chain):
    from helpers import CHAIN
    last = chain_stats.last_slot(CHAIN)
    con = chain_stats.load_window(CHAIN, 0, last, units.unit_of(True))
    events = actor_stats.actor_events(con, CHAIN, 0, last, {})
    fit = actor_fit.fit_actors(events, 0, last, last / DAY, 10, "preview")
    assert fit.proposal_rate == {}
    if fit.contracts:
        assert 0 <= fit.contracts.new_contracts <= fit.contracts.chain_count
        assert np.isfinite(fit.contracts.usage_alpha)
