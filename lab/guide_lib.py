"""Data and toy models behind guide.ipynb. Nothing here draws: the figures
are in guide_figures.py.

Three kinds of helper:

  * toy simulations (awake_spells, poisson_times, arrivals, random streams)
    that mirror the ideas of internal/sched, internal/population and
    internal/rng, not their code: they exist to be played with;
  * readers for the shipped profiles and targets (Network);
  * the real-data steps of the notebook (which data to use, the units
    table, the clustering summary).
"""

from __future__ import annotations

import hashlib
import hmac
import json
import os
import re
from dataclasses import dataclass

import numpy as np
import pandas as pd
import yaml
from scipy import stats as sps

import chain_stats
import cluster
import engine_stats
import grade
import units
from cardano import LOVELACE_PER_ADA, SECONDS_PER_DAY
from fit import DEFAULT_KMAX, DEFAULT_KMIN, DEFAULT_WINDOW_DAYS
from unit_features import unit_features

HERE = os.path.dirname(os.path.abspath(__file__))
DAY_HOURS = 24.0

# Real-data runs of the notebook cap DuckDB here; the fixtures need no cap.
REAL_DATA_MEMORY_LIMIT = "8GB"


def profiles_dir() -> str:
    """The shipped profiles: CONTINUUM_PROFILES, else ../profiles of this file."""
    return os.environ.get("CONTINUUM_PROFILES") or os.path.join(HERE, "..", "profiles")


# --- toy simulations ---------------------------------------------------------


def awake_spells(rng: np.random.Generator, days: float, onset_per_day: float, sleep_median_days: float,
                 sigma: float = 0.6) -> list[tuple[float, float]]:
    """Intervals (start, end), in days, during which one actor is awake.

    An awake spell ends at a constant hazard, so its length is exponential
    with mean 1 / onset_per_day. Each sleep is log-normal with the given
    median: a log-normal's median is exp(mu), so mu = ln(median).
    """
    now, spells = 0.0, []
    while now < days:
        falls_asleep = now + rng.exponential(1.0 / onset_per_day)
        spells.append((now, min(falls_asleep, days)))
        now = falls_asleep + rng.lognormal(np.log(sleep_median_days), sigma)
    return spells


def poisson_times(rng: np.random.Generator, rate: float, start: float, end: float) -> np.ndarray:
    """Event times of a Poisson process of the given rate on [start, end).

    The number of events in a stretch of length T is Poisson with mean
    rate * T, and given that number the events fall uniformly at random.
    https://en.wikipedia.org/wiki/Poisson_point_process
    """
    count = rng.poisson(rate * (end - start))
    return np.sort(rng.uniform(start, end, count))


def daily_factor(t_hours: np.ndarray, amplitude: float) -> np.ndarray:
    """1 + amplitude * sin(2 pi t / 24h): busiest at hour 6, as sched.Daily."""
    return 1.0 + amplitude * np.sin(2 * np.pi * np.asarray(t_hours) / DAY_HOURS)


# Only this many of the latest events feed the excitation while simulating:
# older boosts have decayed to nothing and summing them only costs time.
_RECENT_EVENTS = 200


def _excitation(t: float, events: list[float], beta: float) -> float:
    """Sum over past events of exp(-beta * (t - event)): how much boost is
    still left at time t, before multiplying by alpha."""
    return sum(np.exp(-beta * (t - event)) for event in events)


