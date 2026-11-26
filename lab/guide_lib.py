"""Helpers for guide.ipynb: toy simulations, key derivation and the figures
that read the shipped profiles and targets.

The toy simulations mirror the ideas of internal/sched, internal/population
and internal/rng, not their code: they exist to be played with.
"""

from __future__ import annotations

import hashlib
import hmac
import json
import os
import re
import zlib

import matplotlib.colors as mcolors
import matplotlib.pyplot as plt
import numpy as np
import yaml
from scipy import stats as sps

HERE = os.path.dirname(os.path.abspath(__file__))
DAY_HOURS = 24.0


def profiles_dir() -> str:
    """The shipped profiles: CONTINUUM_PROFILES, else ../profiles of this file."""
    return os.environ.get("CONTINUUM_PROFILES") or os.path.join(HERE, "..", "profiles")


# --- toy simulations ---------------------------------------------------------


def awake_spells(rng: np.random.Generator, days: float, onset_per_day: float, sleep_median_days: float,
                 sigma: float = 0.6) -> list[tuple[float, float]]:
    """Intervals (in days) an actor is awake. Awake time ends at a constant
    hazard (exponential spell); sleep is log-normal around its median."""
    t, out = 0.0, []
    while t < days:
        end = t + rng.exponential(1.0 / onset_per_day)
        out.append((t, min(end, days)))
        t = end + rng.lognormal(np.log(sleep_median_days), sigma)
    return out


def poisson_times(rng: np.random.Generator, rate: float, t0: float, t1: float) -> np.ndarray:
    """Event times of a Poisson process of the given rate on [t0, t1)."""
    n = rng.poisson(rate * (t1 - t0))
    return np.sort(rng.uniform(t0, t1, n))


def daily_factor(t_hours: np.ndarray, amplitude: float) -> np.ndarray:
    """1 + amplitude * sin(2 pi t / 24h): busiest at hour 6, as sched.Daily."""
    return 1.0 + amplitude * np.sin(2 * np.pi * np.asarray(t_hours) / DAY_HOURS)


def arrivals(seed: int, hours: float, rate: float, amplitude: float = 0.0, alpha: float = 0.0,
             beta: float = 1.0) -> tuple[np.ndarray, np.ndarray, np.ndarray]:
    """Wake-up times by thinning (Ogata), with rate per hour.

    Intensity is rate * daily(t) + a Hawkes excitation: every event adds alpha
    to the rate and the boost decays at speed beta. alpha must stay below
    beta, otherwise bursts feed themselves forever. Returns the event times,
    a time grid and the intensity on it.
    """
    if alpha >= beta:
        raise ValueError("alpha must be below beta")
    rng = np.random.default_rng(seed)
    events: list[float] = []

    def lam(t: float) -> float:
        exc = alpha * sum(np.exp(-beta * (t - e)) for e in events[-200:])
        return rate * float(daily_factor(np.array(t), amplitude)) + exc

    t = 0.0
    while t < hours:
        bound = rate * (1 + amplitude) + alpha * (1 + sum(np.exp(-beta * (t - e)) for e in events[-200:]))
        t += rng.exponential(1.0 / bound)
        if t < hours and rng.random() * bound <= lam(t):
            events.append(t)
    grid = np.linspace(0, hours, 600)
    intensity = np.array([rate * float(daily_factor(np.array(g), amplitude)) +
                          alpha * sum(np.exp(-beta * (g - e)) for e in events if e <= g) for g in grid])
    return np.array(events), grid, intensity


# --- seeds -------------------------------------------------------------------


def hkdf(root: bytes, info: str, length: int = 32) -> bytes:
    """HKDF-SHA256 (RFC 5869) with an empty salt: the stream key for a path."""
    prk = hmac.new(b"\0" * 32, root, hashlib.sha256).digest()
    out, block, i = b"", b"", 1
    while len(out) < length:
        block = hmac.new(prk, block + info.encode() + bytes([i]), hashlib.sha256).digest()
        out += block
        i += 1
    return out[:length]


def stream(root_seed: int, path: str) -> np.random.Generator:
    """An independent random stream for one path under a root seed. The
    engine uses ChaCha8; PCG64 stands in here, the derivation is the same."""
    key = hkdf(root_seed.to_bytes(8, "big"), path)
    return np.random.Generator(np.random.PCG64(int.from_bytes(key, "big")))


def actor_draws(root_seed: int, actors: int, n: int = 3) -> dict[int, list[float]]:
    """The first n activity draws of each actor 1..actors."""
    return {a: [round(float(x), 3) for x in stream(root_seed, f"actor/{a}/activity").random(n)]
            for a in range(1, actors + 1)}


