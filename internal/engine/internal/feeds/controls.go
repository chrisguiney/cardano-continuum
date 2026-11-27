package feeds

import (
	"context"
	"fmt"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
)

// ControlSource hands the loop the controls submitted since the last call,
// each the JSON body of a Control input (wire.Control). A live run's
// HTTP API queues them; the loop logs and applies them in order.
type ControlSource interface {
	Controls(ctx context.Context) ([][]byte, error)
}

// Controls logs and applies every submitted control.
type Controls struct{ src ControlSource }

// NewControls returns the control feed; a nil src has no controls.
func NewControls(src ControlSource) Controls { return Controls{src: src} }

// Poll feeds the controls since the last call, each followed by the
// submissions it decides.
func (c Controls) Poll(ctx context.Context, l Loop) error {
	if c.src == nil {
		return nil
	}
	bodies, err := c.src.Controls(ctx)
	if err != nil {
		return fmt.Errorf("engine: controls: %w", err)
	}
	for _, b := range bodies {
		rec := eventlog.Record{At: l.Now(), Kind: eventlog.KindControl, Body: b}
		if err := l.Feed(ctx, rec); err != nil {
			return err
		}
		if err := l.Effects(ctx); err != nil {
			return err
		}
	}
	return nil
}