def arrivals(seed: int, hours: float, rate: float, amplitude: float = 0.0, alpha: float = 0.0,
             beta: float = 1.0) -> tuple[np.ndarray, np.ndarray, np.ndarray]:
    """Wake-up times of one actor, with rate per hour.

    The intensity (expected wake-ups per hour, right now) is

        lambda(t) = rate * daily(t) + alpha * sum over past events of exp(-beta * (t - t_i))

    The first term is the daily cycle. The second is the Hawkes
    excitation: every event adds alpha to the rate and the boost decays at
    speed beta. alpha must stay below beta, otherwise bursts feed
    themselves forever (each event triggers alpha/beta follow-ups on
    average).

    Events are drawn by thinning (Ogata's method): propose candidates from
    a faster constant-rate process whose rate `bound` is at least
    lambda(t), and keep each with probability lambda(t) / bound. Returns
    the event times, a time grid and the intensity on it.
    https://en.wikipedia.org/wiki/Hawkes_process
    https://en.wikipedia.org/wiki/Poisson_point_process#Thinning
    """
    if alpha >= beta:
        raise ValueError("alpha must be below beta")
    rng = np.random.default_rng(seed)
    events: list[float] = []

    def intensity(t: float, past: list[float]) -> float:
        return rate * float(daily_factor(np.array(t), amplitude)) + alpha * _excitation(t, past, beta)

    now = 0.0
    while now < hours:
        # The excitation only decays until the next event, and the daily
        # factor never exceeds 1 + amplitude, so this is an upper bound on
        # the intensity from now on.
        bound = rate * (1 + amplitude) + alpha * (1 + _excitation(now, events[-_RECENT_EVENTS:], beta))
        now += rng.exponential(1.0 / bound)
        if now < hours and rng.random() * bound <= intensity(now, events[-_RECENT_EVENTS:]):
            events.append(now)
    grid = np.linspace(0, hours, 600)
    on_grid = np.array([intensity(g, [event for event in events if event <= g]) for g in grid])
    return np.array(events), grid, on_grid


# --- seeds -------------------------------------------------------------------


def hkdf(root: bytes, info: str, length: int = 32) -> bytes:
    """HKDF-SHA256 (RFC 5869) with an empty salt: the stream key for a path.
    https://www.rfc-editor.org/rfc/rfc5869"""
    pseudorandom_key = hmac.new(b"\0" * 32, root, hashlib.sha256).digest()
    out, block, counter = b"", b"", 1
    while len(out) < length:
        block = hmac.new(pseudorandom_key, block + info.encode() + bytes([counter]), hashlib.sha256).digest()
        out += block
        counter += 1
    return out[:length]


def stream(root_seed: int, path: str) -> np.random.Generator:
    """An independent random stream for one path under a root seed. The
    engine uses ChaCha8; PCG64 stands in here, the derivation is the same."""
    key = hkdf(root_seed.to_bytes(8, "big"), path)
    return np.random.Generator(np.random.PCG64(int.from_bytes(key, "big")))


def actor_draws(root_seed: int, actors: int, n: int = 3) -> dict[int, list[float]]:
    """The first n activity draws of each actor 1..actors."""
    return {actor: [round(float(x), 3) for x in stream(root_seed, f"actor/{actor}/activity").random(n)]
            for actor in range(1, actors + 1)}


def print_replay(seed: int = 7, actors: int = 5) -> None:
    """Show the two properties a seeded run relies on: the same seed gives
    the same draws, and adding an actor leaves the others' draws alone."""
    draws = actor_draws(seed, actors)
    print(f"seed {seed}: first draws per actor")
    for actor, values in draws.items():
        print(f"  actor/{actor}/activity  {values}")
    with_one_more = actor_draws(seed, actors + 1)
    print("same seed, same run:       ", draws == actor_draws(seed, actors))
    print("one more actor, others same:", all(with_one_more[actor] == draws[actor] for actor in draws))
    print("different seed differs:     ", draws != actor_draws(seed + 1, actors))


# --- bech32 addresses (the unit is the stake credential) ---------------------

_BECH32_ALPHABET = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"


def fake_address(prefix: str, raw: bytes) -> str:
    """A bech32-shaped address with a dummy checksum; units ignores checksums."""
    buffer = bits_in_buffer = 0
    chars = []
    for byte in raw:
        buffer = (buffer << 8) | byte
        bits_in_buffer += 8
        while bits_in_buffer >= 5:
            bits_in_buffer -= 5
            chars.append(_BECH32_ALPHABET[(buffer >> bits_in_buffer) & 31])
    if bits_in_buffer:
        chars.append(_BECH32_ALPHABET[(buffer << (5 - bits_in_buffer)) & 31])
    return prefix + "1" + "".join(chars) + "qqqqqq"


