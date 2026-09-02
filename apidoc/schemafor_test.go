package apidoc

// schemafor_test.go — the include-aware recompute spec, in-core over the stub
// reflector. Ported from the v0 adapter suite (adapters/huma/schemafor_test.go)
// onto the hand-built emitRes fixtures shared with emit_test.go.
//
// RECORDED DRIFT vs v0: SchemaFor returns a Schema (not map[string]any), and
// the IR serializer OMITS an empty `required` rather than emitting `[]`.

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/qrotux/wireleaf/include"
)

// allOptionalWire has only omitempty fields, so its recomputed base carries NO
// required keys at all — the fixture for the empty-required drift.
type allOptionalWire struct {
	ID string `json:"id,omitempty"`
}

func TestSchemaForPromotesRequestedEdgeToRequired(t *testing.T) {
	g := buildToyGraph()
	s, err := SchemaFor(stubReflector{}, g.Book, include.IncludeTree{"author": nil}, include.Limits{})
	if err != nil {
		t.Fatalf("SchemaFor: %v", err)
	}
	got := s.Map()

	if req := requiredKeys(t, got); !contains(req, "author") {
		t.Errorf("requested edge not promoted to required: %v", req)
	}
	// A leaf request reuses the exact static edge shape.
	if want := anyOfNull(ref("Author")); !reflect.DeepEqual(prop(t, got, "author"), want) {
		t.Errorf("author fragment = %v, want %v", prop(t, got, "author"), want)
	}
	// Edges the client did NOT ask for are absent — SchemaFor stitches only the tree.
	for _, key := range []string{"reviews", "similar", "images"} {
		if hasProp(got, key) {
			t.Errorf("unrequested edge %q present in the recomputed schema", key)
		}
	}
}

func TestSchemaForInlinesNonEmptySubtree(t *testing.T) {
	g := buildToyGraph()
	s, err := SchemaFor(stubReflector{}, g.Book, include.IncludeTree{
		"author": include.IncludeTree{"books": nil},
	}, include.Limits{})
	if err != nil {
		t.Fatalf("SchemaFor: %v", err)
	}
	got := s.Map()

	arms, ok := prop(t, got, "author")["anyOf"].([]any)
	if !ok || len(arms) != 2 {
		t.Fatalf("author fragment = %v, want anyOf[inner, null]", prop(t, got, "author"))
	}
	inner, ok := arms[0].(map[string]any)
	if !ok {
		t.Fatalf("inlined arm = %T", arms[0])
	}
	if _, isRef := inner["$ref"]; isRef {
		t.Fatalf("non-empty subtree must inline the target, got a $ref: %v", inner)
	}
	if req := requiredKeys(t, inner); !contains(req, "books") {
		t.Errorf("inlined Author required = %v, want it to carry books", req)
	}
	// The inlined child's own leaf edge falls back to the static $ref envelope —
	// which, over the IR, carries the OPTIONAL nextCursor (drift vs the v0 map
	// envelope, which had none).
	wantBooks := envelopeOf(ref("Book"))
	if got := prop(t, inner, "books"); !reflect.DeepEqual(got, wantBooks) {
		t.Errorf("inlined Author.books = %v, want %v", got, wantBooks)
	}
}

// A bare to-many inlines as array<inner>, mirroring its static array<$ref>.
func TestSchemaForInlinesBareEdgeAsArray(t *testing.T) {
	g := buildToyGraph()
	s, err := SchemaFor(stubReflector{}, g.Book, include.IncludeTree{
		"similar": include.IncludeTree{"author": nil},
	}, include.Limits{})
	if err != nil {
		t.Fatalf("SchemaFor: %v", err)
	}
	got := s.Map()
	arms, _ := prop(t, got, "similar")["anyOf"].([]any)
	if len(arms) != 2 {
		t.Fatalf("similar fragment = %v", prop(t, got, "similar"))
	}
	arr, _ := arms[0].(map[string]any)
	if arr["type"] != "array" {
		t.Fatalf("bare edge should inline as an array, got %v", arr)
	}
	items, ok := arr["items"].(map[string]any)
	if !ok {
		t.Fatalf("items = %T", arr["items"])
	}
	if req := requiredKeys(t, items); !contains(req, "author") {
		t.Errorf("inlined Book required = %v, want it to carry author", req)
	}
}

