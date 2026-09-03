package huma

// request.go — the per-request snapshot the engine reads.
//
// include.Ctx exposes Header / PathParam / QueryValue to Wire functions,
// Guards, Enrich hooks and fetchers, and it reads them off an
// *include.Request an ADAPTER fills. This file is that adapter half: one huma
// middleware that snapshots the request once, and one accessor for the
// handler side.

import (
	"context"
	"net/http"
	"regexp"

	humav2 "github.com/danielgtaylor/huma/v2"

	"github.com/qrotux/wireleaf/include"
)

// requestKey is the private context key the snapshot is stored under: private
// so nothing outside this package can substitute a forged request.
type requestKey struct{}

// templateParamRE matches one {name} placeholder of a route template.
var templateParamRE = regexp.MustCompile(`\{([^}]+)\}`)

// requestMiddleware snapshots the request into an include.Request and stores
// it on the Go context, where Bound.Get/List pick it up for include.Ctx.
// Installed by API.Attach; it runs after routing, so the operation and its
// path parameters are known.
func requestMiddleware(ctx humav2.Context, next func(humav2.Context)) {
	u := ctx.URL()
	r := &include.Request{
		Method:     ctx.Method(),
		Path:       u.Path,
		Query:      u.Query(),
		Header:     http.Header{},
		RemoteAddr: ctx.RemoteAddr(),
		PathParams: map[string]string{},
	}
	if op := ctx.Operation(); op != nil {
		r.Route = op.Path
		r.OperationID = op.OperationID
		for _, m := range templateParamRE.FindAllStringSubmatch(op.Path, -1) {
			r.PathParams[m[1]] = ctx.Param(m[1])
		}
	}
	ctx.EachHeader(func(name, value string) { r.Header.Add(name, value) })
	next(humav2.WithValue(ctx, requestKey{}, r))
}

// requestOf returns the request snapshot the middleware stored, or nil.
func requestOf(ctx context.Context) *include.Request {
	r, _ := ctx.Value(requestKey{}).(*include.Request)
	return r
}