def print_one_wallet_one_unit() -> None:
    """Two payment addresses that share a stake credential are one unit."""
    payment_1, payment_2, stake = bytes([1]) * 28, bytes([2]) * 28, bytes([9]) * 28
    base_address_header = bytes([0x00])  # CIP-19 type 0: key payment, key stake
    first = fake_address("addr", base_address_header + payment_1 + stake)
    second = fake_address("addr", base_address_header + payment_2 + stake)
    same_unit = units.unit_key(first) == units.unit_key(second)
    print("two addresses of one wallet, one unit:", same_unit, "->", units.unit_key(first)[:18] + "...")


# --- profiles and targets ----------------------------------------------------

_DISTRIBUTION = re.compile(r"(\w+)\((.*)\)")


def parse_dist(text: str) -> tuple[str, list[float], dict[str, float]]:
    """Name, positional and keyword numbers of a profile distribution such as
    `pareto(xm=341, alpha=0.22)` or `beta(8, 2)`."""
    name, body = _DISTRIBUTION.fullmatch(text.strip()).groups()
    positional, keyword = [], {}
    for part in filter(None, (piece.strip() for piece in body.split(","))):
        if "=" in part:
            key, value = part.split("=")
            keyword[key.strip()] = float(value)
        else:
            positional.append(float(part))
    return name, positional, keyword


def header_of(path: str) -> dict[str, str]:
    """The `# key: value` comment header of a fitted profile file."""
    header = {}
    with open(path) as f:
        for line in f:
            match = re.match(r"# (\w+): (.*)", line)
            if match:
                header[match.group(1)] = match.group(2)
            elif not line.startswith("#"):
                break
    return header


def bic_curve(header: dict[str, str]) -> dict[int, float]:
    """The BIC curve recorded in a fitted file's header as `k=value, ...`."""
    pairs = (pair.split("=") for pair in header.get("bic", "").split(", ") if "=" in pair)
    return {int(k): float(bic) for k, bic in pairs}


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
        return [profile["name"] for profile in self.profiles]

    @property
    def mixture_k(self) -> int:
        """Components of the Gaussian mixture (files from before segments
        recorded only k)."""
        return int(self.header.get("k_gmm", self.header["k"]))

    def summary(self) -> str:
        """One line on how the shipped fit was made."""
        if not self.fitted:
            return f"{self.name}: not fitted yet"
        h = self.header
        return (f"{self.name}: {h['units']} units, window {h['window']}, seed {h['seed']}, "
                f"mixture k {h.get('k_gmm', h['k'])}, {len(self.profiles)} clusters, weighting {h['weighting']}")


def networks(root: str | None = None) -> list[Network]:
    """Preview and preprod, then mainnet (fitted only if its file exists)."""
    return [Network(name, root) for name in ("preview", "preprod", "mainnet")]


def amount_density(amount: dict, log10_lovelace: np.ndarray) -> np.ndarray:
    """Density of a profile's payment amount over y = log10 lovelace: a
    log-normal body cut at xm plus a Pareto tail above it, weighted by
    tail_share (estimators.BodyTail, as stored in a target's "fit").

    On the natural-log scale the cut body is a normal density divided by
    the share of it below ln xm, and a Pareto tail is an exponential:
    alpha * exp(-alpha * (ln x - ln xm)). The final factor ln(10) converts
    a density per unit of ln x into one per decade.
    """
    xm, tail_share = amount["xm"], amount.get("tail_share", 0.0)
    mu, sigma = amount["mu"], amount["sigma"]
    ln_x = log10_lovelace * np.log(10)
    body = sps.norm.pdf((ln_x - mu) / sigma) / sigma / sps.norm.cdf((np.log(xm) - mu) / sigma)
    density = np.where(ln_x <= np.log(xm), (1 - tail_share) * body, 0.0)
    if tail_share > 0:
        tail = amount["alpha"] * np.exp(-amount["alpha"] * (ln_x - np.log(xm)))
        density = density + np.where(ln_x > np.log(xm), tail_share * tail, 0.0)
    return density * np.log(10)