// An in-array edge with a requested subtree inlines the recomputed target into
// the subField POSITION, keeping the row envelope intact.
func TestSchemaForInlinesInArraySubtree(t *testing.T) {
	g := buildToyGraph()
	g.Image.edges = map[string]include.Edge{
		"credit": {Target: func() include.Resource { return g.Author }, Includable: true},
	}
	s, err := SchemaFor(stubReflector{}, g.Book, include.IncludeTree{
		"images": include.IncludeTree{"credit": nil},
	}, include.Limits{})
	if err != nil {
		t.Fatalf("SchemaFor: %v", err)
	}
	got := s.Map()

	arms, _ := prop(t, got, "images")["anyOf"].([]any)
	if len(arms) != 2 {
		t.Fatalf("images fragment = %v", prop(t, got, "images"))
	}
	arr, _ := arms[0].(map[string]any)
	if arr["type"] != "array" {
		t.Fatalf("in-array edge must stay an array of rows, got %v", arr)
	}
	row, ok := arr["items"].(map[string]any)
	if !ok {
		t.Fatalf("row = %T", arr["items"])
	}
	if req := requiredKeys(t, row); !contains(req, "image") {
		t.Errorf("row required = %v, want it to carry the subField", req)
	}
	subArms, _ := prop(t, row, "image")["anyOf"].([]any)
	if len(subArms) != 2 {
		t.Fatalf("subField fragment = %v", prop(t, row, "image"))
	}
	inner, ok := subArms[0].(map[string]any)
	if !ok {
		t.Fatalf("inlined subField arm = %T", subArms[0])
	}
	if _, isRef := inner["$ref"]; isRef {
		t.Fatalf("in-array subtree must inline the target, got a $ref: %v", inner)
	}
	if req := requiredKeys(t, inner); !contains(req, "credit") {
		t.Errorf("inlined Image required = %v, want it to carry credit", req)
	}
}

// ":"-prefixed keys are edge arguments, not child edges: they neither become
// required nor force an inline.
func TestSchemaForIgnoresEdgeArgs(t *testing.T) {
	g := buildToyGraph()
	s, err := SchemaFor(stubReflector{}, g.Book, include.IncludeTree{
		":limit":  "5",
		"reviews": include.IncludeTree{":limit": "3"},
	}, include.Limits{})
	if err != nil {
		t.Fatalf("SchemaFor: %v", err)
	}
	got := s.Map()
	req := requiredKeys(t, got)
	if contains(req, ":limit") {
		t.Errorf("edge arg leaked into required: %v", req)
	}
	if !contains(req, "reviews") {
		t.Errorf("reviews not promoted to required: %v", req)
	}
	if hasProp(got, ":limit") {
		t.Errorf("edge arg leaked into properties: %v", got["properties"])
	}
	// The subtree holds only an arg → still a leaf, so the static shape applies.
	wantReviews := envelopeOf(ref("Review"))
	if got := prop(t, got, "reviews"); !reflect.DeepEqual(got, wantReviews) {
		t.Errorf("reviews fragment = %v, want the static leaf shape %v", got, wantReviews)
	}
}

func TestSchemaForRejectsUnknownAndNonIncludableEdges(t *testing.T) {
	g := buildToyGraph()
	for name, tree := range map[string]include.IncludeTree{
		"unknown":        {"ghost": nil},
		"non-includable": {"notes": nil},
	} {
		if _, err := SchemaFor(stubReflector{}, g.Book, tree, include.Limits{}); err == nil {
			t.Errorf("%s edge: expected an error, got nil", name)
		}
	}
}

