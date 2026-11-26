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


def copy(con, query: str, path: str) -> None:
    os.makedirs(os.path.dirname(path), exist_ok=True)
    con.execute(f"COPY ({query}) TO '{path}' (FORMAT parquet, COMPRESSION zstd)")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--export", required=True)
    ap.add_argument("--chain", required=True)
    ap.add_argument("--out", required=True)
    a = ap.parse_args()
    con = duckdb.connect()
    e = lambda t: f"read_parquet('{a.export}/{t}/*.parquet')"
    c = lambda t: f"read_parquet('{a.chain}/{t}/*.parquet')"
    eo = os.path.join(a.out, "engine")
    copy(con, f"SELECT * FROM {e('submission')} ORDER BY seq LIMIT 200", f"{eo}/submission/part-000000.parquet")
    copy(con, f"SELECT * FROM {e('decision')} ORDER BY seq LIMIT 2000", f"{eo}/decision/part-000000.parquet")

    co = os.path.join(a.out, "chain")
    con.execute(
        f"CREATE TEMP TABLE pick AS SELECT t.hash, t.block_hash FROM {c('tx')} t "
        f"JOIN {c('block')} b ON t.block_hash = b.hash WHERE b.slot > 5000000 AND t.valid "
        f"ORDER BY b.slot, t.block_index LIMIT 100"
    )
    con.execute(
        f"CREATE TEMP TABLE srcs AS SELECT DISTINCT i.src_tx_hash AS h FROM {c('tx_input')} i "
        f"JOIN pick p ON i.tx_hash = p.hash"
    )
    copy(con, f"SELECT t.* FROM {c('tx')} t JOIN pick p ON t.hash = p.hash", f"{co}/tx/part-000000.parquet")
    copy(con, f"SELECT b.* FROM {c('block')} b WHERE b.hash IN (SELECT block_hash FROM pick)", f"{co}/block/part-000000.parquet")
    copy(con, f"SELECT i.* FROM {c('tx_input')} i JOIN pick p ON i.tx_hash = p.hash", f"{co}/tx_input/part-000000.parquet")
    copy(con, f"SELECT o.* FROM {c('tx_output')} o WHERE o.tx_hash IN (SELECT hash FROM pick UNION SELECT h FROM srcs)", f"{co}/tx_output/part-000000.parquet")
    copy(con, f"SELECT x.* FROM {c('certificate')} x JOIN pick p ON x.tx_hash = p.hash", f"{co}/certificate/part-000000.parquet")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
