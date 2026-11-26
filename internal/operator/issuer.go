package operator

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

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
	raw := files[OpCert]
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
