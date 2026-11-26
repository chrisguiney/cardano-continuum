#!/usr/bin/env python3
"""Add Hawkes parameters to a shipped profile file from its target.

    python add_hawkes.py PROFILE.yaml TARGET.json

fit.py now fits each cluster's burstiness (population_fit.fit_hawkes) and
writes it as rates.hawkes. A profile fitted before that has none, and
refitting needs the whole chain again; its target keeps the network's
overall gap quantiles, which stand in for the gap sample. This gives every
wallet profile of the file the overall fit, replacing any it had, and
records it in the header. Clusters lose their own burstiness this way: a
refit is better when the chain is at hand.

Running it twice changes nothing.
"""

from __future__ import annotations

import argparse
import json

import numpy as np

from population_fit import fit_hawkes
from profile_yaml import number

HEADER_KEY = "# hawkes: "


def overall_gaps(target: dict) -> np.ndarray:
    """Gap seconds from the target's overall log10 gap quantiles. A
    quantile at the floor (log10 of one slot) is a same-slot gap."""
    view = target.get("overall", target)
    log10 = np.asarray(view["quantiles"]["log10_gap_seconds"], dtype=float)
    return np.where(log10 <= 0, 0.0, 10.0 ** log10)


def rewrite(text: str, line: str, note: str) -> str:
    """text with line as every wallet profile's rates.hawkes and note in
    the header."""
    out, in_profiles, in_rates = [], False, False
    lines = [ln for ln in text.splitlines() if not ln.startswith(HEADER_KEY)]
    for ln in lines:
        if ln.startswith("profiles:"):
            in_profiles = True
        elif ln and not ln.startswith(" ") and not ln.startswith("#"):
            in_profiles = False
        if in_profiles and ln.startswith("      hawkes:"):
            continue
        out.append(ln)
        if in_profiles and ln.startswith("    rates:"):
            in_rates = True
        if in_rates and ln.startswith("      activity_tail:"):
            out.append(line)
            in_rates = False
    header = 0
    while header < len(out) and out[header].startswith("#"):
        header += 1
    out.insert(min(2, header), note)
    return "\n".join(out) + "\n"


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("profile")
    ap.add_argument("target")
    args = ap.parse_args(argv)
    with open(args.target) as f:
        h = fit_hawkes(overall_gaps(json.load(f)))
    with open(args.profile) as f:
        text = f.read()
    if h is None:
        return 0
    line = f"      hawkes: {{alpha: {number(h.alpha)}, beta: {number(h.beta)}}}"
    note = f"{HEADER_KEY}alpha={number(h.alpha)} beta={number(h.beta)} from the target's overall gaps (add_hawkes.py)"
    with open(args.profile, "w") as f:
        f.write(rewrite(text, line, note))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
