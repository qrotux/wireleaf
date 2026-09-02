package huma

// op.go — the operation layer: what an application declares ABOUT an operation
// that huma's Register cannot infer from the Go types.
//
// Two shapes, and the difference is not stylistic:
//
//   - Op(base, opts...) decorates the humav2.Operation BEFORE huma.Register.
//     Everything expressible on the operation VALUE belongs here — error
//     responses, the ?include= parameter — because a value huma has not seen yet
//     cannot be inconsistent with the document.
//   - ApplyRequestBodyDoc / ApplyResponseHeaders run AFTER huma.Register,
//     because both REPLACE something huma itself emitted: the auto
//     application/octet-stream requestBody it derives from a RawBody field, and
//     the response objects it creates per status.

import (
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"

	humav2 "github.com/danielgtaylor/huma/v2"

	"github.com/qrotux/wireleaf/apidoc"
	"github.com/qrotux/wireleaf/include"
)

// ErrorDef is one catalogued contract error: the HTTP status it is returned
// with, the stable machine code, and the human message. It is the doc-facing
// half of an application's error catalog.
type ErrorDef struct {
	Status  int
	Code    string
	Message string
}

// OpOpt decorates an operation before registration.
type OpOpt func(*humav2.Operation)

// Op applies the decorators to base and returns the operation to hand to
// huma.Register.
//
// base is taken BY VALUE, and the two reference fields a decorator writes are
// detached before anything runs: the Responses map is cloned, and Parameters is
// capacity-clamped so an append allocates instead of writing into the caller's
// spare capacity. Without both, a shared base template (the reason to have one)
// would leak one route's errors and parameters into the next.
func Op(base humav2.Operation, opts ...OpOpt) humav2.Operation {
	if base.Responses != nil {
		base.Responses = maps.Clone(base.Responses)
	}
	base.Parameters = base.Parameters[:len(base.Parameters):len(base.Parameters)]
	for _, opt := range opts {
		if opt != nil {
			opt(&base)
		}
	}
	return base
}

// Errors documents the operation's declared error surface: one response per
// STATUS (sorted), whose description joins every code returned with that status
// as "CODE (HTTP n): message" with "; ", and whose body is a $ref to the Error
// component.
//
// One response per status, not per code, because OpenAPI keys responses by
// status: two codes sharing a status are one response with a compound
// description, not a silent last-writer-wins.
func Errors(defs ...ErrorDef) OpOpt {
	return func(op *humav2.Operation) {
		if len(defs) == 0 {
			return
		}
		if op.Responses == nil {
			op.Responses = map[string]*humav2.Response{}
		}
		byStatus := map[int][]ErrorDef{}
		var statuses []int
		for _, def := range defs {
			if _, seen := byStatus[def.Status]; !seen {
				statuses = append(statuses, def.Status)
			}
			byStatus[def.Status] = append(byStatus[def.Status], def)
		}
		sort.Ints(statuses)
		for _, status := range statuses {
			parts := make([]string, 0, len(byStatus[status]))
			for _, def := range byStatus[status] {
				parts = append(parts, fmt.Sprintf("%s (HTTP %d): %s", def.Code, def.Status, def.Message))
			}
			op.Responses[strconv.Itoa(status)] = &humav2.Response{
				Description: strings.Join(parts, "; "),
				Content: map[string]*humav2.MediaType{
					"application/json": {Schema: &humav2.Schema{Ref: apidoc.RefPrefix + ErrorComponent}},
				},
			}
		}
	}
}

// IncludeParam appends the ?include= query parameter for res: an optional
// string carrying every valid include path under the x-include-paths extension,
// enumerated from the graph itself (a depth-first walk over includable edges
// bounded by include.DefaultLimits). A handler that plans with other Limits
// should use IncludeParamWithLimits so the document matches what it accepts.
//
// The paths are DOCUMENTED, not enumerated as an enum: the value is a
// comma-separated path LIST with per-edge arguments, so no single-value enum
// describes it. A generator reads the extension.
//
// IDEMPOTENT: an operation that already declares an "include" query parameter
// (a base template that carries one, or a second IncludeParam) is left alone —
// two parameters of the same name in the same location is an invalid OpenAPI
// document, and the first declaration is the one the caller meant.
func IncludeParam(res include.Resource) OpOpt {
	return IncludeParamWithLimits(res, include.DefaultLimits)
}

// IncludeParamWithLimits is IncludeParam with the path enumeration bounded by
// lim — pass the same include.Limits the handler passes to the planner. A
// zero-value lim means include.DefaultLimits.
func IncludeParamWithLimits(res include.Resource, lim include.Limits) OpOpt {
	if lim == (include.Limits{}) {
		lim = include.DefaultLimits
	}
	return func(op *humav2.Operation) {
		for _, p := range op.Parameters {
			if p != nil && p.Name == "include" && p.In == "query" {
				return
			}
		}
		frag := apidoc.IncludeParamSchema(apidoc.IncludePaths(res, lim))
		schema, err := toHuma(apidoc.RawFragment(frag).IR())
		if err != nil {
			panic(fmt.Sprintf("adapters/huma: building the include parameter schema: %v", err))
		}
		op.Parameters = append(op.Parameters, &humav2.Param{
			Name:     "include",
			In:       "query",
			Required: false,
			Schema:   schema,
		})
	}
}