# --- bech32 addresses (the unit is the stake credential) ---------------------

_CHARSET = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"


def fake_address(prefix: str, raw: bytes) -> str:
    """A bech32-shaped address with a dummy checksum; units ignores checksums."""
    acc = bits = 0
    out = []
    for b in raw:
        acc = (acc << 8) | b
        bits += 8
        while bits >= 5:
            bits -= 5
            out.append(_CHARSET[(acc >> bits) & 31])
    if bits:
        out.append(_CHARSET[(acc << (5 - bits)) & 31])
    return prefix + "1" + "".join(out) + "qqqqqq"


# --- profiles and targets ----------------------------------------------------

_DIST = re.compile(r"(\w+)\((.*)\)")


def parse_dist(text: str) -> tuple[str, list[float], dict[str, float]]:
    """Name, positional and keyword numbers of a profile distribution such as
    `pareto(xm=341, alpha=0.22)` or `beta(8, 2)`."""
    name, body = _DIST.fullmatch(text.strip()).groups()
    pos, kw = [], {}
    for part in filter(None, (p.strip() for p in body.split(","))):
        if "=" in part:
            k, v = part.split("=")
            kw[k.strip()] = float(v)
        else:
            pos.append(float(part))
    return name, pos, kw


def header_of(path: str) -> dict[str, str]:
    """The `# key: value` comment header of a fitted profile file."""
    out = {}
    with open(path) as f:
        for line in f:
            m = re.match(r"# (\w+): (.*)", line)
            if m:
                out[m.group(1)] = m.group(2)
            elif not line.startswith("#"):
                break
    return out


def bic_curve(header: dict[str, str]) -> dict[int, float]:
    """The BIC curve recorded in a fitted file's header."""
    return {int(k): float(v) for k, v in (p.split("=") for p in header.get("bic", "").split(", ") if "=" in p)}


class Network:
    """One network's shipped fit, or a marker that there is none yet."""

    def __init__(self, name: str, root: str | None = None):
        root = root or profiles_dir()
        self.name, self.fitted = name, False
        self.profiles: list[dict] = []
        self.target: dict = {}
        self.header: dict[str, str] = {}
        path = os.path.join(root, f"{name}.yaml")
        if not os.path.exists(path):
            return
        self.fitted = True
        with open(path) as f:
            self.profiles = yaml.safe_load(f)["profiles"]
        self.header = header_of(path)
        with open(os.path.join(root, f"{name}.target.json")) as f:
            self.target = json.load(f)

    @property
    def names(self) -> list[str]:
        return [p["name"] for p in self.profiles]


def networks(root: str | None = None) -> list[Network]:
    """Preview and preprod, then mainnet (fitted only if its file exists)."""
    return [Network(n, root) for n in ("preview", "preprod", "mainnet")]


def colour(name: str) -> tuple:
    """A stable colour per cluster name: one hue per behaviour class, lighter
    for smaller payments, so variants of a class stay distinguishable."""
    palette = {"holder": "#4c78a8", "occasional-payer": "#f58518", "frequent-trader": "#e45756",
               "script-user": "#54a24b", "staker": "#b279a2"}
    base = next((k for k in palette if name.startswith(k)), None)
    if base is None:
        return plt.cm.tab10(zlib.crc32(name.encode()) % 10)
    white = {"-small": 0.5, "-mid": 0.25}.get(next((s for s in ("-small", "-mid", "-large") if s in name), ""), 0.0)
    r, g, b = mcolors.to_rgb(palette[base])
    return (r + (1 - r) * white, g + (1 - g) * white, b + (1 - b) * white)


def panels(title: str, nets: list[Network], size=(4.6, 3.4)):
    """One axes per network, side by side. An unfitted network gets a labelled
    placeholder, so a mainnet fit appears the day its file does."""
    fig, axes = plt.subplots(1, len(nets), figsize=(size[0] * len(nets), size[1]), squeeze=False)
    for ax, n in zip(axes[0], nets):
        ax.set_title(f"{n.name}", fontsize=10)
        if not n.fitted:
            ax.text(0.5, 0.5, f"{n.name}: not fitted yet", ha="center", va="center", color="#777",
                    fontsize=11, transform=ax.transAxes)
            ax.set_xticks([])
            ax.set_yticks([])
            for s in ax.spines.values():
                s.set_linestyle("--")
                s.set_color("#aaa")
    fig.suptitle(title, fontsize=11)
    return fig, axes[0]