// An empty tree recomputes to the plain scalar object: no edge keys.
func TestSchemaForEmptyTreeIsScalarBase(t *testing.T) {
	g := buildToyGraph()
	s, err := SchemaFor(stubReflector{}, g.Review, include.IncludeTree{}, include.Limits{})
	if err != nil {
		t.Fatalf("SchemaFor: %v", err)
	}
	got := s.Map()
	for _, key := range []string{"id", "body"} {
		if !hasProp(got, key) {
			t.Errorf("scalar %q missing from the recomputed base: %v", key, got["properties"])
		}
	}
	if hasProp(got, "reviews") || hasProp(got, "author") {
		t.Errorf("an empty tree must stitch no edges: %v", got["properties"])
	}
}

// DRIFT (recorded, Task 14): v0 pinned `required` to a non-nil empty slice so
// the key was always present. Over the IR the serializer OMITS an empty
// required entirely (Task 13 decision).
func TestSchemaForOmitsEmptyRequired(t *testing.T) {
	node := &emitRes{name: "AllOptional", wire: allOptionalWire{}, edges: map[string]include.Edge{}}
	s, err := SchemaFor(stubReflector{}, node, include.IncludeTree{}, include.Limits{})
	if err != nil {
		t.Fatalf("SchemaFor: %v", err)
	}
	got := s.Map()
	if _, ok := got["required"]; ok {
		t.Fatalf("empty required must be omitted, got %#v", got["required"])
	}
}

func TestSchemaForRejectsInArrayEdgeWithoutSubField(t *testing.T) {
	g := buildToyGraph()
	g.Book.edges = map[string]include.Edge{
		"images": {
			Target:     func() include.Resource { return g.Image },
			Many:       true,
			Includable: true,
			ArrayPath:  "gallery",
		},
	}

	_, err := SchemaFor(stubReflector{}, g.Book, include.IncludeTree{"images": nil}, include.Limits{})
	if err == nil {
		t.Fatal("SchemaFor: want an error for a blank SubField, got nil")
	}
	if want := `apidoc: node "Book" edge "images": in-array edge requires SubField`; err.Error() != want {
		t.Errorf("err = %q, want %q", err, want)
	}
}

// ---------------------------------------------------------------------------
// base-node fidelity
// ---------------------------------------------------------------------------

// annotatedReflector emits ONE object component carrying non-Props facts:
// AdditionalProperties, a Description and an x- extension. Building the
// recomputed node from scratch would silently drop all three, making the
// include-aware schema contradict the static component (and hiding an
// extension-borne $ref from Verify).
type annotatedReflector struct{ scalar bool }

func (a annotatedReflector) ReflectComponents(types []reflect.Type, overrides map[reflect.Type]string) (map[string]*IRNode, error) {
	name := overrides[types[0]]
	if a.scalar {
		return map[string]*IRNode{name: newScalar("string")}, nil
	}
	n := newObject([]Prop{{Name: "id", Schema: newScalar("string"), Required: true}})
	n.AdditionalProperties = false
	n.Description = "annotated base"
	n.Extensions = map[string]any{"x-audit": "pii"}
	return map[string]*IRNode{name: n}, nil
}

var _ Reflector = annotatedReflector{}

func TestSchemaForKeepsBaseNodeNonPropsFacts(t *testing.T) {
	g := buildToyGraph()
	s, err := SchemaFor(annotatedReflector{}, g.Book, include.IncludeTree{"author": nil}, include.Limits{})
	if err != nil {
		t.Fatalf("SchemaFor: %v", err)
	}
	got := s.Map()
	if ap, ok := got["additionalProperties"]; !ok || ap != false {
		t.Errorf("additionalProperties = %#v, want false", got["additionalProperties"])
	}
	if got["description"] != "annotated base" {
		t.Errorf("description = %#v, want %q", got["description"], "annotated base")
	}
	if got["x-audit"] != "pii" {
		t.Errorf("x-audit = %#v, want %q", got["x-audit"], "pii")
	}
	// ...and the recompute still did its job.
	if req := requiredKeys(t, got); !contains(req, "author") {
		t.Errorf("required = %v, want it to carry author", req)
	}
}

// ---------------------------------------------------------------------------
// error paths
// ---------------------------------------------------------------------------

