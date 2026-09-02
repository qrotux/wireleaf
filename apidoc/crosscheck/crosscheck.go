// Package crosscheck validates runtime JSON samples against assembled apidoc
// components with a real draft-2020-12 validator.
//
// It is a TEST-ONLY nested module: the core library never depends on a schema
// validator, so the dependency lives here, behind its own go.mod. The point is
// to answer the one question the doc layer cannot answer about itself — does a
// document a handler actually returns satisfy the component the doc claims?
//
// Assembly: every component is placed under "$defs" of one synthetic root, and
// each "#/components/schemas/X" pointer is rewritten to "#/$defs/X", so the
// document-shaped refs resolve inside a standalone schema resource.
//
// One semantic shift comes with that flattening: a component carrying its OWN
// local "$defs" and "#/$defs/..." pointers still resolves, but against the
// synthetic root — the component is not its own resource here, so its pointers
// are read as root-level ones. Component names therefore share one namespace
// with any component-local "$defs" key.
//
// "format" and "contentEncoding"/"contentMediaType" are annotations under
// draft 2020-12, and this validator turns both into ASSERTIONS
// (AssertFormat/AssertContent): a crosscheck exists to catch a sample the
// document promises something about, so a format row that checks nothing would
// be a false green.
package crosscheck

import (
	"bytes"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/qrotux/wireleaf/apidoc"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// rootURL is the synthetic base of the assembled resource. Nothing is fetched:
// the resource is handed to the compiler in memory and every ref is local.
const rootURL = "mem://root"

// defsPrefix is the in-resource replacement for apidoc.RefPrefix.
const defsPrefix = "#/$defs/"

// draft2020URL is the "$schema" the synthetic root declares.
const draft2020URL = "https://json-schema.org/draft/2020-12/schema"

// Validator is a compiled draft-2020-12 schema for one named component.
type Validator struct {
	name string
	sch  *jsonschema.Schema
}

// Compile assembles components into one draft-2020-12 resource and compiles the
// schema of the component named by name.
//
// Each component is serialized through the IR's own MarshalJSON (the same bytes
// the emitted document carries) and then decoded: key order and Opaque
// fidelity are irrelevant to validation semantics, so a decoded tree is the
// convenient representation to rewrite refs on. This is deliberately NOT the
// path document emission takes.
//
// A ref to an absent component is reported here, naming it, rather than
// surfacing later as an opaque resolution failure.
func Compile(components map[string]apidoc.Schema, name string) (*Validator, error) {
	if _, ok := components[name]; !ok {
		return nil, fmt.Errorf("crosscheck: no component named %q (have %s)",
			name, strings.Join(sortedKeys(components), ", "))
	}

	defs := make(map[string]any, len(components))
	for _, n := range sortedKeys(components) {
		doc, err := decodeComponent(components[n])
		if err != nil {
			return nil, fmt.Errorf("crosscheck: component %q: %w", n, err)
		}
		defs[n] = rewriteRefs(doc)
	}
	if err := checkRefs(defs); err != nil {
		return nil, err
	}

	root := map[string]any{
		"$schema": draft2020URL,
		"$id":     rootURL,
		"$defs":   defs,
		"$ref":    defsPrefix + name,
	}

	c := jsonschema.NewCompiler()
	// "$schema" on the root pins the draft; DefaultDraft is the belt for any
	// subresource that declares none.
	c.DefaultDraft(jsonschema.Draft2020)
	// Annotations the doc layer emits as promises are checked, not ignored.
	c.AssertFormat()
	c.AssertContent()
	if err := c.AddResource(rootURL, root); err != nil {
		return nil, fmt.Errorf("crosscheck: add resource: %w", err)
	}
	sch, err := c.Compile(rootURL)
	if err != nil {
		return nil, fmt.Errorf("crosscheck: compile %q: %w", name, err)
	}
	return &Validator{name: name, sch: sch}, nil
}

// Validate reports whether the sample satisfies the compiled component schema.
func (v *Validator) Validate(sample []byte) error {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(sample))
	if err != nil {
		return fmt.Errorf("crosscheck: sample is not valid JSON: %w", err)
	}
	if err := v.sch.Validate(doc); err != nil {
		return fmt.Errorf("crosscheck: sample does not satisfy %q: %w", v.name, err)
	}
	return nil
}

// decodeComponent renders one component through its IR MarshalJSON and decodes
// the bytes. jsonschema.UnmarshalJSON is used so numbers arrive in the form the
// validator's own loaders produce.
func decodeComponent(s apidoc.Schema) (any, error) {
	n := s.IR()
	if n == nil {
		return nil, fmt.Errorf("empty Schema")
	}
	b, err := n.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return doc, nil
}

// rewriteRefs walks the decoded tree and rewrites every document-shaped
// component pointer to its "$defs" equivalent. It rewrites any string carrying
// the prefix, not only "$ref" values: extensions ("x-links") hold pointers too,
// and a stale one there would be as wrong as a stale "$ref".
func rewriteRefs(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, e := range t {
			t[k] = rewriteRefs(e)
		}
		return t
	case []any:
		for i, e := range t {
			t[i] = rewriteRefs(e)
		}
		return t
	case string:
		if strings.HasPrefix(t, apidoc.RefPrefix) {
			return defsPrefix + strings.TrimPrefix(t, apidoc.RefPrefix)
		}
		return t
	default:
		return v
	}
}

// checkRefs reports a local "$defs" pointer with no matching component. The
// compiler would fail on its own, but with a message about a JSON pointer
// rather than about the component the caller forgot to pass.
func checkRefs(defs map[string]any) error {
	var missing []string
	seen := map[string]bool{}
	for _, owner := range sortedKeys(defs) {
		for _, target := range collectRefs(defs[owner]) {
			if _, ok := defs[target]; ok || seen[owner+"->"+target] {
				continue
			}
			seen[owner+"->"+target] = true
			missing = append(missing, fmt.Sprintf("%s -> %s", owner, target))
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("crosscheck: dangling component ref(s): %s (known: %s)",
			strings.Join(missing, ", "), strings.Join(sortedKeys(defs), ", "))
	}
	return nil
}

// collectRefs gathers the component names a rewritten subtree points at.
func collectRefs(v any) []string {
	var out []string
	switch t := v.(type) {
	case map[string]any:
		for _, k := range sortedKeys(t) {
			out = append(out, collectRefs(t[k])...)
		}
	case []any:
		for _, e := range t {
			out = append(out, collectRefs(e)...)
		}
	case string:
		if tail, ok := strings.CutPrefix(t, defsPrefix); ok && tail != "" {
			// A deep pointer ("#/$defs/Author/properties/id") still names its
			// component in the first segment.
			name, _, _ := strings.Cut(tail, "/")
			out = append(out, name)
		}
	}
	return out
}

// sortedKeys returns the keys of m in ascending order.
func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}
