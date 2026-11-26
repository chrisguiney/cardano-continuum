package httpapi

import (
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var (
	reLabel = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)
	reKind  = regexp.MustCompile(`^[A-Za-z]{1,32}$`)
)

const maxKinds = 16

// queryOf parses the query string strictly. Request.URL.Query drops a
// malformed pair silently, which would let "sort=id;x" fall back to the
// default instead of being refused.
func queryOf(r *http.Request) (url.Values, error) {
	v, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, badf("query string: %v", err)
	}
	return v, nil
}

// parseActorQuery validates the actors query string into a typed query.
func parseActorQuery(r *http.Request) (ActorQuery, error) {
	q := ActorQuery{Sort: "id", Limit: DefaultLimit}
	v, err := queryOf(r)
	if err != nil {
		return q, err
	}
	if s := v.Get("limit"); s != "" {
		if q.Limit, err = strconv.Atoi(s); err != nil || q.Limit < 1 || q.Limit > MaxLimit {
			return q, badf("limit must be an integer in 1..%d", MaxLimit)
		}
	}
	if s := v.Get("offset"); s != "" {
		if q.Offset, err = strconv.Atoi(s); err != nil || q.Offset < 0 || q.Offset > maxOffset {
			return q, badf("offset must be an integer in 0..%d", maxOffset)
		}
	}
	if s := v.Get("sort"); s != "" {
		if !slices.Contains(SortKeys, s) {
			return q, badf("sort must be one of %s", strings.Join(SortKeys, ", "))
		}
		q.Sort = s
	}
	switch v.Get("dir") {
	case "", "asc":
	case "desc":
		q.Desc = true
	default:
		return q, badf("dir must be asc or desc")
	}
	for name, dst := range map[string]*string{"profile": &q.Filter.Profile, "state": &q.Filter.State, "role": &q.Filter.Role, "kind": &q.Filter.Kind} {
		if s := v.Get(name); s != "" {
			if !reLabel.MatchString(s) {
				return q, badf("%s must match %s", name, reLabel)
			}
			*dst = s
		}
	}
	switch v.Get("delegated") {
	case "":
	case "true", "false":
		d := v.Get("delegated") == "true"
		q.Filter.Delegated = &d
	default:
		return q, badf("delegated must be true or false")
	}
	return q, nil
}

// parseFilter validates the decision feed's filter parameters.
func parseFilter(v url.Values) (Filter, error) {
	var f Filter
	if s := v.Get("actor"); s != "" {
		id, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return f, badf("actor must be an unsigned integer")
		}
		f.Actor = &id
	}
	if s := v.Get("profile"); s != "" {
		if !reLabel.MatchString(s) {
			return f, badf("profile must match %s", reLabel)
		}
		f.Profile = s
	}
	if s := v.Get("intent"); s != "" {
		if !slices.Contains(BuildEnums().IntentKind, s) {
			return f, badf("intent must be one of %s", strings.Join(BuildEnums().IntentKind, ", "))
		}
		f.Intent = s
	}
	for _, raw := range v["kind"] {
		for _, k := range strings.Split(raw, ",") {
			if !reKind.MatchString(k) {
				return f, badf("kind must match %s", reKind)
			}
			f.Kinds = append(f.Kinds, k)
		}
	}
	if len(f.Kinds) > maxKinds {
		return f, badf("at most %d kinds", maxKinds)
	}
	if s := v.Get("vote"); s != "" {
		b, err := strconv.ParseBool(s)
		if err != nil {
			return f, badf("vote must be true or false")
		}
		f.Vote = b
	}
	return f, nil
}

// parseFindingFilter validates the findings kind and severity parameters
// against the oracle's own lists.
func parseFindingFilter(r *http.Request) (Page, error) {
	var p Page
	v, err := queryOf(r)
	if err != nil {
		return p, err
	}
	e := BuildEnums()
	if s := v.Get("kind"); s != "" {
		if !slices.Contains(e.FindingKind, s) {
			return p, badf("kind must be one of %s", strings.Join(e.FindingKind, ", "))
		}
		p.Kind = s
	}
	if s := v.Get("severity"); s != "" {
		if !slices.Contains(e.Severity, s) {
			return p, badf("severity must be one of %s", strings.Join(e.Severity, ", "))
		}
		p.Severity = s
	}
	return p, nil
}

// parseAfter reads the client's resume point from the Last-Event-ID header,
// or the last_event_id parameter for clients that cannot set headers.
func parseAfter(header string, v url.Values) (*uint64, error) {
	s := header
	if s == "" {
		s = v.Get("last_event_id")
	}
	if s == "" {
		return nil, nil
	}
	id, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return nil, badf("Last-Event-ID must be an unsigned integer")
	}
	return &id, nil
}