# --- calibration -------------------------------------------------------------


def ecdf(sample: np.ndarray) -> tuple[np.ndarray, np.ndarray]:
    """The empirical cumulative curve of a sample: sorted values, and for
    each the share of the sample at or below it."""
    values = np.sort(np.asarray(sample, dtype=float))
    return values, np.arange(1, len(values) + 1) / len(values)


def ks_gap(a: np.ndarray, b: np.ndarray) -> tuple[float, float]:
    """The KS statistic D of two samples and the x where the curves differ
    most. Both cumulative curves are evaluated at every observed value; D
    is the largest absolute difference (see compare.compare_sample)."""
    grid = np.sort(np.concatenate([a, b]))
    share_of_a = np.searchsorted(np.sort(a), grid, side="right") / len(a)
    share_of_b = np.searchsorted(np.sort(b), grid, side="right") / len(b)
    widest = int(np.argmax(np.abs(share_of_a - share_of_b)))
    return float(abs(share_of_a[widest] - share_of_b[widest])), float(grid[widest])


# --- data modes --------------------------------------------------------------


@dataclass(frozen=True)
class DataPaths:
    """Where the notebooks read their data from."""

    # True for a mounted real chain and run, False for the test fixtures.
    real: bool
    # A mithril2pq chain directory.
    chain: str
    # A `continuum export` directory.
    export: str

    def describe(self) -> str:
        if self.real:
            return "REAL preprod chain and seed-7 run"
        return "test fixtures (mount real data to see the real plots)"


def data_paths() -> DataPaths:
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
        fixtures = os.path.join(HERE, "tests", "fixtures")
        return DataPaths(False, os.path.join(fixtures, "chain"), os.path.join(fixtures, "engine"))
    return DataPaths(True, chain, export)


def run_and_target(paths: DataPaths) -> tuple[dict[str, np.ndarray], dict, str]:
    """The run's samples, the overall target to hold them against, and the
    target file's path.

    A real run names its target through its profile hash; the fixture run
    has none, so it is shown against the shipped preprod target.
    """
    run = engine_stats.engine_stats(paths.export)
    if paths.real:
        target_path = grade.find_target(paths.export, profiles_dir())
    else:
        target_path = os.path.join(profiles_dir(), "preprod.target.json")
    with open(target_path) as f:
        overall, _ = grade.target_views(json.load(f))
    return run.samples, overall, target_path


def units_table(paths: DataPaths) -> pd.DataFrame:
    """The per-unit feature table (unit_features.COLUMNS): the last 90 days
    of the chain when real, the whole fixture otherwise."""
    last = chain_stats.last_slot(paths.chain)
    window_slots = int(DEFAULT_WINDOW_DAYS * SECONDS_PER_DAY) if paths.real else last + 1
    con = chain_stats.load_window(paths.chain, max(last - window_slots, 0), last, units.unit_of(True),
                                  REAL_DATA_MEMORY_LIMIT if paths.real else None)
    return unit_features(con, window_slots)


def cluster_units_table(table: pd.DataFrame, paths: DataPaths, seed: int = 1) -> cluster.Clustering:
    """Cluster a units table as fit.py does, and add a `cluster` column with
    each unit's cluster name. The fixture has too few units for more than
    three components."""
    kmax = DEFAULT_KMAX if paths.real else 3
    clustering = cluster.cluster_units(table, DEFAULT_KMIN, kmax, seed=seed)
    table["cluster"] = [clustering.names[label] for label in clustering.labels]
    return clustering


def describe_clustering(table: pd.DataFrame, clustering: cluster.Clustering) -> str:
    """One line on how many clusters were found and whether BIC settled."""
    fit = clustering.fit
    settled = "interior" if fit.interior else "not interior: the curve is still falling at the edge of the range"
    return (f"{len(table)} units -> {clustering.k} clusters (mixture k = {fit.k}, "
            f"BIC minimum at k = {fit.k_min}, {settled})")


