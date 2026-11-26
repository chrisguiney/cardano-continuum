#!/usr/bin/env python3
"""Regenerate lab/tests/fixtures from a seed-7 fake run and a preview slice.

    continuum run --fake --seed 7 --wallets 100 --epochs 2 --out RUN
    continuum export RUN/events.log --out EXPORT
    mithril2pq -mithril-archive preview -last-chunk 1500 -out CHAIN
    python make_fixtures.py --export EXPORT --chain CHAIN --out tests/fixtures

The engine fixture keeps the first 200 submissions and the first 2000
decision records; the chain fixture keeps 100 preview transactions with the
rows the statistics join to.
"""

import argparse
import os

import duckdb

from chain_stats import sql_text, table_glob

ENGINE_SUBMISSIONS = 200
ENGINE_DECISIONS = 2000
CHAIN_TRANSACTIONS = 100
# The chain slice starts after this slot: far enough into preview that
# transactions spend outputs created in the Shelley-and-later eras.
CHAIN_FIRST_SLOT = 5_000_000
FIXTURE_FILE = "part-000000.parquet"


def copy_query(con: duckdb.DuckDBPyConnection, query: str, out_dir: str, table: str) -> None:
    """Write a query's rows as the single Parquet file of a fixture table."""
    path = os.path.join(out_dir, table, FIXTURE_FILE)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    con.execute(f"COPY ({query}) TO {sql_text(path)} (FORMAT parquet, COMPRESSION zstd)")


def write_engine_fixture(con: duckdb.DuckDBPyConnection, export_dir: str, out_dir: str) -> None:
    """The first submissions and decision records of a run, in log order."""
    def table(name: str) -> str:
        return f"read_parquet({sql_text(table_glob(export_dir, name))})"

    copy_query(con, f"SELECT * FROM {table('submission')} ORDER BY seq LIMIT {ENGINE_SUBMISSIONS}", out_dir, "submission")
    copy_query(con, f"SELECT * FROM {table('decision')} ORDER BY seq LIMIT {ENGINE_DECISIONS}", out_dir, "decision")


def write_chain_fixture(con: duckdb.DuckDBPyConnection, chain_dir: str, out_dir: str) -> None:
    """A run of consecutive valid transactions with every row the chain
    statistics join them to: their blocks, inputs, outputs, certificates,
    and the earlier outputs their inputs spend (which give the senders)."""
    def table(name: str) -> str:
        return f"read_parquet({sql_text(table_glob(chain_dir, name))})"

    con.execute(
        f"CREATE TEMP TABLE picked_tx AS SELECT t.hash, t.block_hash FROM {table('tx')} t "
        f"JOIN {table('block')} b ON t.block_hash = b.hash WHERE b.slot > {CHAIN_FIRST_SLOT} AND t.valid "
        f"ORDER BY b.slot, t.block_index LIMIT {CHAIN_TRANSACTIONS}"
    )
    con.execute(
        f"CREATE TEMP TABLE spent_source AS SELECT DISTINCT i.src_tx_hash AS h FROM {table('tx_input')} i "
        f"JOIN picked_tx p ON i.tx_hash = p.hash"
    )
    queries = {
        "tx": f"SELECT t.* FROM {table('tx')} t JOIN picked_tx p ON t.hash = p.hash",
        "block": f"SELECT b.* FROM {table('block')} b WHERE b.hash IN (SELECT block_hash FROM picked_tx)",
        "tx_input": f"SELECT i.* FROM {table('tx_input')} i JOIN picked_tx p ON i.tx_hash = p.hash",
        "tx_output": f"SELECT o.* FROM {table('tx_output')} o "
                     f"WHERE o.tx_hash IN (SELECT hash FROM picked_tx UNION SELECT h FROM spent_source)",
        "certificate": f"SELECT x.* FROM {table('certificate')} x JOIN picked_tx p ON x.tx_hash = p.hash",
    }
    for name, query in queries.items():
        copy_query(con, query, out_dir, name)


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--export", required=True)
    ap.add_argument("--chain", required=True)
    ap.add_argument("--out", required=True)
    args = ap.parse_args()
    con = duckdb.connect()
    write_engine_fixture(con, args.export, os.path.join(args.out, "engine"))
    write_chain_fixture(con, args.chain, os.path.join(args.out, "chain"))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
