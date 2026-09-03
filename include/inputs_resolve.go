// inputs_resolve.go — ResolveInputs, the ONE runtime check of a list
// request's sort / pagination / where against the node's Inputs. A framework
// validates only what it derives from its own input struct; the parameters
// apidoc.InputParams documents are enforced here, so document and behaviour
// agree by construction.

package include

import (
	"strconv"
	"strings"
)

// RawInputs is the client's list parameters as bound by the transport. Include
// is NOT here: the include string is parsed by the Hydrator method that
// consumes it. Where arrives already parsed (filters.ParseJSON or
// filters.ParseQuery); nil means no filter.
type RawInputs struct {
	Sort   string
	Page   int
	Cursor string
	Limit  int
	Where  Filter
}

// ResolveInputs validates raw against root's Inputs (DefaultInputs for a node
// that declared none) and returns the QueryArgs to hand a fetcher. Every
// rejection is a *Error with status 400: INVALID_SORT, INVALID_PAGINATION or
// a filter code. On success Limit is never zero.
func ResolveInputs(root Resource, raw RawInputs, opts Options) (QueryArgs, error) {
	in, _ := InputsOf(root)
	var q QueryArgs

	sortKey := raw.Sort
	if sortKey == "" {
		sortKey = in.Sort.Default
	}
	if sortKey != "" {
		if !in.Sort.Enabled {
			return q, NewError(INVALID_SORT, clientEcho(raw.Sort))
		}
		key := strings.TrimPrefix(sortKey, "-")
		sql, ok := in.Sort.Keys[key]
		if !ok {
			return q, NewError(INVALID_SORT, clientEcho(sortKey))
		}
		if strings.HasPrefix(sortKey, "-") {
			sql = "-" + sql
		}
		q.Sort = sql
	}

	switch {
	case raw.Limit < 0 || raw.Limit > in.Page.MaxLimit:
		return q, NewError(INVALID_PAGINATION, "limit="+strconv.Itoa(raw.Limit))
	case raw.Limit == 0:
		q.Limit = in.Page.DefaultLimit
	default:
		q.Limit = raw.Limit
	}

	switch in.Page.Mode {
	case PageModeCursor:
		if raw.Page != 0 {
			return q, NewError(INVALID_PAGINATION, "page="+strconv.Itoa(raw.Page))
		}
		q.Cursor = raw.Cursor
	default:
		if raw.Cursor != "" {
			return q, NewError(INVALID_PAGINATION, "cursor="+clientEcho(raw.Cursor))
		}
		switch {
		case raw.Page < 0:
			return q, NewError(INVALID_PAGINATION, "page="+strconv.Itoa(raw.Page))
		case raw.Page == 0:
			q.Page = 1
		default:
			q.Page = raw.Page
		}
	}

	if raw.Where != nil {
		if !in.Filter.Enabled {
			return q, NewError(INVALID_FILTER, "where")
		}
		rf, err := ResolveFilter(root, raw.Where, opts)
		if err != nil {
			return q, err
		}
		q.Where = rf
	}
	return q, nil
}
