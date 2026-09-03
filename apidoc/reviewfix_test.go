package apidoc

// reviewfix_test.go — pins for the 2026-09-01 review fixes: SchemaFor mirrors
// the planner's defaults-merge; emission reaches and stitches default edges;
// EmitComponent fails loudly on surviving auxiliaries.

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/qrotux/wireleaf/include"
)

// mirrorGraph builds:
//
//	Book    author → Author (to-one, Required, includable, DEFAULT)
//	Author  avatar → Image  (to-one, NOT includable, DEFAULT)
//	        books  → Book   (reverse, includable, DEFAULT — cut by the chain)
//	Image   leaf
func mirrorGraph() (book, author, image *emitRes) {
	image = &emitRes{name: "Image", wire: imageWire{}, edges: map[string]include.Edge{}}
	author = &emitRes{name: "Author", wire: authorWire{}, defaults: []string{"avatar", "books"}}
	book = &emitRes{name: "Book", wire: bookWire{}, defaults: []string{"author"}}
	author.edges = map[string]include.Edge{
		"avatar": {Target: func() include.Resource { return image }}, // default, NOT includable
		"books": {Target: func() include.Resource { return book }, Many: true, Includable: true,
			Backref: "authorId"},
	}
	book.edges = map[string]include.Edge{
		"author": {Target: func() include.Resource { return author }, Includable: true, Required: true},
	}
	return book, author, image
}

