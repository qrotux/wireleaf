package huma

import (
	"context"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	humav2 "github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"

	"github.com/qrotux/wireleaf/apidoc"
	"github.com/qrotux/wireleaf/graph"
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
	if cfg.OpenAPI == nil || cfg.Info == nil || cfg.Info.Title != "t" || cfg.Info.Version != "1" {
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

func TestAPIAttachTwicePanics(t *testing.T) {
	a := New(inputsGraph(t), include.DefaultOptions, "t", "1")
	_, h := humatest.New(t, a.Config())
	a.Attach(h)
	assertPanicsWith(t, "Attach called twice", func() { a.Attach(h) })
}

// TestNewRefusesNodeNamedLikeALibraryComponent pins that a graph node whose
// name is one the bridge itself installs (Error, the pagination blocks) is
// refused at New with a message naming the collision, not later with a
// generic "re-registered with a different schema" from the installer.
func TestNewRefusesNodeNamedLikeALibraryComponent(t *testing.T) {
	b := graph.NewBuilder()
	n := graph.Node[fixtureRow, BookWireT](b, ErrorComponent).
		Wire(func(r fixtureRow, _ *include.Ctx) BookWireT { return BookWireT{ID: r.ID} }).
		PrimaryKey(func(r fixtureRow) string { return r.ID })
	b.Root(n)
	g, err := b.Compile()
	if err != nil {
		t.Fatal(err)
	}
	assertPanicsWith(t, `node "Error" collides with the library component of the same name`, func() {
		New(g, include.DefaultOptions, "t", "1")
	})
}

// TestRequestMiddlewareWithoutOperation pins the branch huma takes for a
// request that matched no operation (a 404 route): the snapshot still exists,
// with no route and no path params, so requestOf never returns nil past
// Attach.
func TestRequestMiddlewareWithoutOperation(t *testing.T) {
	var seen *include.Request
	requestMiddleware(noOpContext{}, func(c humav2.Context) { seen = requestOf(c.Context()) })
	if seen == nil || seen.Route != "" || seen.OperationID != "" || len(seen.PathParams) != 0 || seen.Path != "/nowhere" || seen.Query.Get("q") != "1" {
		t.Fatalf("request = %+v", seen)
	}
}

// noOpContext is the slice of humav2.Context requestMiddleware reads, with no
// operation behind it. Every other method is a nil-interface panic. The
// embedded interface is aliased so its field name does not shadow Context().
type noOpContext struct{ humaContext }

type humaContext interface{ humav2.Context }

func (noOpContext) Operation() *humav2.Operation    { return nil }
func (noOpContext) Context() context.Context        { return context.Background() }
func (noOpContext) Method() string                  { return http.MethodGet }
func (noOpContext) RemoteAddr() string              { return "" }
func (noOpContext) URL() url.URL                    { return url.URL{Path: "/nowhere", RawQuery: "q=1"} }
func (noOpContext) EachHeader(func(string, string)) {}

// TestAPIRegisterRefusesUnboundCoreNode: the core apidoc.Node[W] wrapper is
// registered by the same Bind, so an output carrying it unbound must be
// refused just like this package's Node[W].
func TestAPIRegisterRefusesUnboundCoreNode(t *testing.T) {
	a := New(inputsGraph(t), include.DefaultOptions, "t", "1")
	_, h := humatest.New(t, a.Config())
	a.Attach(h)
	type out struct {
		Body struct {
			Books map[string]apidoc.Node[BookWireT] `json:"books"`
		}
	}
	assertPanicsWith(t, "no Bind registered it", func() {
		Register(a, humav2.Operation{OperationID: "list-books", Method: http.MethodGet, Path: "/books"},
			func(context.Context, *struct{}) (*out, error) { return nil, nil })
	})
}

func TestAPIRegisterNilAPIPanics(t *testing.T) {
	assertPanicsWith(t, "nil *API", func() {
		Register((*API)(nil), humav2.Operation{OperationID: "x", Method: http.MethodGet, Path: "/x"},
			func(context.Context, *struct{}) (*struct{}, error) { return nil, nil })
	})
}
