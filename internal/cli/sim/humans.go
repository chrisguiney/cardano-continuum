package sim

import (
	"context"
	"fmt"
	"strconv"

	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// walletBuckets are the wallets-per-human counts the humans line shows;
// the last bucket holds that many and more.
const walletBuckets = 5

// money is the fiat side of a humans run as its records tell it, in cents
// summed over currencies.
type money struct {
	// income, paid, bought and sold are the Income, Expense (paid) and
	// Traded (buy and sell) cents.
	income, paid, bought, sold int64
	// months, unemployed and short count paydays, paydays out of work and
	// months whose bills went partly unpaid.
	months, unemployed, short int
	// invested counts the purchases from income, transfers the moves
	// between one owner's wallets.
	invested, transfers int
}

func (m *money) add(rec eventlog.Record) error {
	switch rec.Kind {
	case engine.KindIncome:
		var b engine.Income
		if err := eventlog.Decode(rec.Body, &b); err != nil {
			return err
		}
		m.income += b.Cents
		m.months++
		if !b.Employed {
			m.unemployed++
		}
	case engine.KindExpense:
		var b engine.Expense
		if err := eventlog.Decode(rec.Body, &b); err != nil {
			return err
		}
		m.paid += b.Paid
		if b.Short > 0 {
			m.short++
		}
	case engine.KindTraded:
		var b engine.Traded
		if err := eventlog.Decode(rec.Body, &b); err != nil {
			return err
		}
		if b.Side == "buy" {
			m.bought += b.Cents
		} else {
			m.sold += b.Cents
		}
	case engine.KindInvested:
		m.invested++
	case engine.KindSubmitted:
		var b engine.Submitted
		if err := eventlog.Decode(rec.Body, &b); err != nil {
			return err
		}
		if b.Kind == string(profile.IntentTransfer) {
			m.transfers++
		}
	}
	return nil
}

// humansLine is the summary's humans line: how many humans and how many
// wallets each owns, the transfers between one owner's wallets (the
// payments line counts them, but they are no payment between people), and
// the household money. conserved reports that the humans' fiat now is
// what they started with, plus their pay, less their bills and purchases,
// plus their sales. Empty and conserved for a run without humans.
func humansLine(ctx context.Context, s engine.Summary, r eventlog.Reader) (line string, conserved bool, err error) {
	if s.Wallets == nil {
		return "", true, nil
	}

	var m money
	if err := r.Scan(ctx, 1, func(rec eventlog.Record) error { return m.add(rec) }); err != nil {
		return "", false, err
	}

	by, total := map[string]int{}, 0
	for _, n := range s.Wallets {
		key := strconv.Itoa(n)
		if n >= walletBuckets {
			key = strconv.Itoa(walletBuckets) + "+"
		}
		by[key]++
		total += n
	}

	mean := 0.0
	if len(s.Wallets) > 0 {
		mean = float64(total) / float64(len(s.Wallets))
	}

	conserved = s.Fiat == s.Fiat0+m.income-m.paid-m.bought+m.sold
	line = fmt.Sprintf("humans: %d owning %d wallets (%.2f each; by count %s); transfers %d\n",
		len(s.Wallets), total, mean, classCounts(by), m.transfers)
	if m.months > 0 {
		line += fmt.Sprintf("household: %d paydays (%d out of work, %d short of bills); %d purchases from income (%.2f per payday); fiat conserved: %t (start %d, pay %d, bills %d, bought %d, sold %d, now %d cents)\n",
			m.months, m.unemployed, m.short, m.invested, float64(m.invested)/float64(m.months), conserved,
			s.Fiat0, m.income, m.paid, m.bought, m.sold, s.Fiat)
	}
	return line, conserved, nil
}