def fig_shares(nets: list[Network]):
    """Cluster shares by units (the weight in each profile file)."""
    fig, axes = panels("Cluster shares (weighted by units)", nets)
    for ax, n in zip(axes, nets):
        if n.fitted:
            w = [p["weight"] for p in n.profiles]
            ax.barh(n.names, w, color=[colour(x) for x in n.names])
            ax.invert_yaxis()
            ax.set_xlabel("share of units")
    fig.tight_layout()
    return fig


def fig_quantile_cdfs(nets: list[Network], stat: str, xlabel: str, title: str, logx: bool = False):
    """Per-cluster empirical CDFs of a target statistic, drawn from its quantiles."""
    fig, axes = panels(title, nets)
    for ax, n in zip(axes, nets):
        for name, c in n.target.get("clusters", {}).items():
            q = c["quantiles"].get(stat)
            if q:
                ax.plot(q, np.linspace(0, 1, len(q)), color=colour(name), label=name, lw=1.3)
        if n.fitted:
            ax.set_xlabel(xlabel)
            if logx:
                ax.set_xscale("log")
            ax.set_ylabel("share at or below")
            ax.legend(fontsize=6, loc="lower right")
    fig.tight_layout()
    return fig


def amount_density(am: dict, y: np.ndarray) -> np.ndarray:
    """Density over y = log10 lovelace of a profile's payment amount: a
    log-normal body cut at xm plus a Pareto tail above it, weighted by
    tail_share."""
    xm, share = am["xm"], am.get("tail_share", 0.0)
    mu, sg = am["mu"], am["sigma"]
    ln = y * np.log(10)
    body = sps.norm.pdf((ln - mu) / sg) / sg / sps.norm.cdf((np.log(xm) - mu) / sg)
    dens = np.where(ln <= np.log(xm), (1 - share) * body, 0.0)
    if share > 0:
        tail = am["alpha"] * np.exp(-am["alpha"] * (ln - np.log(xm)))
        dens = dens + np.where(ln > np.log(xm), share * tail, 0.0)
    return dens * np.log(10)


def fig_amounts(nets: list[Network]):
    """Payment-amount curves: log-normal body, Pareto tail, per cluster."""
    fig, axes = panels("Payment amounts: body + tail", nets)
    y = np.linspace(5.5, 13.5, 500)
    for ax, n in zip(axes, nets):
        for name, c in n.target.get("clusters", {}).items():
            ax.plot(10 ** y / 1e6, amount_density(c["fit"]["amount"], y), color=colour(name), label=name, lw=1.3)
        if n.fitted:
            ax.set_xscale("log")
            ax.set_ylim(0, 2.5)  # payments that pile on one value (2 ADA) peak far above
            ax.set_xlabel("payment (ADA)")
            ax.set_ylabel("density per decade")
            ax.legend(fontsize=6)
    fig.tight_layout()
    return fig


def fig_mix(nets: list[Network]):
    """Transaction-type mix (the intent weights) per cluster."""
    fig, axes = panels("Transaction-type mix", nets)
    kinds = ["send", "script_call", "delegate", "register_pool"]
    cols = ["#4c78a8", "#54a24b", "#b279a2", "#e45756"]
    for ax, n in zip(axes, nets):
        left = np.zeros(len(n.profiles))
        for k, c in zip(kinds, cols):
            v = np.array([p["weights"].get(k, 0.0) for p in n.profiles])
            if n.fitted:
                ax.barh(n.names, v, left=left, color=c, label=k)
            left += v
        if n.fitted:
            ax.invert_yaxis()
            ax.legend(fontsize=6, loc="lower right")
    fig.tight_layout()
    return fig


def fig_traits(nets: list[Network]):
    """Trait shapes of each cluster: competence and care (beta), patience (pareto)."""
    fig, axes = plt.subplots(len(nets), 3, figsize=(13, 2.8 * len(nets)), squeeze=False)
    x = np.linspace(0.005, 0.995, 300)
    for row, n in zip(axes, nets):
        if not n.fitted:
            for ax in row:
                ax.text(0.5, 0.5, f"{n.name}: not fitted yet", ha="center", va="center", color="#777",
                        transform=ax.transAxes)
                ax.set_xticks([])
                ax.set_yticks([])
            continue
        for p in n.profiles:
            for ax, trait in zip(row[:2], ("competence", "care")):
                _, (a, b), _ = parse_dist(p["traits"][trait])
                ax.plot(x, np.clip(sps.beta.pdf(x, a, b), 0, 8), color=colour(p["name"]), lw=1.2)
            _, _, kw = parse_dist(p["traits"]["patience"])
            g = np.logspace(np.log10(kw["xm"]), np.log10(kw["xm"]) + 4, 200)
            row[2].plot(g, sps.pareto.pdf(g, kw["alpha"], scale=kw["xm"]) * g, color=colour(p["name"]), lw=1.2)
        row[0].set_ylabel(n.name)
        row[2].set_xscale("log")
    for ax, t in zip(axes[0], ("competence ~ beta (0..1)", "care ~ beta (0..1)", "patience ~ pareto (s, density x s)")):
        ax.set_title(t, fontsize=9)
    fig.tight_layout()
    return fig


