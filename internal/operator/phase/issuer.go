package phase

import (
	"context"
	"log/slog"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/operator/render"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/common"
)

// IssuerOf is the cold-key hash that signs the headers of the pool with the
// given PoolID, the value to match against a header's issuer.
//
// A PoolID that is itself 56 hex characters is that hash. Any other PoolID
// is a label, and the hash comes from the cold key in the opcert.cert of the
// pool's keys Secret (files). A blank or malformed certificate is an error:
// the pod is not Ready while the keys gate fails, so the caller retries.
func IssuerOf(poolID string, files map[string][]byte) (common.Blake2b224, error) {
	var out common.Blake2b224
	if b, err := hex.DecodeString(poolID); err == nil && len(b) == len(out) {
		copy(out[:], b)
		return out, nil
	}
	raw := files[render.OpCert]
	if len(raw) == 0 {
		return out, errors.New("no operational certificate in the keys secret")
	}
	var env struct {
		CborHex string `json:"cborHex"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return out, errors.New("operational certificate is not a text envelope")
	}
	payload, err := hex.DecodeString(env.CborHex)
	if err != nil {
		return out, errors.New("operational certificate has no hex CBOR payload")
	}
	// [[hot_vkey, counter, kes_period, cold_sig], cold_vkey]
	var doc struct {
		cbor.StructAsArray
		Cert     cbor.RawMessage
		ColdVKey []byte
	}
	if _, err := cbor.Decode(payload, &doc); err != nil {
		return out, fmt.Errorf("decode operational certificate: %w", err)
	}
	if len(doc.ColdVKey) != 32 {
		return out, fmt.Errorf("operational certificate has a %d-byte cold key", len(doc.ColdVKey))
	}
	return common.Blake2b224Hash(doc.ColdVKey), nil
}

// issuerCache caches a pod's cold-key hash by pod UID: the keys do not
// change while the pod runs.
type issuerCache struct {
	live client.Reader
	log  *slog.Logger

	mu    sync.Mutex // protects byPod
	byPod map[types.UID]common.Blake2b224
}

// of is the cold-key hash of the pool in pod, read once per pod.
func (c *issuerCache) of(ctx context.Context, p *v1alpha1.ContinuumPool, pod *corev1.Pod) (common.Blake2b224, bool) {
	c.mu.Lock()
	h, ok := c.byPod[pod.UID]
	c.mu.Unlock()
	if ok {
		return h, true
	}
	h, err := IssuerOf(p.Spec.PoolID, nil)
	if err != nil {
		var s corev1.Secret
		if err := c.live.Get(ctx, types.NamespacedName{Namespace: p.Namespace, Name: p.Spec.KeysSecret}, &s); err != nil {
			c.log.Warn("read keys secret", "pool", p.Name, "err", err)
			return h, false
		}
		h, err = IssuerOf(p.Spec.PoolID, s.Data)
	}
	if err != nil {
		c.log.Warn("pool issuer unknown", "pool", p.Name, "err", err)
		return h, false
	}
	c.mu.Lock()
	c.byPod[pod.UID] = h
	c.mu.Unlock()
	return h, true
}
