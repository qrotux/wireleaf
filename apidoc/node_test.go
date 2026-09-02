package apidoc

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

type nodeWire struct{ V string }
type nodeWireDup struct{ V string }
type nodeWireOrphan struct{ V string }

type nodeEnvelope struct {
	Data Node[nodeWire] `json:"data"`
}

func TestNodeVerbatimMarshalNoEscape(t *testing.T) {
	raw := json.RawMessage(`{"html":"a&<>b"}`)
	env := nodeEnvelope{Data: NodeOf[nodeWire](raw)}
	// Runtime semantics of a serving pipeline: Encoder + SetEscapeHTML(false).
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

func TestNodeUnmarshalRoundTrip(t *testing.T) {
	var env nodeEnvelope
	if err := json.Unmarshal([]byte(`{"data":{"v":1}}`), &env); err != nil {
		t.Fatal(err)
	}
	b, _ := env.Data.MarshalJSON()
	if string(b) != `{"v":1}` {
		t.Fatalf("got %s", b)
	}
}

// Two wire types may share ONE component name (aliasing); the registry is keyed
// by type, so the two bindings coexist.
func TestRegisterNodeAllowsNameAliasing(t *testing.T) {
	c := NewComponents()
	RegisterNode[nodeWire](c, "SharedComponent")
	RegisterNode[nodeWireDup](c, "SharedComponent")
	for _, tt := range []reflect.Type{reflect.TypeFor[Node[nodeWire]](), reflect.TypeFor[Node[nodeWireDup]]()} {
		if name, ok := c.NodeComponent(tt); !ok || name != "SharedComponent" {
			t.Fatalf("%v: got (%q, %v)", tt, name, ok)
		}
	}
	if _, ok := c.TypeName(reflect.TypeFor[nodeWireOrphan]()); ok {
		t.Fatal("unregistered W must miss")
	}
}
