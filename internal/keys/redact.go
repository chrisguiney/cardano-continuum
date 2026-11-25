package keys

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
)

// redaction is the only text any secret-holding type ever renders. The
// fingerprint is derived from public material (a key hash), so it identifies
// a key in logs without disclosing it.
type redaction struct {
	kind string
	fp   []byte
}

func (r redaction) String() string {
	return fmt.Sprintf("[redacted keys.%s %s]", r.kind, hex.EncodeToString(r.fp))
}

func (r redaction) format(f fmt.State) {
	_, _ = fmt.Fprint(f, r.String())
}

func (r redaction) marshal() ([]byte, error) { return json.Marshal(r.String()) }

func (r redaction) logValue() slog.Value { return slog.StringValue(r.String()) }
