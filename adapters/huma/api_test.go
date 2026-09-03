package huma

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	humav2 "github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"

	"github.com/qrotux/wireleaf/apidoc"
	"github.com/qrotux/wireleaf/include"
)

func TestAPINewEmitsComponents(t *testing.T) {
	g := inputsGraph(t)
	a := New(g, include.Options{}, "t", "1")
	if a.opts != include.DefaultOptions {
		t.Fatalf("zero opts = %+v, want DefaultOptions", a.opts)
	}
	if _, ok := a.Components().Get("Book"); !ok {
		t.Fatal("components: want a Book component")
	}
	if a.Graph() != g {
		t.Fatal("Graph() must return the graph New was given")
	}
	cfg := a.Config()
	if cfg.OpenAPI == nil || cfg.OpenAPI.Info == nil || cfg.OpenAPI.Info.Title != "t" || cfg.OpenAPI.Info.Version != "1" {
		t.Fatalf("config info = %+v", cfg.OpenAPI)
	}
	// The document's registry is the bridge over the API's component set.
	if BridgeOf(cfg.OpenAPI).Components() != a.Components() {
		t.Fatal("config registry must bridge the API's components")
	}
}

func TestAPIIncludeDeclaresParamAndError(t *testing.T) {
	g := inputsGraph(t)
	a := New(g, include.DefaultOptions, "t", "1")
	op := Op(humav2.Operation{}, a.Include(g.Resource("Book")))
	if len(op.Parameters) != 1 || op.Parameters[0].Name != "include" || op.Parameters[0].In != "query" {
		t.Fatalf("params = %+v", op.Parameters)
	}
	if op.Responses["400"] == nil || !strings.Contains(op.Responses["400"].Description, "INVALID_INCLUDE") {
		t.Fatalf("400 = %+v", op.Responses["400"])
	}
}

func TestAPIInputsDeclaresParams(t *testing.T) {
	g := inputsGraph(t)
	a := New(g, include.DefaultOptions, "t", "1")
	op := Op(humav2.Operation{}, a.Inputs(g.Resource("Book")))
	var names []string
	for _, p := range op.Parameters {
		names = append(names, p.Name)
	}
	if !reflect.DeepEqual(names, []string{"include", "sort", "page", "limit", "where"}) {
		t.Fatalf("params = %v", names)
	}
	if op.Responses["400"] == nil || !strings.Contains(op.Responses["400"].Description, "INVALID_SORT") {
		t.Fatalf("400 = %+v", op.Responses["400"])
	}
	// idempotent
	op2 := Op(op, a.Inputs(g.Resource("Book")))
	if len(op2.Parameters) != len(op.Parameters) {
		t.Fatal("Inputs must not duplicate parameters")
	}
}

func TestAPIInputsCursorModeAndBracketFilter(t *testing.T) {
	g := cursorGraph(t)
	a := New(g, include.DefaultOptions, "t", "1", WithFilterSyntax(apidoc.FilterBracket))
	op := Op(humav2.Operation{}, a.Inputs(g.Resource("Book")))
	var where *humav2.Param
	var names []string
	for _, p := range op.Parameters {
		names = append(names, p.Name)
		if p.Name == "where" {
			where = p
		}
	}
	if !reflect.DeepEqual(names, []string{"include", "sort", "cursor", "limit", "where"}) {
		t.Fatalf("params = %v", names)
	}
	if where == nil || where.Style != "deepObject" || where.Explode == nil || !*where.Explode {
		t.Fatalf("where = %+v", where)
	}
}

func TestAPIRegisterBeforeAttachPanics(t *testing.T) {
	a := New(inputsGraph(t), include.DefaultOptions, "t", "1")
	assertPanicsWith(t, "Register before Attach", func() {
		Register(a, humav2.Operation{OperationID: "x", Method: http.MethodGet, Path: "/x"},
			func(context.Context, *struct{}) (*struct{}, error) { return nil, nil })
	})
}

func TestAPIRegisterRefusesUnboundNode(t *testing.T) {
	a := New(inputsGraph(t), include.DefaultOptions, "t", "1")
	_, h := humatest.New(t, a.Config())
	a.Attach(h)
	type out struct {
		Body struct {
			Book Node[BookWireT] `json:"book"`
		}
	}
	assertPanicsWith(t, "no Bind registered it", func() {
		Register(a, humav2.Operation{OperationID: "get-book", Method: http.MethodGet, Path: "/books/{id}"},
			func(context.Context, *struct{}) (*out, error) { return nil, nil })
	})
}

func TestAPIRequestMiddleware(t *testing.T) {
	a := New(inputsGraph(t), include.DefaultOptions, "t", "1")
	_, h := humatest.New(t, a.Config())
	a.Attach(h)
	var seen *include.Request
	type in struct {
		ID string `path:"id"`
	}
	Register(a, humav2.Operation{OperationID: "get-x", Method: http.MethodGet, Path: "/x/{id}"},
		func(ctx context.Context, i *in) (*struct{}, error) {
			seen = requestOf(ctx)
			return nil, nil
		})
	h.Get("/x/42?q=1", "X-Remote-Addr: 10.0.0.1")
	if seen == nil || seen.Method != "GET" || seen.Path != "/x/42" || seen.Route != "/x/{id}" || seen.OperationID != "get-x" ||
		seen.PathParams["id"] != "42" || seen.Query.Get("q") != "1" || seen.Header.Get("X-Remote-Addr") != "10.0.0.1" {
		t.Fatalf("request = %+v", seen)
	}
}

func TestAPIRequestOfWithoutMiddleware(t *testing.T) {
	if r := requestOf(context.Background()); r != nil {
		t.Fatalf("requestOf(bare ctx) = %+v, want nil", r)
	}
}
