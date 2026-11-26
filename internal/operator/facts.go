package operator

import (
	"context"
	"log/slog"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/genesis"
)

// FactsOf reads the network parameters a phase is judged against from a
// shelley-genesis.json. Share is 1/len(staking.pools): the genesis gives
// every pool the same stake (internal/genesis PoolStake).
func FactsOf(shelley []byte) (Facts, error) {
	var g struct {
		ActiveSlotsCoeff float64 `json:"activeSlotsCoeff"`
		EpochLength      uint64  `json:"epochLength"`
		SecurityParam    uint64  `json:"securityParam"`
		SlotLength       float64 `json:"slotLength"`
		SystemStart      string  `json:"systemStart"`
		Staking          struct {
			Pools map[string]json.RawMessage `json:"pools"`
		} `json:"staking"`
	}
	if err := json.Unmarshal(shelley, &g); err != nil {
		return Facts{}, fmt.Errorf("decode shelley genesis: %w", err)
	}
	if g.SlotLength <= 0 {
		return Facts{}, errors.New("shelley genesis has no slotLength")
	}
	start, err := time.Parse(time.RFC3339, g.SystemStart)
	if err != nil {
		return Facts{}, fmt.Errorf("shelley genesis systemStart: %w", err)
	}
	f := Facts{
		Start: start, SlotLen: time.Duration(g.SlotLength * float64(time.Second)),
		F: g.ActiveSlotsCoeff, Epoch: g.EpochLength, K: g.SecurityParam,
	}
	if n := len(g.Staking.Pools); n > 0 {
		f.Share = 1 / float64(n)
	}
	return f, nil
}

type genesisKey struct {
	net types.UID
	ref string
}

// genesisFacts caches each network's Facts by network and genesis
// reference, read through live.
type genesisFacts struct {
	live client.Reader
	log  *slog.Logger

	mu    sync.Mutex // protects byKey
	byKey map[genesisKey]Facts
}

// of reads the network's genesis once per network and genesis
// reference. A failed read is not cached, so the next reconcile retries and
// meanwhile the phase uses the best tip as the clock.
func (c *genesisFacts) of(ctx context.Context, net *v1alpha1.ContinuumNetwork) Facts {
	k := genesisKey{net: net.UID, ref: net.Spec.GenesisRef}
	c.mu.Lock()
	f, ok := c.byKey[k]
	c.mu.Unlock()
	if ok {
		return f
	}
	var cm corev1.ConfigMap
	err := c.live.Get(ctx, types.NamespacedName{Namespace: net.Namespace, Name: net.Spec.GenesisRef}, &cm)
	if err == nil {
		f, err = FactsOf([]byte(cm.Data[genesis.ShelleyFile]))
	}
	if err != nil {
		c.log.Warn("network facts unavailable, using the best tip as the clock", "network", net.Name, "err", err)
		return Facts{Epoch: uint64(max(net.Spec.EpochLength, 0)), K: uint64(max(net.Spec.SecurityParam, 0))}
	}
	f.Epoch, f.K = uint64(max(net.Spec.EpochLength, 0)), uint64(max(net.Spec.SecurityParam, 0))
	c.mu.Lock()
	c.byKey[k] = f
	c.mu.Unlock()
	return f
}
