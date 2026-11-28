#!/usr/bin/env python3
"""Refresh internal/profile/fxsnapshot.json from public APIs.

Run through `make market-snapshot`. The engine never fetches at runtime: the
snapshot is committed data that seeds the default ADA market, regenerated on
demand. Sources, both unauthenticated:

  Frankfurter (ECB reference rates)  units per USD, latest and a year of days
  CoinGecko                          ADA price now and a year of daily prices

Currencies the ECB does not publish (ARS, NGN, VND) take their USD rate as
ADA in that currency over ADA in USD, from CoinGecko's daily series.

Per currency it stores the USD rate now and, from the year of daily log
returns, the annualised volatility, the drift (log change per year, negative
when the currency loses value against USD) and an Ornstein-Uhlenbeck
reversion per year from the lag-1 autocorrelation of the detrended log level,
clamped to [0.25, 8]. One simulated hour stands for one such year.

Environment: OUT (output path, default internal/profile/fxsnapshot.json).
Needs curl and python3 only; every request has a timeout.
"""
import datetime
import json
import math
import os
import sys
import time
import subprocess

CODES = ["EUR", "GBP", "JPY", "KRW", "BRL", "MXN", "ZAR", "TRY", "ARS", "NGN",
         "INR", "IDR", "PHP", "VND"]
# Not in the ECB set: derived from CoinGecko.
VIA_ADA = ["ARS", "NGN", "VND"]
FRANKFURTER = "https://api.frankfurter.dev/v1"
COINGECKO = "https://api.coingecko.com/api/v3"
OUT = os.environ.get("OUT", "internal/profile/fxsnapshot.json")
RATE_FLOOR, RATE_CAP = 0.25, 8.0


def get(url):
    for attempt in range(4):
        try:
            # curl rather than urllib: it uses the system trust store.
            body = subprocess.run(["curl", "-fsS", "--max-time", "30", url],
                                  check=True, capture_output=True, text=True).stdout
            return json.loads(body)
        except Exception as e:  # retry on rate limits and transient errors
            if attempt == 3:
                raise SystemExit(f"fetch {url}: {e}")
            time.sleep(20 * (attempt + 1))


def fit(levels, per_year):
    """Annualised vol, drift and reversion from a series of USD rates."""
    logs = [math.log(x) for x in levels if x > 0]
    rets = [b - a for a, b in zip(logs, logs[1:])]
    n = len(rets)
    mean = sum(rets) / n
    var = sum((r - mean) ** 2 for r in rets) / (n - 1)
    vol = math.sqrt(var * per_year)
    drift = mean * per_year
    # AR(1) on the detrended level: x[t+1] = phi x[t] + e, phi = exp(-theta dt).
    dev = [x - (logs[0] + mean * i) for i, x in enumerate(logs)]
    num = sum(a * b for a, b in zip(dev, dev[1:]))
    den = sum(a * a for a in dev[:-1])
    phi = num / den if den else 0.0
    theta = RATE_CAP if phi <= 0 else -math.log(min(phi, 0.999999)) * per_year
    return vol, drift, min(max(theta, RATE_FLOOR), RATE_CAP)


def main():
    today = datetime.date.today()
    start = today - datetime.timedelta(days=365)

    fx = get(f"{FRANKFURTER}/{start}..{today}?base=USD&symbols={','.join(c for c in CODES if c not in VIA_ADA)}")
    days = sorted(fx["rates"])

    ada_usd = get(f"{COINGECKO}/simple/price?ids=cardano&vs_currencies=usd")["cardano"]["usd"]
    chart = {}
    for cur in ["usd"] + [c.lower() for c in VIA_ADA]:
        chart[cur] = {
            datetime.datetime.fromtimestamp(t / 1000, datetime.timezone.utc).date().isoformat(): p
            for t, p in get(f"{COINGECKO}/coins/cardano/market_chart?vs_currency={cur}&days=365&interval=daily")["prices"]
        }
        time.sleep(15)

    ada_days = sorted(chart["usd"])
    ada_vol, ada_drift, _ = fit([chart["usd"][d] for d in ada_days], 365)

    out = {}
    out["USD"] = {"usd": 1.0, "vol": 0.0, "drift": 0.0, "reversion": 1.0}
    for code in CODES:
        if code in VIA_ADA:
            cur = chart[code.lower()]
            common = [d for d in ada_days if d in cur]
            series = [chart["usd"][d] / cur[d] for d in common]
            now = ada_usd / get(f"{COINGECKO}/simple/price?ids=cardano&vs_currencies={code.lower()}")["cardano"][code.lower()]
        else:
            series = [1 / fx["rates"][d][code] for d in days if code in fx["rates"][d]]
            now = 1 / get(f"{FRANKFURTER}/latest?base=USD&symbols={code}")["rates"][code]
        vol, drift, theta = fit(series, 365)
        out[code] = {"usd": float(f"{now:.6g}"), "vol": round(vol, 4), "drift": round(drift, 4), "reversion": round(theta, 2)}
        if code in VIA_ADA:
            time.sleep(15)

    snap = {
        "fetched": today.isoformat(),
        "sources": {
            "fx": "Frankfurter (ECB reference rates), https://frankfurter.dev",
            "ada": "CoinGecko public API, https://www.coingecko.com",
            "via_ada": VIA_ADA,
        },
        "ada": {"usd": ada_usd, "vol": round(ada_vol, 4), "drift": round(ada_drift, 4)},
        "currencies": out,
    }
    with open(OUT, "w") as f:
        json.dump(snap, f, indent=2, sort_keys=True)
        f.write("\n")
    print(f"wrote {OUT}", file=sys.stderr)


if __name__ == "__main__":
    main()
