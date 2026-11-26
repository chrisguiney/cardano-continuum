package httpapi

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"

	"github.com/chrisguiney/cardano-continuum/internal/explorer"
)

var rePoolHex = regexp.MustCompile(`^[0-9a-f]{56}$`)

// exRoutes serves the explorer endpoints over the API the explorer is
// mounted on.
type exRoutes struct{ *api }

// register registers the chain explorer's endpoints:
//
//	GET /api/explorer/status
//	GET /api/explorer/blocks?limit&offset&pool
//	GET /api/explorer/blocks/{hash|height}
//	GET /api/explorer/txs/{hash}
//	GET /api/explorer/txs/{hash}/cbor
//	GET /api/explorer/addresses/{addr}?limit&offset
//	GET /api/explorer/epochs/{n}
//	GET /api/explorer/search?q
func (a exRoutes) register(mux *http.ServeMux) {
	ex := a.Explorer
	mux.HandleFunc("GET /api/explorer/status", func(w http.ResponseWriter, r *http.Request) {
		if _, err := exQuery(r); err != nil {
			a.fail(w, err)
			return
		}
		a.json(w, http.StatusOK, ex.Status())
	})
	mux.HandleFunc("GET /api/explorer/blocks", func(w http.ResponseWriter, r *http.Request) {
		q, err := parseBlockQuery(r)
		if err != nil {
			a.fail(w, err)
			return
		}
		a.reply(w, func() (any, error) { return ex.Blocks(r.Context(), q) })
	})
	mux.HandleFunc("GET /api/explorer/blocks/{key}", func(w http.ResponseWriter, r *http.Request) {
		if _, err := exQuery(r); err != nil {
			a.fail(w, err)
			return
		}
		a.reply(w, func() (any, error) { return ex.Block(r.Context(), r.PathValue("key")) })
	})
	mux.HandleFunc("GET /api/explorer/txs/{hash}", func(w http.ResponseWriter, r *http.Request) {
		if _, err := exQuery(r); err != nil {
			a.fail(w, err)
			return
		}
		a.reply(w, func() (any, error) { return ex.Tx(r.Context(), r.PathValue("hash")) })
	})
	mux.HandleFunc("GET /api/explorer/txs/{hash}/cbor", a.txCbor)
	mux.HandleFunc("GET /api/explorer/addresses/{addr}", func(w http.ResponseWriter, r *http.Request) {
		p, err := parseExPage(r)
		if err != nil {
			a.fail(w, err)
			return
		}
		a.reply(w, func() (any, error) { return ex.Address(r.Context(), r.PathValue("addr"), p) })
	})
	mux.HandleFunc("GET /api/explorer/epochs/{n}", func(w http.ResponseWriter, r *http.Request) {
		n, err := strconv.ParseUint(r.PathValue("n"), 10, 32)
		if err != nil {
			a.fail(w, badf("epoch must be an unsigned integer"))
			return
		}
		if _, err := exQuery(r); err != nil {
			a.fail(w, err)
			return
		}
		a.reply(w, func() (any, error) { return ex.Epoch(r.Context(), n) })
	})
	mux.HandleFunc("GET /api/explorer/search", func(w http.ResponseWriter, r *http.Request) {
		v, err := exQuery(r, "q")
		if err != nil {
			a.fail(w, err)
			return
		}
		a.reply(w, func() (any, error) { return ex.Search(r.Context(), v.Get("q")) })
	})
}

// reply writes f's result, or its error.
func (a exRoutes) reply(w http.ResponseWriter, f func() (any, error)) {
	v, err := f()
	if err != nil {
		a.fail(w, err)
		return
	}
	a.json(w, http.StatusOK, v)
}

func (a exRoutes) txCbor(w http.ResponseWriter, r *http.Request) {
	if _, err := exQuery(r); err != nil {
		a.fail(w, err)
		return
	}
	raw, err := a.Explorer.TxCbor(r.PathValue("hash"))
	if err != nil {
		a.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+r.PathValue("hash")+`.cbor"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(raw)
}

// exQuery parses the query string and refuses a parameter not in allowed.
func exQuery(r *http.Request, allowed ...string) (url.Values, error) {
	v, err := queryOf(r)
	if err != nil {
		return nil, err
	}
	for k := range v {
		ok := false
		for _, a := range allowed {
			ok = ok || a == k
		}
		if !ok {
			return nil, badf("unknown parameter %q", k)
		}
	}
	return v, nil
}

func parseExPage(r *http.Request, extra ...string) (explorer.Page, error) {
	p := explorer.Page{Limit: explorer.DefaultLimit}
	v, err := exQuery(r, append([]string{"limit", "offset"}, extra...)...)
	if err != nil {
		return p, err
	}
	if s := v.Get("limit"); s != "" {
		if p.Limit, err = strconv.Atoi(s); err != nil || p.Limit < 1 || p.Limit > explorer.MaxLimit {
			return p, badf("limit must be an integer in 1..%d", explorer.MaxLimit)
		}
	}
	if s := v.Get("offset"); s != "" {
		if p.Offset, err = strconv.Atoi(s); err != nil || p.Offset < 0 || p.Offset > maxOffset {
			return p, badf("offset must be an integer in 0..%d", maxOffset)
		}
	}
	return p, nil
}

func parseBlockQuery(r *http.Request) (explorer.BlockQuery, error) {
	p, err := parseExPage(r, "pool")
	if err != nil {
		return explorer.BlockQuery{}, err
	}
	q := explorer.BlockQuery{Page: p, Pool: r.URL.Query().Get("pool")}
	if q.Pool != "" && !rePoolHex.MatchString(q.Pool) {
		return q, badf("pool must be 56 lowercase hex digits")
	}
	return q, nil
}
