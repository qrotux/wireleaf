package apidoc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
)

// marshalValue encodes a leaf value without HTML escaping, so that the bytes
// this package produces stay faithful to the input (Opaque fragments in
// particular). Note that feeding an *IRNode to json.Marshal re-escapes <, >
// and & in the result -- callers that need byte-exact output must use
// MarshalJSON directly or a json.Encoder with SetEscapeHTML(false).
func marshalValue(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// sortedKeys returns the keys of m in ascending order.
func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}

// irWriter accumulates a JSON object with an explicit, order-preserving key
// sequence. Keys are known-safe literals; values go through json.Marshal.
type irWriter struct {
	buf   bytes.Buffer
	n     int
	err   error
	begun bool
}

func (w *irWriter) begin() {
	if !w.begun {
		w.buf.WriteByte('{')
		w.begun = true
	}
}

func (w *irWriter) key(k string) {
	w.begin()
	if w.n > 0 {
		w.buf.WriteByte(',')
	}
	w.n++
	kb, err := marshalValue(k)
	if err != nil {
		w.err = fmt.Errorf("apidoc: marshal key %q: %w", k, err)
		return
	}
	w.buf.Write(kb)
	w.buf.WriteByte(':')
}

// raw writes a key with pre-encoded JSON bytes.
func (w *irWriter) raw(k string, v []byte) {
	if w.err != nil {
		return
	}
	w.key(k)
	w.buf.Write(v)
}

// set writes a key whose value is produced by json.Marshal.
func (w *irWriter) set(k string, v any) {
	if w.err != nil {
		return
	}
	b, err := marshalValue(v)
	if err != nil {
		w.err = fmt.Errorf("apidoc: marshal %q: %w", k, err)
		return
	}
	w.raw(k, b)
}

func (w *irWriter) done() ([]byte, error) {
	if w.err != nil {
		return nil, w.err
	}
	w.begin()
	w.buf.WriteByte('}')
	return w.buf.Bytes(), nil
}

// writeNodes writes a JSON array of child nodes under key k.
func (w *irWriter) writeNodes(k string, nodes []*IRNode) {
	if w.err != nil || len(nodes) == 0 {
		return
	}
	w.key(k)
	w.buf.WriteByte('[')
	for i, c := range nodes {
		if i > 0 {
			w.buf.WriteByte(',')
		}
		b, err := c.MarshalJSON()
		if err != nil {
			w.err = err
			return
		}
		w.buf.Write(b)
	}
	w.buf.WriteByte(']')
}

// writeNode writes one child node under key k.
func (w *irWriter) writeNode(k string, n *IRNode) {
	if w.err != nil || n == nil {
		return
	}
	b, err := n.MarshalJSON()
	if err != nil {
		w.err = err
		return
	}
	w.raw(k, b)
}

