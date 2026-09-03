package huma

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	humav2 "github.com/danielgtaylor/huma/v2"

	"github.com/qrotux/wireleaf/apidoc"
	"github.com/qrotux/wireleaf/reflector"
)

type nodeWire struct {
	V string `json:"v"`
}
type nodeWireOrphan struct{ V string }

type nodeEnvelope struct {
	Data Node[nodeWire] `json:"data"`
}

// registerWiredNode builds a Components with nodeWire bound, plus its bridge.
// The indexes live on the value, so every test gets a fresh one.
func registerWiredNode() (*Registry, *apidoc.Components) {
	c := apidoc.NewComponents()
	RegisterNode[nodeWire](c, "NodeWireComponent")
	return NewRegistry(c, &reflector.Reflector{}).(*Registry), c
}

func TestNodeSchemaRefAndPanics(t *testing.T) {
	b, c := registerWiredNode()
	s := Node[nodeWire]{}.Schema(b)
	if s.Ref != apidoc.RefPrefix+"NodeWireComponent" {
		t.Fatalf("ref = %q", s.Ref)
	}
	// An unregistered W is a wiring bug, not an invitation to auto-register.
	assertPanics(t, func() { Node[nodeWireOrphan]{}.Schema(b) })
	// So is a registry that is not the wireleaf bridge.
	plain := humav2.NewMapRegistry(apidoc.RefPrefix, humav2.DefaultSchemaNamer)
	assertPanics(t, func() { Node[nodeWire]{}.Schema(plain) })
	// Re-binding the same wire type to another component is a conflict.
	assertPanics(t, func() { RegisterNode[nodeWire](c, "Again") })
}

// RegisterNode fills every lookup: the wire type (TypeName), the core wrapper
// (an envelope-derivation pass off a struct field) and this package's wrapper.
func TestRegisterNodeFillsEveryLookup(t *testing.T) {
	_, c := registerWiredNode()
	if name, ok := c.TypeName(reflect.TypeFor[nodeWire]()); !ok || name != "NodeWireComponent" {
		t.Fatalf("wire lookup = %q, %v", name, ok)
	}
	if name, ok := c.NodeComponent(reflect.TypeFor[Node[nodeWire]]()); !ok || name != "NodeWireComponent" {
		t.Fatalf("wrapper lookup = %q, %v", name, ok)
	}
	if name, ok := c.NodeComponent(reflect.TypeFor[apidoc.Node[nodeWire]]()); !ok || name != "NodeWireComponent" {
		t.Fatalf("embedded wrapper lookup = %q, %v", name, ok)
	}
}

// The bridge resolves a node wrapper through the wrapper index, never by
// reflecting the wrapper struct into a component of its own.
func TestBridgeResolvesNodeWrapper(t *testing.T) {
	b, _ := registerWiredNode()
	s := b.Schema(reflect.TypeFor[Node[nodeWire]](), true, "data")
	if s.Ref != apidoc.RefPrefix+"NodeWireComponent" {
		t.Fatalf("ref = %q", s.Ref)
	}
	if _, reflected := b.Map()["Node"]; reflected {
		t.Fatalf("the wrapper type must not become a component of its own")
	}
}

// The embedded apidoc.Node keeps its verbatim-bytes marshalling through the
// huma wrapper.
func TestNodeVerbatimMarshalNoEscape(t *testing.T) {
	raw := json.RawMessage(`{"html":"a&<>b"}`)
	env := nodeEnvelope{Data: NodeOf[nodeWire](raw)}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(env); err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSuffix(buf.String(), "\n")
	want := `{"data":{"html":"a&<>b"}}`
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestNodeNilMarshalsNull(t *testing.T) {
	b, err := json.Marshal(nodeEnvelope{})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"data":null}` {
		t.Fatalf("got %s", b)
	}
}

func TestNodeUnmarshalKeepsBytes(t *testing.T) {
	var env nodeEnvelope
	if err := json.Unmarshal([]byte(`{"data":{"v":"x"}}`), &env); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"data":{"v":"x"}}` {
		t.Fatalf("round trip = %s", out)
	}
}
