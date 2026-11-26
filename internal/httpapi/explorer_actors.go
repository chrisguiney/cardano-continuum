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
	byAddr, byID, err := d.read(ctx)
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.byAddr, d.byID, d.readAt = byAddr, byID, d.now()
	d.mu.Unlock()
	return nil
}

// read pages through the store's actor table.
func (d *actorDir) read(ctx context.Context) (map[string]ActorRef, map[uint64]ActorRef, error) {
	byAddr, byID := map[string]ActorRef{}, map[uint64]ActorRef{}
	for off := 0; ; off += store.MaxLimit {
		page, err := d.names.Actors(ctx, store.ActorQuery{SortBy: "id", Offset: off, Limit: store.MaxLimit})
		if err != nil {
			return nil, nil, fmt.Errorf("read actor names: %w", err)
		}
		for _, r := range page {
			indexActor(r, byAddr, byID)
		}
		if len(page) < store.MaxLimit {
			return byAddr, byID, nil
		}
	}
}

// indexActor records a row by id and, when its identity parses, by address. A
// row whose identity does not parse is still found by id.
func indexActor(r store.ActorRow, byAddr map[string]ActorRef, byID map[uint64]ActorRef) {
	ref := ActorRef{ID: r.ID, Name: r.Name, Role: r.Role}
	byID[r.ID] = ref
	if r.Identity == "" {
		return
	}
	in, err := store.DecodeIdentity(r.Identity)
	if err != nil {
		return
	}
	for _, a := range []string{in.PaymentAddr, in.StakeAddr} {
		if a != "" {
			byAddr[a] = ref
		}
	}
}
