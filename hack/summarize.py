#!/usr/bin/env python3
"""Summarise a hack/sample.sh TSV: per pod-kind steady (median over the last
third of samples) and peak memory and CPU. Pod kinds: pool-* (node or dingo,
told apart by --dingo names), engine, operator. Usage: summarize.py FILE [dingo-pod ...]"""
import statistics
import sys
from collections import defaultdict

path, dingo = sys.argv[1], set(sys.argv[2:])
by_pod = defaultdict(list)
with open(path) as f:
    for line in f:
        t, pod, mem, cpu = line.rstrip("\n").split("\t")
        by_pod[pod].append((int(mem), int(cpu)))

def kind(pod):
    if pod in dingo:
        return "dingo"
    for p, k in (("pool-", "cardano-node"), ("continuum-engine", "engine"), ("continuum-operator", "operator")):
        if pod.startswith(p):
            return k
    return "other"

rows = defaultdict(list)
for pod, s in by_pod.items():
    tail = s[len(s) * 2 // 3:] or s
    rows[kind(pod)].append((statistics.median(m for m, _ in tail), max(m for m, _ in s),
                            statistics.median(c for _, c in tail), max(c for _, c in s)))
tot_steady = tot_peak = 0
print("kind\tpods\tmem_steady_mib(each)\tmem_peak_mib(each)\tcpu_steady_m(each)\tcpu_peak_m(each)")
for k, v in sorted(rows.items()):
    n = len(v)
    ms, mp = statistics.mean(x[0] for x in v), max(x[1] for x in v)
    cs, cp = statistics.mean(x[2] for x in v), max(x[3] for x in v)
    tot_steady += sum(x[0] for x in v)
    tot_peak += sum(x[1] for x in v)
    print(f"{k}\t{n}\t{ms:.0f}\t{mp}\t{cs:.0f}\t{cp}")
print(f"total\t\t{tot_steady:.0f}\t{tot_peak}")
