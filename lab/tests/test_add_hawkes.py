"""add_hawkes.py: Hawkes parameters for a shipped profile from its target."""

import json

import numpy as np

import add_hawkes
import population_fit

PROFILE = """# Fitted by lab/fit.py --clusters; see lab/README.md. Do not edit by hand.
# network: test
version: 1
profiles:
  - name: a
    kind: wallet
    agenda: hodl
    rates:
      base:
        send: 0.001
      activity_tail: 2
    weights:
      send: 1.000000
  - name: b
    kind: wallet
    agenda: hodl
    rates:
      base:
        send: 0.002
      activity_tail: 1.5
      hawkes: {alpha: 0.1, beta: 0.2}
populations:
  - name: pools
"""


def test_every_wallet_profile_gets_the_overall_fit(tmp_path):
    log10 = np.concatenate([np.zeros(500), np.linspace(0.5, 2.4, 250), np.linspace(4, 6, 251)])
    target = {"overall": {"quantiles": {"log10_gap_seconds": list(log10)}}}
    (tmp_path / "t.json").write_text(json.dumps(target))
    (tmp_path / "p.yaml").write_text(PROFILE)
    add_hawkes.main([str(tmp_path / "p.yaml"), str(tmp_path / "t.json")])
    text = (tmp_path / "p.yaml").read_text()
    h = population_fit.fit_hawkes(np.where(log10 <= 0, 0.0, 10.0 ** log10))
    line = f"      hawkes: {{alpha: {add_hawkes.number(h.alpha)}, beta: {add_hawkes.number(h.beta)}}}"
    assert text.count(line) == 2 and "alpha: 0.1" not in text
    assert "# hawkes: " in text.splitlines()[2]
    add_hawkes.main([str(tmp_path / "p.yaml"), str(tmp_path / "t.json")])
    assert (tmp_path / "p.yaml").read_text() == text