// ---------------------------------------------------------------------------
// post-registration document fixes
// ---------------------------------------------------------------------------

// ResponseHeader is one declared response header. Schema is a raw JSON-schema
// fragment ({"type":"string"} and friends): a header schema is a leaf, and a
// map keeps the declaration at the call site short.
type ResponseHeader struct {
	Name        string
	Description string
	Schema      map[string]any
	Required    bool
}

// ApplyRequestBodyDoc replaces the operation's requestBody with the JSON schema
// of bodyT.
//
// It runs AFTER huma.Register because that is what it undoes: an operation
// whose input carries `RawBody []byte` (the shape wireleaf's write path uses,
// so the handler owns the raw bytes) makes huma emit an
// application/octet-stream body. The declared schema comes from bodyT's
// RequestBodyProvider when it has one, else from the bridge — the same
// reflector the rest of the document came from.
//
// Required is FALSE: an absent body is a validation concern of the handler, not
// of the document.
//
// The naming HINT is derived from the operation, never fixed: an ANONYMOUS body
// struct is named from the hint alone, so a constant would give two operations'
// unrelated bodies one component name — a conflict at best, a silently shared
// schema at worst.
func ApplyRequestBodyDoc(oapi *humav2.OpenAPI, method, path string, bodyT reflect.Type) {
	b := BridgeOf(oapi)
	op := operationAt(oapi, method, path)
	var schema *humav2.Schema
	if s, ok := requestBodySchemaOf(bodyT); ok {
		var err error
		if schema, err = toHuma(s.IR()); err != nil {
			panic(fmt.Sprintf("adapters/huma: converting the declared request body of %v: %v", bodyT, err))
		}
	} else {
		schema = b.Schema(bodyT, true, requestBodyHint(op, method, path))
	}
	op.RequestBody = &humav2.RequestBody{
		Required: false,
		Content: map[string]*humav2.MediaType{
			"application/json": {Schema: schema},
		},
	}
}

// requestBodyHint is the per-operation naming hint: the operation id when there
// is one (huma's own convention, "<OperationID>RequestBody"), else the
// method+path, which is unique by construction. DefaultSchemaNamer strips the
// punctuation.
func requestBodyHint(op *humav2.Operation, method, path string) string {
	if op.OperationID != "" {
		return op.OperationID + "RequestBody"
	}
	return method + path + "RequestBody"
}

// ApplyResponseHeaders attaches declared response headers to the already-emitted
// response of each status (success or error). A status with no response yet
// gets a bare one (a header-only response).
//
// The emitted humav2.Param carries NO name/in — both are omitempty — so it
// marshals as the OpenAPI Header object {schema, description[, required]}.
//
// COPY-ON-WRITE: a *Response in the map may be shared — a base template's
// inherited response, or the same pointer registered on two operations — so a
// decorated response is REPLACED by a shallow copy carrying its own Headers map.
// Writing through the pointer would put one route's headers on every route that
// inherited the response.
func ApplyResponseHeaders(oapi *humav2.OpenAPI, method, path string, headers map[int][]ResponseHeader) {
	if len(headers) == 0 {
		return
	}
	op := operationAt(oapi, method, path)
	if op.Responses == nil {
		op.Responses = map[string]*humav2.Response{}
	}
	statuses := make([]int, 0, len(headers))
	for status := range headers {
		statuses = append(statuses, status)
	}
	sort.Ints(statuses) // deterministic order for a deterministic panic/diff
	for _, status := range statuses {
		key := strconv.Itoa(status)
		var resp *humav2.Response
		if prev := op.Responses[key]; prev != nil {
			cp := *prev // never write through a possibly-shared pointer
			resp = &cp
		} else {
			resp = &humav2.Response{}
		}
		resp.Headers = maps.Clone(resp.Headers)
		if resp.Headers == nil {
			resp.Headers = map[string]*humav2.Param{}
		}
		op.Responses[key] = resp
		for _, h := range headers[status] {
			resp.Headers[h.Name] = &humav2.Param{
				Description: h.Description,
				Required:    h.Required,
				Schema:      &humav2.Schema{Extensions: h.Schema},
			}
		}
	}
}

// operationAt finds the registered operation. Both helpers above run right
// after huma.Register, so a miss is a caller bug (a typo'd path, a method that
// was never registered) — loud, not silent.
func operationAt(oapi *humav2.OpenAPI, method, path string) *humav2.Operation {
	if oapi != nil && oapi.Paths != nil {
		if pi := oapi.Paths[path]; pi != nil {
			if op := operationOf(pi, method); op != nil {
				return op
			}
		}
	}
	panic(fmt.Sprintf("adapters/huma: operation %s %s is not in the OpenAPI doc (register it first)", method, path))
}

func operationOf(pi *humav2.PathItem, method string) *humav2.Operation {
	switch strings.ToUpper(method) {
	case http.MethodGet:
		return pi.Get
	case http.MethodPut:
		return pi.Put
	case http.MethodPost:
		return pi.Post
	case http.MethodDelete:
		return pi.Delete
	case http.MethodOptions:
		return pi.Options
	case http.MethodHead:
		return pi.Head
	case http.MethodPatch:
		return pi.Patch
	case http.MethodTrace:
		return pi.Trace
	}
	return nil
}
