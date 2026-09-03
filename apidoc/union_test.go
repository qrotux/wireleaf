package apidoc

import (
	"errors"
	"maps"
	"reflect"
	"slices"
	"testing"
)

type unionCreated struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type unionAccepted struct {
	RequestID string `json:"requestId"`
}

type unionTestWidget struct{ V string }

// unionPinned pins its whole arm to a named component instead of reflecting.
type unionPinned struct {
	Ignored string `json:"ignored"`
}

func (unionPinned) BodySchema() Schema { return RefTo("PinnedComponent") }

func TestUnionOfRoundTrip(t *testing.T) {
	u := UnionOf("status",
		Variant[unionCreated]{Status: 201},
		Variant[unionAccepted]{Status: 202},
	)
	if u.Discriminator != "status" {
		t.Fatalf("discriminator = %q", u.Discriminator)
	}
	want := []struct {
		typ    reflect.Type
		status int
	}{
		{reflect.TypeFor[unionCreated](), 201},
		{reflect.TypeFor[unionAccepted](), 202},
	}
	if len(u.Variants) != len(want) {
		t.Fatalf("variants = %d, want %d", len(u.Variants), len(want))
	}
	for i, w := range want {
		if got := u.Variants[i].VariantType(); got != w.typ {
			t.Fatalf("variant %d type = %v, want %v", i, got, w.typ)
		}
		if got := u.Variants[i].VariantStatus(); got != w.status {
			t.Fatalf("variant %d status = %d, want %d", i, got, w.status)
		}
	}
}

func TestUnionSchemaOneOfInVariantOrder(t *testing.T) {
	u := UnionOf("status",
		Variant[unionCreated]{Status: 201},
		Variant[unionAccepted]{Status: 202},
	)
	s, err := u.Schema(stubReflector{})
	if err != nil {
		t.Fatal(err)
	}
	m := s.Map()
	if len(m) != 1 {
		// The discriminator must NOT leak into the fragment.
		t.Fatalf("fragment must carry only oneOf, got %v", m)
	}
	oneOf, ok := m["oneOf"].([]any)
	if !ok || len(oneOf) != 2 {
		t.Fatalf("oneOf must be a 2-element list, got %v", m["oneOf"])
	}
	wantProps := [][]string{
		{"id", "status"},
		{"requestId"},
	}
	for i, keys := range wantProps {
		frag, ok := oneOf[i].(map[string]any)
		if !ok {
			t.Fatalf("oneOf[%d] is not a fragment: %v", i, oneOf[i])
		}
		props, ok := frag["properties"].(map[string]any)
		if !ok {
			t.Fatalf("oneOf[%d] has no properties: %v", i, frag)
		}
		if len(props) != len(keys) {
			t.Fatalf("oneOf[%d] properties = %v, want keys %v", i, slices.Sorted(maps.Keys(props)), keys)
		}
		for _, k := range keys {
			if _, ok := props[k]; !ok {
				t.Fatalf("oneOf[%d] missing property %q (have %v)", i, k, slices.Sorted(maps.Keys(props)))
			}
		}
	}
}

func TestUnionSchemaHonorsBodySchemaProvider(t *testing.T) {
	u := UnionOf("status",
		Variant[unionPinned]{Status: 201},
		Variant[unionAccepted]{Status: 202},
	)
	s, err := u.Schema(stubReflector{})
	if err != nil {
		t.Fatal(err)
	}
	arms := s.Map()["oneOf"].([]any)
	want := map[string]any{"$ref": RefPrefix + "PinnedComponent"}
	if !reflect.DeepEqual(arms[0], want) {
		t.Fatalf("pinned arm = %v, want %v", arms[0], want)
	}
}

func TestUnionSchemaPropagatesReflectorError(t *testing.T) {
	u := UnionOf("status",
		Variant[unionCreated]{Status: 201},
		Variant[unionAccepted]{Status: 202},
	)
	if _, err := u.Schema(stubReflector{err: errors.New("boom")}); err == nil {
		t.Fatalf("reflector error must propagate")
	}
}

// A non-struct arm is a wiring bug: nothing to reflect into an object fragment.
func TestUnionSchemaRejectsNonStructVariant(t *testing.T) {
	u := UnionOf("status",
		Variant[int]{Status: 201},
		Variant[unionAccepted]{Status: 202},
	)
	if _, err := u.Schema(stubReflector{}); err == nil {
		t.Fatalf("non-struct variant must error")
	}
}

func TestUnionOfPanicsOnSingleVariant(t *testing.T) {
	assertPanics(t, func() { UnionOf("status", Variant[unionCreated]{Status: 201}) })
}

func TestNodeComponentHitAndMiss(t *testing.T) {
	c := NewComponents()
	RegisterNode[unionTestWidget](c, "UnionTestWidget")
	name, ok := c.NodeComponent(reflect.TypeFor[Node[unionTestWidget]]())
	if !ok || name != "UnionTestWidget" {
		t.Fatalf("hit: got (%q, %v)", name, ok)
	}
	// The key is Node[W], NOT W itself — the bare wire type must miss.
	if name, ok := c.NodeComponent(reflect.TypeFor[unionTestWidget]()); ok {
		t.Fatalf("bare wire type must miss, got %q", name)
	}
	if name, ok := c.NodeComponent(reflect.TypeFor[Node[unionCreated]]()); ok {
		t.Fatalf("unregistered Node type must miss, got %q", name)
	}
}
