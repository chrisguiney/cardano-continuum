"""agendas.py: a cluster's agenda follows its transaction mix, and its
weights name only intents that agenda can choose."""

import pytest

import agendas

MIX = {"plain": 0.0, "script": 0.0, "stake": 0.0, "pool": 0.0, "gov": 0.0}


def mix(**shares):
    return {**MIX, **shares}


@pytest.mark.parametrize("shares, agenda", [
    (mix(plain=1.0), "hodl"),
    (mix(plain=0.97, script=0.03), "hodl"),       # within the margin: stays a holder
    (mix(plain=0.4, script=0.6), "dapp"),
    (mix(plain=0.78, script=0.22), "dapp"),       # a holder that calls scripts often
    (mix(plain=0.3, stake=0.7), "yield"),
    (mix(plain=0.2, gov=0.7, stake=0.1), "civic"),
    (mix(plain=0.6, stake=0.4), "hodl"),          # yield would lose more than it gains
])
def test_agenda_follows_the_mix(shares, agenda):
    assert agendas.choose_agenda(agendas.intent_shares(shares)) == agenda


def test_minting_script_calls_become_mint():
    shares = agendas.intent_shares(mix(plain=0.5, script=0.5), mint_share=0.2)
    assert shares["mint"] == pytest.approx(0.1) and shares["script_call"] == pytest.approx(0.4)
    weights = agendas.agenda_weights("dapp", shares)
    assert weights == {"send": 0.5, "script_call": 0.4, "mint": 0.1}


def test_weights_sum_to_one_and_dapp_keeps_an_unseen_mint():
    """A weight the engine does not find defaults to 1, so dapp writes
    mint: 0 rather than leaving it out; the sum is exactly 1 after rounding."""
    weights = agendas.agenda_weights("dapp", agendas.intent_shares(mix(plain=1 / 3, script=2 / 3)))
    assert weights["mint"] == 0.0 and sum(weights.values()) == pytest.approx(1.0, abs=1e-9)
    assert agendas.agenda_weights("hodl", agendas.intent_shares(mix(plain=0.5, pool=0.5))) == {"send": 1.0}


def test_tx_probability():
    assert agendas.tx_probability("hodl") == pytest.approx(0.02 / 1.02)
    assert agendas.tx_probability("yield") == 0.5