// A reflector that renders the wire type as a non-object cannot have edge keys
// stitched into it.
func TestSchemaForRejectsNonObjectBase(t *testing.T) {
	g := buildToyGraph()
	_, err := SchemaFor(annotatedReflector{scalar: true}, g.Book, include.IncludeTree{}, include.Limits{})
	if err == nil {
		t.Fatal("a non-object base must error")
	}
	want := `apidoc: SchemaFor: node "Book" base schema is a scalar, want an object`
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err, want)
	}
}

// A Resource with no WireSample() has nothing to reflect.
func TestSchemaForRejectsNodeWithoutWireSample(t *testing.T) {
	if _, err := SchemaFor(stubReflector{}, &bareRes{name: "Bare"}, include.IncludeTree{}, include.Limits{}); err == nil {
		t.Fatal("a node without a WireSample must error")
	}
}

func TestSchemaForPropagatesReflectorError(t *testing.T) {
	g := buildToyGraph()
	if _, err := SchemaFor(stubReflector{err: errors.New("boom")}, g.Book, include.IncludeTree{}, include.Limits{}); err == nil {
		t.Fatal("a reflector error must propagate")
	}
}

// ---------------------------------------------------------------------------
// computed edges
// ---------------------------------------------------------------------------

// A COMPUTED edge carries a nil Target func, so the Computed branch must come
// BEFORE any Target-nil check: the v0 order rejected it as a wiring bug.
func TestSchemaForComputedRequired(t *testing.T) {
	g := buildToyGraph()
	g.Book.edges["stats"] = include.Edge{
		Computed:       true,
		Includable:     true,
		ComputedSchema: RawFragment(map[string]any{"type": "integer"}),
	}

	s, err := SchemaFor(stubReflector{}, g.Book, include.IncludeTree{"stats": nil}, include.Limits{})
	if err != nil {
		t.Fatalf("SchemaFor: %v", err)
	}
	got := s.Map()
	if req := requiredKeys(t, got); !contains(req, "stats") {
		t.Errorf("computed edge not promoted to required: %v", req)
	}
	want := map[string]any{"type": "integer"}
	if frag := prop(t, got, "stats"); !reflect.DeepEqual(frag, want) {
		t.Errorf("stats fragment = %v, want %v", frag, want)
	}
}

// A computed edge has no target Resource, so it can carry no sub-includes.
// SchemaFor must reject the tree rather than quietly dropping the children.
func TestSchemaForComputedRejectsSubtree(t *testing.T) {
	g := buildToyGraph()
	g.Book.edges["stats"] = include.Edge{
		Computed:       true,
		Includable:     true,
		ComputedSchema: RawFragment(map[string]any{"type": "integer"}),
	}

	_, err := SchemaFor(stubReflector{}, g.Book, include.IncludeTree{
		"stats": include.IncludeTree{"ghost": nil},
	}, include.Limits{})
	if err == nil {
		t.Fatal("a subtree under a computed edge must error")
	}
	want := `apidoc: SchemaFor: node "Book" computed edge "stats" takes no sub-includes, got [ghost]`
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err, want)
	}
	// A ":"-arg-only subtree is still a LEAF and stays legal.
	if _, err := SchemaFor(stubReflector{}, g.Book, include.IncludeTree{
		"stats": include.IncludeTree{":limit": "3"},
	}, include.Limits{}); err != nil {
		t.Errorf("an arg-only subtree under a computed edge must be accepted: %v", err)
	}
}

// A computed edge that declares no ComputedSchema is a wiring bug.
func TestSchemaForComputedWithoutSchemaErrors(t *testing.T) {
	g := buildToyGraph()
	g.Book.edges["stats"] = include.Edge{Computed: true, Includable: true}

	if _, err := SchemaFor(stubReflector{}, g.Book, include.IncludeTree{"stats": nil}, include.Limits{}); err == nil {
		t.Fatal("a computed edge without a ComputedSchema must error")
	}
}

