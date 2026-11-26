package explorer

import (
	"strconv"
	"strings"

	"github.com/blinklabs-io/gouroboros/ledger/common"
)

// Classify says what shape q has. It looks only at the text; Find and the
// callers resolve it.
func Classify(q string) Kind {
	q = strings.TrimSpace(q)
	switch {
	case q == "":
		return KindUnknown
	case strings.HasPrefix(q, "actor:") && allDigits(q[len("actor:"):]):
		return KindActor
	case strings.HasPrefix(q, "@") && allDigits(q[1:]):
		return KindActor
	case strings.HasPrefix(q, "slot:") && allDigits(q[len("slot:"):]):
		return KindSlot
	case allDigits(q):
		return KindNumber
	case isHex(q, 64):
		return KindHash64
	case isHex(q, 56) || strings.HasPrefix(q, "pool1"):
		return KindPool
	}
	for _, p := range []string{"addr1", "addr_test1", "stake1", "stake_test1"} {
		if strings.HasPrefix(q, p) {
			return KindAddress
		}
	}
	return KindUnknown
}

func allDigits(s string) bool {
	if s == "" || len(s) > 20 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func isHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

// find resolves a key to a block, transaction or address the state holds.
func find(st *state, q string) (Hit, bool) {
	q = strings.TrimSpace(q)
	switch Classify(q) {
	case KindHash64:
		return findHash(st, strings.ToLower(q))
	case KindNumber:
		n, _ := strconv.ParseUint(q, 10, 64)
		if pos, ok := st.byHeight[n]; ok {
			return Hit{Kind: KindBlock, ID: st.metas[pos].hashHex()}, true
		}
		return findSlot(st, n)
	case KindSlot:
		n, _ := strconv.ParseUint(q[len("slot:"):], 10, 64)
		return findSlot(st, n)
	case KindAddress:
		return findAddress(st, q)
	}
	return Hit{}, false
}

func findHash(st *state, q string) (Hit, bool) {
	h, err := parseHash(q)
	if err != nil {
		return Hit{}, false
	}
	if _, ok := st.byHash[h]; ok {
		return Hit{Kind: KindBlock, ID: q}, true
	}
	if _, ok := st.txs[h]; ok {
		return Hit{Kind: KindTx, ID: q}, true
	}
	return Hit{}, false
}

// findSlot binary-searches the chain, whose slots only grow.
func findSlot(st *state, slot uint64) (Hit, bool) {
	lo, hi := 0, len(st.metas)
	for lo < hi {
		mid := (lo + hi) / 2
		if st.metas[mid].slot < slot {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo < len(st.metas) && st.metas[lo].slot == slot {
		return Hit{Kind: KindBlock, ID: st.metas[lo].hashHex()}, true
	}
	return Hit{}, false
}

func findAddress(st *state, q string) (Hit, bool) {
	a, err := common.NewAddress(q)
	if err != nil {
		return Hit{}, false
	}
	raw, err := a.Bytes()
	if err != nil {
		return Hit{}, false
	}
	if len(st.utxos.byAddr[string(raw)]) == 0 && len(st.hist.of(string(raw))) == 0 {
		return Hit{}, false
	}
	return Hit{Kind: KindAddress, ID: a.String()}, true
}