// MarshalJSON renders the node as an OAS-3.1 JSON Schema, preserving property
// order and emitting keywords in a fixed order. Opaque nodes are emitted
// byte-exact.
func (n *IRNode) MarshalJSON() ([]byte, error) {
	if n == nil {
		return []byte("null"), nil
	}
	if n.Kind == KindOpaque {
		return append([]byte(nil), n.Opaque...), nil
	}

	w := &irWriter{}

	// $ref
	if n.Ref != "" {
		w.set("$ref", RefPrefix+n.Ref)
	}

	// type -- bare string when single, array otherwise
	switch len(n.Types) {
	case 0:
	case 1:
		w.set("type", n.Types[0])
	default:
		w.set("type", n.Types)
	}

	if len(n.Enum) > 0 {
		w.set("enum", n.Enum)
	}
	if n.Const != nil {
		w.set("const", n.Const)
	}

	// validations, in struct field order
	if n.Minimum != nil {
		w.set("minimum", *n.Minimum)
	}
	if n.Maximum != nil {
		w.set("maximum", *n.Maximum)
	}
	if n.ExclusiveMinimum != nil {
		w.set("exclusiveMinimum", *n.ExclusiveMinimum)
	}
	if n.ExclusiveMaximum != nil {
		w.set("exclusiveMaximum", *n.ExclusiveMaximum)
	}
	if n.MultipleOf != nil {
		w.set("multipleOf", *n.MultipleOf)
	}
	if n.MinLength != nil {
		w.set("minLength", *n.MinLength)
	}
	if n.MaxLength != nil {
		w.set("maxLength", *n.MaxLength)
	}
	if n.MinItems != nil {
		w.set("minItems", *n.MinItems)
	}
	if n.MaxItems != nil {
		w.set("maxItems", *n.MaxItems)
	}
	if n.MinProperties != nil {
		w.set("minProperties", *n.MinProperties)
	}
	if n.MaxProperties != nil {
		w.set("maxProperties", *n.MaxProperties)
	}
	if n.UniqueItems {
		w.set("uniqueItems", true)
	}
	if n.Pattern != "" {
		w.set("pattern", n.Pattern)
	}
	if n.Format != "" {
		w.set("format", n.Format)
	}
	if n.ContentEncoding != "" {
		w.set("contentEncoding", n.ContentEncoding)
	}
	if n.ContentMediaType != "" {
		w.set("contentMediaType", n.ContentMediaType)
	}
	if len(n.DependentRequired) > 0 {
		w.key("dependentRequired")
		w.buf.WriteByte('{')
		for i, k := range sortedKeys(n.DependentRequired) {
			if i > 0 {
				w.buf.WriteByte(',')
			}
			kb, err := marshalValue(k)
			if err != nil {
				return nil, err
			}
			w.buf.Write(kb)
			w.buf.WriteByte(':')
			vb, err := marshalValue(n.DependentRequired[k])
			if err != nil {
				return nil, err
			}
			w.buf.Write(vb)
		}
		w.buf.WriteByte('}')
	}

	// structure
	// An object with ZERO properties omits "properties" entirely (and, as ever,
	// an empty "required"). Decided at Task 13 for the golden documents:
	// {"type":"object"} must round-trip to itself, and `"properties":{}` on
	// every annotation-only or additionalProperties-only object is noise that
	// diffs badly. An empty object is still an object.
	if n.Kind == KindObject && len(n.Props) > 0 {
		w.key("properties")
		w.buf.WriteByte('{')
		for i, p := range n.Props {
			if i > 0 {
				w.buf.WriteByte(',')
			}
			kb, err := marshalValue(p.Name)
			if err != nil {
				return nil, err
			}
			w.buf.Write(kb)
			w.buf.WriteByte(':')
			pb, err := p.Schema.MarshalJSON()
			if err != nil {
				return nil, err
			}
			w.buf.Write(pb)
		}
		w.buf.WriteByte('}')

		var required []string
		for _, p := range n.Props {
			if p.Required {
				required = append(required, p.Name)
			}
		}
		if len(required) > 0 {
			w.set("required", required)
		}
	}

	w.writeNode("items", n.Items)
	w.writeNodes("anyOf", n.AnyOf)
	w.writeNodes("oneOf", n.OneOf)
	w.writeNodes("allOf", n.AllOf)
	w.writeNode("not", n.Not)

	switch ap := n.AdditionalProperties.(type) {
	case nil:
	case bool:
		w.set("additionalProperties", ap)
	case *IRNode:
		w.writeNode("additionalProperties", ap)
	default:
		return nil, fmt.Errorf("apidoc: AdditionalProperties must be nil, bool or *IRNode, got %T", ap)
	}

	// annotations
	if n.Description != "" {
		w.set("description", n.Description)
	}
	if n.Title != "" {
		w.set("title", n.Title)
	}
	if n.Default != nil {
		w.set("default", n.Default)
	}
	if len(n.Examples) > 0 {
		w.set("examples", n.Examples)
	}
	if n.ReadOnly {
		w.set("readOnly", true)
	}
	if n.WriteOnly {
		w.set("writeOnly", true)
	}
	if n.Deprecated {
		w.set("deprecated", true)
	}

	// extensions, sorted by key
	for _, k := range sortedKeys(n.Extensions) {
		w.set(k, n.Extensions[k])
	}

	return w.done()
}

// mapAny renders the node as a generic map, via a marshal/unmarshal round trip.
// Key order is lost by construction; use MarshalJSON when order matters.
func (n *IRNode) mapAny() map[string]any {
	if n == nil {
		return nil
	}
	b, err := n.MarshalJSON()
	if err != nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	return m
}