def cluster_summary(table: pd.DataFrame) -> pd.DataFrame:
    """Per cluster: how many units, their median activity and their median
    payment in ADA."""
    def median_in_ada(lovelace: pd.Series) -> float:
        return lovelace.median() / LOVELACE_PER_ADA

    summary = table.groupby("cluster").agg(
        units=("unit", "size"),
        tx_per_day=("tx_per_day", "median"),
        median_payment_ada=("median_payment_lovelace", median_in_ada),
    )
    return summary.round(3)


def cluster_weights(table: pd.DataFrame) -> tuple[pd.Series, pd.Series]:
    """Each cluster's share by number of units and by number of
    transactions (see fit.cluster_shares)."""
    by_units = table.groupby("cluster").size() / len(table)
    by_transactions = table.groupby("cluster").tx_count.sum() / table.tx_count.sum()
    return by_units, by_transactions.reindex(by_units.index)


# --- markets: currencies, exchanges, synthetic lifecycle -----------------------
#
# A toy copy of internal/market and the trade scores of internal/agenda: the
# same processes and parameters (read from profiles/m1.yaml's markets
# section), drawn with numpy's generator, so a path here matches a run's in
# shape, not draw for draw.

# The agenda's trade constants (internal/agenda/trade.go).
LOSS_GAIN = 0.1
PANIC_SCORE = 0.2
PATIENCE_SCALE = 3.0


def markets_params() -> dict:
    """The markets section of the shipped m1 profile: every default spelled out."""
    with open(os.path.join(profiles_dir(), "m1.yaml")) as f:
        return yaml.safe_load(f)["markets"]


@dataclass
class RatePath:
    """A seeded market path: per step, ADA/USD, each currency's USD rate and
    each exchange's log basis."""
    t_hours: np.ndarray
    ada: np.ndarray
    usd: dict[str, np.ndarray]
    basis: dict[str, np.ndarray]
    params: dict

    def mid(self, code: str) -> np.ndarray:
        """ADA in the currency: units per ADA."""
        return self.ada / self.usd[code]

    def quotes(self, exchange: dict, code: str) -> tuple[np.ndarray, np.ndarray]:
        """The exchange's bid and ask in the currency."""
        mid = self.mid(code) * np.exp(self.basis[exchange["name"]])
        half = exchange["spread"] / 2
        return mid * (1 - half), mid * (1 + half)


def _ou(x: float, level: float, theta: float, vol: float, dt: float, z: float) -> float:
    a = np.exp(-theta * dt)
    return level + (x - level) * a + vol * np.sqrt((1 - a * a) / (2 * theta)) * z


def rate_path(seed: int, hours: float, params: dict | None = None) -> RatePath:
    """ADA/USD as a regime-switching log random walk with jumps, each
    currency as a log Ornstein-Uhlenbeck process around its level, and each
    exchange's basis as one around zero: internal/market's curve."""
    p = params or markets_params()
    rng = np.random.default_rng(seed)
    a, dt = p["ada"], p["step"] / 3600
    n = int(hours / dt) + 1
    ada, bull = np.empty(n), True
    ada[0] = np.log(a["start"])
    usd = {c["code"]: np.empty(n) for c in p["currencies"]}
    basis = {x["name"]: np.zeros(n) for x in p["exchanges"]}
    for c in p["currencies"]:
        usd[c["code"]][0] = np.log(c["usd"])
    for k in range(1, n):
        drift = a["drift"] if bull else -a["drift"]
        ada[k] = ada[k - 1] + (drift - a["vol"] ** 2 / 2) * dt + a["vol"] * np.sqrt(dt) * rng.standard_normal()
        bull = bull != (rng.random() < 1 - np.exp(-a["regime_rate"] * dt))
        jumps = rng.poisson(a["jump_rate"] * dt)
        if jumps:
            ada[k] += jumps * a["jump_mean"] + a["jump_sd"] * np.sqrt(jumps) * rng.standard_normal()
        for c in p["currencies"]:
            code = c["code"]
            usd[code][k] = _ou(usd[code][k - 1], np.log(c["usd"]), p["fx"]["reversion"], p["fx"]["vol"], dt, rng.standard_normal())
        for x in p["exchanges"]:
            basis[x["name"]][k] = _ou(basis[x["name"]][k - 1], 0.0, x["basis_reversion"], x["basis_vol"], dt, rng.standard_normal())
    return RatePath(np.arange(n) * dt, np.exp(ada), {c: np.exp(v) for c, v in usd.items()}, basis, p)


