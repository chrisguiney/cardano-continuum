package explorer

// history is each address's transaction ids, oldest first, one entry per
// transaction.
type history struct {
	m map[string][][32]byte
}

func newHistory() *history { return &history{m: map[string][][32]byte{}} }

// add notes that tx touched addr; a transaction that touches an address
// twice is listed once.
func (h *history) add(addr string, tx [32]byte) bool {
	l := h.m[addr]
	if n := len(l); n > 0 && l[n-1] == tx {
		return false
	}
	h.m[addr] = append(l, tx)
	return true
}

// drop removes the newest entry of addr if it is tx: a rollback unwinds
// the newest blocks first, so it is always the last one.
func (h *history) drop(addr string, tx [32]byte) {
	l := h.m[addr]
	if n := len(l); n > 0 && l[n-1] == tx {
		if n == 1 {
			delete(h.m, addr)
			return
		}
		h.m[addr] = l[:n-1]
	}
}

func (h *history) of(addr string) [][32]byte { return h.m[addr] }