// The stitched ComputedSchema IR is SHARED by pointer with the edge
// declaration; SchemaFor must not mutate it (substituteAux invariant).
func TestSchemaForDoesNotMutateComputedSchema(t *testing.T) {
	g := buildToyGraph()
	declared := RawFragment(map[string]any{"type": "integer"})
	g.Book.edges["stats"] = include.Edge{
		Computed: true, Includable: true, ComputedSchema: declared,
	}

	if _, err := SchemaFor(stubReflector{}, g.Book, include.IncludeTree{"stats": nil}, include.Limits{}); err != nil {
		t.Fatalf("SchemaFor: %v", err)
	}
	want := map[string]any{"type": "integer"}
	if got := declared.Map(); !reflect.DeepEqual(got, want) {
		t.Fatalf("declared ComputedSchema mutated: %v, want %v", got, want)
	}
}

// A repeated recompute of the same tree must produce byte-identical output:
// tree keys are walked in SORTED order, not Go map order.
func TestSchemaForDeterministicPropOrder(t *testing.T) {
	g := buildToyGraph()
	tree := include.IncludeTree{"similar": nil, "author": nil, "reviews": nil}
	var first string
	for i := 0; i < 8; i++ {
		s, err := SchemaFor(stubReflector{}, g.Book, tree, include.Limits{})
		if err != nil {
			t.Fatalf("SchemaFor: %v", err)
		}
		b, err := s.IR().MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = string(b)
			continue
		}
		if string(b) != first {
			t.Fatalf("recompute %d differs:\n%s\n%s", i, first, string(b))
		}
	}
}

// ---------------------------------------------------------------------------
// IncludePaths: computed keys
// ---------------------------------------------------------------------------

// A computed edge is an includable LEAF: it has no Target, so the walk must not
// skip it on the Target-nil test, and it contributes no deeper paths.
func TestIncludePathsComputedKey(t *testing.T) {
	leaf := &ipRes{name: "Leaf", edges: map[string]include.Edge{}}
	root := &ipRes{name: "Root"}
	root.edges = map[string]include.Edge{
		"child": {Target: func() include.Resource { return leaf }, Includable: true},
		"stats": {Computed: true, Includable: true},
		// A computed edge that is NOT includable stays invisible.
		"secret": {Computed: true},
	}

	got := IncludePaths(root, include.DefaultLimits)
	want := []string{"child", "stats"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("IncludePaths(computed) = %v, want %v", got, want)
	}
}

func TestSchemaForEnforcesLimits(t *testing.T) {
	g := buildToyGraph()
	// Book.similar is a self-edge in the toy graph, so it can chain to any depth.
	chain := func(depth int) include.IncludeTree {
		tree := include.IncludeTree{}
		cur := tree
		for i := 0; i < depth; i++ {
			next := include.IncludeTree{}
			cur["similar"] = next
			cur = next
		}
		return tree
	}
	if _, err := SchemaFor(stubReflector{}, g.Book, chain(4), include.Limits{MaxDepth: 4, MaxNodes: 50}); err != nil {
		t.Fatalf("depth 4 with MaxDepth 4: %v", err)
	}
	if _, err := SchemaFor(stubReflector{}, g.Book, chain(5), include.Limits{MaxDepth: 4, MaxNodes: 50}); err == nil || !strings.Contains(err.Error(), "INCLUDE_TOO_DEEP") {
		t.Fatalf("depth 5 with MaxDepth 4: want INCLUDE_TOO_DEEP, got %v", err)
	}
	// Zero-value Limits → DefaultLimits (MaxDepth 4).
	if _, err := SchemaFor(stubReflector{}, g.Book, chain(5), include.Limits{}); err == nil {
		t.Fatal("zero Limits did not apply DefaultLimits")
	}
	// Node budget: 3 client keys against MaxNodes 2; args do not count.
	wide := include.IncludeTree{"author": nil, "reviews": nil, "similar": nil, ":limit": "5"}
	if _, err := SchemaFor(stubReflector{}, g.Book, wide, include.Limits{MaxDepth: 4, MaxNodes: 2}); err == nil {
		t.Fatal("3 keys with MaxNodes 2: want error")
	}
	if _, err := SchemaFor(stubReflector{}, g.Book, wide, include.Limits{MaxDepth: 4, MaxNodes: 3}); err != nil {
		t.Fatalf("3 keys with MaxNodes 3 (args do not count): %v", err)
	}
}
