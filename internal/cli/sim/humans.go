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

// humansLine is the summary's humans line: how many humans, how many
// wallets each owns, and the transfers between one owner's wallets, which
// the payments line counts but are not payments between people. Empty for
// a run without humans.
func humansLine(ctx context.Context, wallets []int, r eventlog.Reader) (string, error) {
	if wallets == nil {
		return "", nil
	}

	transfers := 0
	err := r.Scan(ctx, 1, func(rec eventlog.Record) error {
		if rec.Kind != engine.KindSubmitted {
			return nil
		}

		var s engine.Submitted
		if err := eventlog.Decode(rec.Body, &s); err != nil {
			return err
		}

		if s.Kind == string(profile.IntentTransfer) {
			transfers++
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	by, total := map[string]int{}, 0
	for _, n := range wallets {
		key := strconv.Itoa(n)
		if n >= walletBuckets {
			key = strconv.Itoa(walletBuckets) + "+"
		}
		by[key]++
		total += n
	}

	mean := 0.0
	if len(wallets) > 0 {
		mean = float64(total) / float64(len(wallets))
	}
	return fmt.Sprintf("humans: %d owning %d wallets (%.2f each; by count %s); transfers %d\n",
		len(wallets), total, mean, classCounts(by), transfers), nil
}