def extra_currencies(seed: int, n: int, extra: float, currencies: int) -> np.ndarray:
    """How many extra preferred currencies each of n wallets draws: one more
    with probability extra each time, at most currencies - 1."""
    rng = np.random.default_rng(seed)
    out = np.zeros(n, dtype=int)
    for i in range(n):
        while out[i] < currencies - 1 and rng.random() < extra:
            out[i] += 1
    return out


def _window_return(path: RatePath, series: np.ndarray, seconds: float) -> np.ndarray:
    lag = int(round(seconds / path.params["step"]))
    back = np.concatenate([np.full(lag, series[0]), series[:-lag]]) if lag else series
    return np.log(series / back)


def birth_factor(path: RatePath, code: str) -> np.ndarray:
    """clamp(exp(beta * log return over the birth window)): ADA rising
    against a currency brings that currency's wallets."""
    b = path.params["births"]
    return np.clip(np.exp(b["beta"] * _window_return(path, path.mid(code), b["window"])), b["min"], b["max"])


def death_factor(path: RatePath, code: str, hodl: bool = False) -> np.ndarray:
    """clamp(exp(-beta * log return over the longer death window)), to the
    power hodl for a hodl wallet: a prolonged fall hastens dormancy and
    abandonment, and holders resist."""
    d = path.params["deaths"]
    f = np.clip(np.exp(-d["beta"] * _window_return(path, path.mid(code), d["window"])), d["min"], d["max"])
    return f ** d["hodl"] if hodl else f


def largest_drawdown(series: np.ndarray) -> tuple[int, int, float]:
    """The peak and trough indexes of the deepest fall, and its depth."""
    peak, best = 0, (0, 0, 0.0)
    for i, x in enumerate(series):
        if x > series[peak]:
            peak = i
        if (depth := x / series[peak] - 1) < best[2]:
            best = (peak, i, depth)
    return best


def sell_score(gain: float, loss_aversion: float, patience: float, hodl: bool) -> float:
    """A wallet's score for selling under water (internal/agenda's sell):
    zero for a hodl wallet, else grown with loss aversion, impatience and
    the depth of the loss; above water, the standing 0.01."""
    if gain > -LOSS_GAIN:
        return 0.01
    if hodl:
        return 0.0
    impatience = PATIENCE_SCALE / (PATIENCE_SCALE + patience)
    return PANIC_SCORE * loss_aversion * impatience * -gain / LOSS_GAIN


def drawdown_split(seed: int, wallets: int = 300, depth: float = -0.3, hodl_share: float = 2 / 3,
                   decisions: int = 20) -> dict[str, int]:
    """Wallets that wake decisions times under water at depth: a hodl wallet
    never sells; a trader sells on a draw against its sell score over
    sell score + 1 (idle). Traits as m1's trader: loss aversion beta(4, 3),
    patience lognormal(0.5, 0.8)."""
    rng = np.random.default_rng(seed)
    out = {"hodl": 0, "held": 0, "sold": 0}
    for _ in range(wallets):
        if rng.random() < hodl_share:
            out["hodl"] += 1
            continue
        s = sell_score(depth, rng.beta(4, 3), rng.lognormal(0.5, 0.8), False)
        out["sold" if (rng.random(decisions) < s / (s + 1)).any() else "held"] += 1
    return out