func irProp(t *testing.T, n *IRNode, name string) Prop {
	t.Helper()
	for _, p := range n.Props {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("no property %q (have %s)", name, propListNames(n))
	return Prop{}
}

func hasIRProp(n *IRNode, name string) bool {
	for _, p := range n.Props {
		if p.Name == name {
			return true
		}
	}
	return false
}

func propListNames(n *IRNode) string {
	names := make([]string, 0, len(n.Props))
	for _, p := range n.Props {
		names = append(names, p.Name)
	}
	return strings.Join(names, ",")
}

// SchemaFor for an EMPTY tree mirrors the planner: Defaults() expand at every
// level, every effective key is required, the default-only chain cuts cycles.
func TestSchemaForMirrorsDefaults(t *testing.T) {
	book, _, _ := mirrorGraph()
	s, err := SchemaFor(stubReflector{}, book, include.IncludeTree{}, include.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	n := s.IR()

	// author: default → present AND required; Required() to-one → bare $ref…
	a := irProp(t, n, "author")
	if !a.Required {
		t.Errorf("default edge author must be required")
	}
	// …but Author has a LIVE default (avatar), so the target is INLINED.
	if a.Schema.Kind != KindObject {
		t.Fatalf("author value = %s, want the inlined Author object (live default avatar)", a.Schema.Kind)
	}
	av := irProp(t, a.Schema, "avatar")
	if !av.Required {
		t.Errorf("nested default avatar must be required")
	}
	// avatar's target Image has no defaults → leaf $ref shape (nullable to-one).
	if av.Schema.Kind != KindCombinator {
		t.Errorf("avatar value = %s, want anyOf[$ref Image, null]", av.Schema.Kind)
	}
	// books: default whose target (Book) is on the chain → cut, absent.
	if hasIRProp(a.Schema, "books") {
		t.Errorf("author.books must be cut by the default chain (re-enters Book)")
	}
}

// A client key on top of the defaults keeps the defaults AND stays ordered
// after them; a stale default is skipped silently.
func TestSchemaForDefaultsPlusClientAndStale(t *testing.T) {
	book, author, _ := mirrorGraph()
	book.defaults = []string{"ghost", "author"} // ghost: no such edge
	book.edges["reviews"] = include.Edge{
		Target: func() include.Resource {
			return &emitRes{name: "Review", wire: reviewWire{}, edges: map[string]include.Edge{}}
		},
		Many: true, Includable: true, Backref: "bookId",
	}
	_ = author

	s, err := SchemaFor(stubReflector{}, book, include.IncludeTree{"reviews": include.IncludeTree{}}, include.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	n := s.IR()
	if hasIRProp(n, "ghost") {
		t.Errorf("stale default must be skipped")
	}
	if !irProp(t, n, "reviews").Required {
		t.Errorf("client key reviews must be required")
	}
	// Plan order: wire fields, then defaults, then client-only keys.
	if got := propListNames(n); got != "id,title,author,reviews" {
		t.Errorf("prop order = %s, want id,title,author,reviews", got)
	}
}

// A NON-includable default is part of the response and thus of the schema —
// but a CLIENT asking for it by name is still rejected.
func TestSchemaForNonIncludableDefault(t *testing.T) {
	_, author, _ := mirrorGraph()
	s, err := SchemaFor(stubReflector{}, author, include.IncludeTree{}, include.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if !irProp(t, s.IR(), "avatar").Required {
		t.Errorf("non-includable default avatar must be present and required")
	}

	if _, err := SchemaFor(stubReflector{}, author, include.IncludeTree{"avatar": include.IncludeTree{}}, include.Limits{}); err == nil {
		t.Errorf("client include of a non-includable edge must error")
	}
}

// EmitComponents reaches a non-includable DEFAULT edge's target (its component
// is emitted, so the stitched $ref resolves) and stitches the default key as an
// OPTIONAL property (exclude can prune it).
func TestEmitComponentsReachesDefaultTargets(t *testing.T) {
	_, author, _ := mirrorGraph()
	comps, err := EmitComponents(stubReflector{}, []include.Resource{author})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := comps["Image"]; !ok {
		t.Fatalf("Image (target of a non-includable default) must be emitted; have %v", slices.Sorted(maps.Keys(comps)))
	}
	av := irProp(t, comps["Author"].IR(), "avatar")
	if av.Required {
		t.Errorf("static component marks default avatar optional (exclude can prune it)")
	}
}

// EmitComponent (singular) fails loudly when an auxiliary survives inlining
// instead of returning a schema with a dangling $ref.
func TestEmitComponentRejectsSurvivingAux(t *testing.T) {
	type soleRefAux struct {
		V string `json:"v"`
	}
	type soleRefWire struct {
		ID  string     `json:"id"`
		Aux soleRefAux `json:"aux" doc:"described"` // sibling keyword → sole-ref rule blocks inlining
	}
	node := &emitRes{name: "SoleRef", wire: soleRefWire{}, edges: map[string]include.Edge{}}
	_, _, err := EmitComponent(stubReflector{}, node)
	if err == nil || !strings.Contains(err.Error(), "use EmitComponents") {
		t.Fatalf("want a surviving-aux error pointing at EmitComponents, got %v", err)
	}
}

// A Required to-one edge is a bare $ref in `required` under EVERY
// RequiredMissing policy: the policy governs the engine, not the published
// contract. The doc layer must not widen to anyOf[$ref,null] to accommodate a
// tolerant runtime — that would hide a server-side fault behind the schema.
func TestRequiredEdgeShapeIgnoresMissingPolicy(t *testing.T) {
	strict, lenient := include.MissingRequiredError, include.MissingRequiredNull
	for _, tc := range []struct {
		name   string
		policy *include.MissingRequiredPolicy
	}{
		{"inherit (no declaration)", nil},
		{"MissingRequiredError", &strict},
		{"MissingRequiredNull", &lenient},
	} {
		t.Run(tc.name, func(t *testing.T) {
			book, _, _ := mirrorGraph()
			e := book.edges["author"]
			e.MissingRequired = tc.policy
			book.edges["author"] = e

			comps, err := EmitComponents(stubReflector{}, []include.Resource{book})
			if err != nil {
				t.Fatal(err)
			}
			p := irProp(t, comps["Book"].IR(), "author")
			if !p.Required {
				t.Errorf("a Required edge must be listed in `required`")
			}
			if p.Schema.Kind != KindRef {
				t.Errorf("value kind = %s, want a bare $ref (KindRef)", p.Schema.Kind)
			}
		})
	}
}
