#!/usr/bin/env python3
"""Fit profile distributions to chain data.

    python fit.py CHAIN_DIR --out OUT_DIR [--name mainnet-fit]

CHAIN_DIR is a mithril2pq output directory. Writes OUT_DIR/profile.yaml (a
profile file in internal/profile's syntax) and OUT_DIR/target.json (the
quantiles and shares grade.py tests a run against).
"""

from __future__ import annotations

import argparse
import json
import os
import sys

import numpy as np

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import common  # noqa: E402

TAIL_QUANTILE = 0.9
# profile.Pareto rejects an index at or below 1 for activity_tail.
MIN_TAIL = 1.05


def fit_lognormal(x: np.ndarray) -> tuple[float, float]:
    """MLE of the log-normal: mean and std of ln x."""
    x = x[x > 0]
    if len(x) < 2:
        return 0.0, 1.0
    lx = np.log(x)
    return float(lx.mean()), float(max(lx.std(), 1e-6))


def fit_pareto(x: np.ndarray, xm: float) -> float:
    """MLE of the Pareto index above xm: n / sum(ln(x / xm))."""
    t = x[x > xm]
    if len(t) < 2 or xm <= 0:
        return 2.0
    s = float(np.log(t / xm).sum())
    return float(len(t) / s) if s > 0 else 2.0


def fit_body_tail(x: np.ndarray, q: float = TAIL_QUANTILE) -> dict:
    """Log-normal body below the q-quantile, Pareto tail above it."""
    xm = float(np.quantile(x, q))
    mu, sigma = fit_lognormal(x[x <= xm])
    return {"xm": xm, "mu": mu, "sigma": sigma, "alpha": fit_pareto(x, xm), "q": q}


def fit_beta(p: np.ndarray) -> tuple[float, float]:
    """Method-of-moments beta fit; (1, 1) when the sample is degenerate."""
    p = p[np.isfinite(p)]
    if len(p) < 3:
        return 1.0, 1.0
    m, v = float(p.mean()), float(p.var())
    if v <= 1e-9 or not 0 < m < 1 or v >= m * (1 - m):
        return 1.0, 1.0
    k = m * (1 - m) / v - 1
    return float(m * k), float((1 - m) * k)


def fmt(v: float) -> str:
    return f"{v:.6g}"


def build(st: common.Stats, name: str) -> tuple[str, dict]:
    ov = st.samples["output_value_lovelace"]
    bt = fit_body_tail(ov)
    counts = st.samples["tx_count_per_address"]
    tail = max(fit_pareto(counts, float(np.quantile(counts, 0.5))), MIN_TAIL)
    gaps = st.samples["gap_seconds"]
    gaps = gaps[gaps > 0]
    gap_xm = max(float(np.median(gaps)), 1.0)
    gap_alpha = fit_pareto(gaps, gap_xm)
    long = gaps[gaps > 86400]
    if len(long) < 30:
        long = gaps[gaps > gap_xm]
    dmu, dsig = fit_lognormal(long)
    comp = fit_beta(st.per_address["change_to_sender"])
    care = fit_beta(st.per_address["address_reuse"])
    mix = st.shares["tx_type_mix"]
    rate = float(st.samples["activity_per_day"].mean()) / 86400.0
    weights = {"send": mix["plain"], "script_call": mix["script"],
               "delegate": mix["stake"], "register_pool": mix["pool"]}
    weights = {k: v for k, v in weights.items() if v > 0}
    z = sum(weights.values())
    weights = {k: v / z for k, v in weights.items()}
    # Rounding must keep the sum within profile's tolerance, so the last
    # weight takes the remainder.
    keys = list(weights)
    rounded = {k: round(weights[k], 6) for k in keys}
    rounded[keys[-1]] = round(1.0 - sum(rounded[k] for k in keys[:-1]), 6)

    lines = [
        "# Fitted by lab/fit.py from chain data; see lab/README.md for the mapping.",
        f"# output value (lovelace): lognormal body below {fmt(bt['xm'])} "
        f"lognormal({fmt(bt['mu'])}, {fmt(bt['sigma'])}), tail pareto(xm={fmt(bt['xm'])}, alpha={fmt(bt['alpha'])})",
        "version: 1",
        "profiles:",
        f"  - name: {name}",
        "    kind: wallet",
        "    agenda: hodl",
        "    traits:",
        f"      competence: beta({fmt(comp[0])}, {fmt(comp[1])})",
        f"      patience: pareto(xm={fmt(gap_xm)}, alpha={fmt(gap_alpha)})",
        "      learning_rate: const(0)",
        f"      care: beta({fmt(care[0])}, {fmt(care[1])})",
        "    rates:",
        "      base:",
        f"        send: {fmt(rate)}",
        f"      activity_tail: {fmt(tail)}",
        "    weights:",
    ]
    lines += [f"      {k}: {rounded[k]:.6f}" for k in keys]
    lines += [
        "    hazards:",
        f"      dormancy: lognormal({fmt(dmu)}, {fmt(dsig)})",
    ]
    target = {
        "version": 1,
        "info": st.info,
        "fit": {"output_value": bt, "activity_tail": tail, "gap": {"xm": gap_xm, "alpha": gap_alpha},
                "dormancy": {"mu": dmu, "sigma": dsig}, "competence": comp, "care": care},
        "quantiles": {k: common.quantiles(st.samples[k]) for k in common.KS_STATS},
        "shares": st.shares,
        "fractions": st.fractions,
    }
    return "\n".join(lines) + "\n", target


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("chain_dir")
    ap.add_argument("--out", required=True)
    ap.add_argument("--name", default="chain-fit")
    ap.add_argument("--slot-seconds", type=float, default=1.0)
    a = ap.parse_args(argv)
    st = common.chain_stats(a.chain_dir, a.slot_seconds)
    yml, target = build(st, a.name)
    os.makedirs(a.out, exist_ok=True)
    with open(os.path.join(a.out, "profile.yaml"), "w") as f:
        f.write(yml)
    with open(os.path.join(a.out, "target.json"), "w") as f:
        json.dump(target, f, separators=(",", ":"))
    print(f"wrote {a.out}/profile.yaml and target.json ({int(st.info['txs'])} txs)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