# --- calibration and clustering ----------------------------------------------


def ecdf(x: np.ndarray) -> tuple[np.ndarray, np.ndarray]:
    xs = np.sort(np.asarray(x, dtype=float))
    return xs, np.arange(1, len(xs) + 1) / len(xs)


def ks_gap(a: np.ndarray, b: np.ndarray) -> tuple[float, float]:
    """The KS statistic D of two samples and the x where the curves differ most."""
    grid = np.sort(np.concatenate([a, b]))
    fa = np.searchsorted(np.sort(a), grid, side="right") / len(a)
    fb = np.searchsorted(np.sort(b), grid, side="right") / len(b)
    i = int(np.argmax(np.abs(fa - fb)))
    return float(abs(fa[i] - fb[i])), float(grid[i])


def fig_ks(run: dict[str, np.ndarray], target: dict, dmax: float = 0.1):
    """Run samples against the target's quantile curve, with D marked."""
    stats = [s for s in ("log10_output_value", "log10_gap_seconds", "activity_per_day")
             if len(run.get(s, [])) and s in target["quantiles"]]
    fig, axes = plt.subplots(1, len(stats), figsize=(4.6 * len(stats), 3.5), squeeze=False)
    for ax, s in zip(axes[0], stats):
        q = np.asarray(target["quantiles"][s])
        d, at = ks_gap(run[s], q)
        xs, ys = ecdf(run[s])
        ax.step(xs, ys, color="#f58518", label=f"run (n={len(xs)})")
        ax.plot(q, np.linspace(0, 1, len(q)), color="#4c78a8", label="target")
        ax.axvline(at, color="#888", ls=":")
        ax.set_title(f"{s}\nD = {d:.3f} ({'pass' if d <= dmax else 'fail'}, bound {dmax})", fontsize=9)
        if s == "activity_per_day":
            ax.set_xscale("log")
        ax.legend(fontsize=7)
    fig.tight_layout()
    return fig


def fig_bic(curves: dict[str, dict[int, float]], chosen: dict[str, int]):
    """BIC against k with the chosen k marked, one panel per fit."""
    fig, axes = plt.subplots(1, len(curves), figsize=(4.6 * len(curves), 3.3), squeeze=False)
    for ax, (name, c) in zip(axes[0], curves.items()):
        ks = sorted(c)
        ax.plot(ks, [c[k] for k in ks], "o-", ms=3)
        if chosen[name] in c:
            ax.plot([chosen[name]], [c[chosen[name]]], "o", color="#e45756", ms=8, label=f"chosen k = {chosen[name]}")
        ax.set_title(name, fontsize=10)
        ax.set_xlabel("k (mixture components)")
        ax.set_ylabel("BIC (lower is better)")
        ax.legend(fontsize=8)
    fig.tight_layout()
    return fig


# --- data modes --------------------------------------------------------------


def data_paths() -> dict:
    """Where the guide reads its real data from.

    GUIDE_CHAIN and GUIDE_EXPORT (default /data/chain and /data/export, where
    `make lab` mounts LAB_DATA) hold a mithril2pq chain and a `continuum
    export` of the seed-7 preprod run. GUIDE_REAL=1 requires them, 0 forces the
    small test fixtures, and unset uses them when they exist.
    """
    chain = os.environ.get("GUIDE_CHAIN", "/data/chain")
    export = os.environ.get("GUIDE_EXPORT", "/data/export")
    flag = os.environ.get("GUIDE_REAL")
    have = os.path.isdir(chain) and os.path.isdir(export)
    if flag == "1" and not have:
        raise FileNotFoundError(f"GUIDE_REAL=1 but {chain} or {export} is missing")
    if flag == "0" or not have:
        fx = os.path.join(HERE, "tests", "fixtures")
        return {"real": False, "chain": os.path.join(fx, "chain"), "export": os.path.join(fx, "engine")}
    return {"real": True, "chain": chain, "export": export}


def units_table(paths: dict):
    """The per-unit feature table: the last 90 days of the chain when real,
    the whole fixture otherwise."""
    import common
    import units

    last = common.last_slot(paths["chain"])
    window = int(90 * 86400) if paths["real"] else last + 1
    con = common.prepare(paths["chain"], max(last - window, 0), last, units.unit_of(True),
                         "8GB" if paths["real"] else None)
    return units.features(con, window)
