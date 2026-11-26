package httpapi

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/chrisguiney/cardano-continuum/internal/store"
)

// actorDir maps an address to the continuum actor that owns it, from the
// store's actor table, reread at most every namesTTL.
type actorDir struct {
	names Names
	now   func() time.Time

	mu     sync.Mutex // protects byAddr, byID, readAt
	byAddr map[string]ActorRef
	byID   map[uint64]ActorRef
	readAt time.Time
}

func newActorDir(n Names, now func() time.Time) *actorDir {
	return &actorDir{names: n, now: now}
}

// ofAddress returns the actor whose payment or stake address is addr.
func (d *actorDir) ofAddress(ctx context.Context, addr string) (*ActorRef, error) {
	if err := d.load(ctx); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if a, ok := d.byAddr[addr]; ok {
		return &a, nil
	}
	return nil, nil
}

// ofID returns the actor with this id.
func (d *actorDir) ofID(ctx context.Context, id uint64) (*ActorRef, error) {
	if err := d.load(ctx); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if a, ok := d.byID[id]; ok {
		return &a, nil
	}
	return nil, nil
}

func (d *actorDir) load(ctx context.Context) error {
	if d.names == nil {
		return nil
	}
	d.mu.Lock()
	fresh := d.byID != nil && d.now().Sub(d.readAt) < namesTTL
	d.mu.Unlock()
	if fresh {
		return nil
	}
	byAddr, byID := map[string]ActorRef{}, map[uint64]ActorRef{}
	for off := 0; ; off += store.MaxLimit {
		page, err := d.names.Actors(ctx, store.ActorQuery{SortBy: "id", Offset: off, Limit: store.MaxLimit})
		if err != nil {
			return fmt.Errorf("read actor names: %w", err)
		}
		for _, r := range page {
			ref := ActorRef{ID: r.ID, Name: r.Name, Role: r.Role}
			byID[r.ID] = ref
			if r.Identity == "" {
				continue
			}
			if in, err := store.DecodeIdentity(r.Identity); err == nil {
				for _, a := range []string{in.PaymentAddr, in.StakeAddr} {
					if a != "" {
						byAddr[a] = ref
					}
				}
			}
		}
		if len(page) < store.MaxLimit {
			break
		}
	}
	d.mu.Lock()
	d.byAddr, d.byID, d.readAt = byAddr, byID, d.now()
	d.mu.Unlock()
	return nil
}
