package apidoc

// build.go — Components: the passed-around component registry.
//
// There is no package-global registry. A Components value is created, filled
// and handed to whoever assembles the document; two independent documents in
// one process are two independent Components.
//
// Everything is stored as typed IR. Assembly NEVER routes a component through
// map[string]any: that would lose property order and an Opaque fragment's exact
// bytes. Merge is the one map-shaped seam, and it inserts pre-encoded JSON.

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
)

// Components is a registry of named doc component fragments plus the two type
// indexes the doc layer needs: wire type → component name, and node WRAPPER
// type → component name. The zero value is ready to use. Not safe for
// concurrent mutation (registration-time only).
type Components struct {
	entries map[string]*IRNode
	byType  map[reflect.Type]string
	// byName is the reverse of byType, maintained by the same writers so the
	// lookup TypeOf performs is O(1) and DETERMINISTIC. Two wire types may name
	// the same component; the FIRST binding wins, because iterating byType
	// instead would answer differently from call to call.
	byName       map[string]reflect.Type
	external     map[string]bool
	nodeWrappers map[reflect.Type]string
}

// bindType records t -> name in both directions. Callers have already decided
// the binding is legal.
func (c *Components) bindType(t reflect.Type, name string) {
	if c.byType == nil {
		c.byType = map[reflect.Type]string{}
	}
	c.byType[t] = name
	if c.byName == nil {
		c.byName = map[string]reflect.Type{}
	}
	if _, taken := c.byName[name]; !taken {
		c.byName[name] = t
	}
}

// NewComponents returns an empty registry. The zero value is equally usable;
// this exists so call sites read as construction.
func NewComponents() *Components { return &Components{} }

// ---------------------------------------------------------------------------
// registration
// ---------------------------------------------------------------------------

// Add registers a named component fragment. Duplicate names PANIC — two writers
// for one fact is a wiring bug, and a half-built document is worse than a loud
// stop at declaration time. An IR that violates the Kind<->field invariants
// panics for the same reason.
//
// The idempotent sibling used by the reflector bridge is RegisterReflected: an
// identical re-registration there is a no-op, not a duplicate.
func (c *Components) Add(name string, s Schema) {
	if err := c.add(name, s.n); err != nil {
		panic(err.Error())
	}
}

func (c *Components) add(name string, n *IRNode) error {
	if name == "" {
		return fmt.Errorf("apidoc: component name is empty")
	}
	if err := validateComponent(name, n); err != nil {
		return err
	}
	if _, dup := c.entries[name]; dup {
		return fmt.Errorf("apidoc: component %q registered twice", name)
	}
	if c.entries == nil {
		c.entries = map[string]*IRNode{}
	}
	c.entries[name] = n
	return nil
}

// RegisterReflected is the bridge-facing registration: reflector output and the
// adapter bridge re-request the same component many times over a document's
// assembly, so an IDENTICAL re-registration is a no-op and a CONFLICTING one is
// an error. Equality is reflect.DeepEqual over the IR.
//
// It is exported because the huma adapter lives in a SEPARATE module and is the
// registry bridge: without it the bridge would have to keep a private shadow of
// everything it registered and could not tell an identical re-registration from
// another writer's component.
//
// t may be nil (an auxiliary component with no owning wire type).
func (c *Components) RegisterReflected(name string, n *IRNode, t reflect.Type) error {
	if name == "" {
		return fmt.Errorf("apidoc: component name is empty")
	}
	if err := validateComponent(name, n); err != nil {
		return err
	}
	if prev, ok := c.entries[name]; ok {
		if !reflect.DeepEqual(prev, n) {
			return fmt.Errorf("apidoc: component %q re-registered with a different schema", name)
		}
	} else {
		if c.entries == nil {
			c.entries = map[string]*IRNode{}
		}
		c.entries[name] = n
	}
	if t != nil {
		if prev, ok := c.byType[t]; ok && prev != name {
			return fmt.Errorf("apidoc: type %s already maps to component %q, cannot remap to %q", t, prev, name)
		}
		c.bindType(t, name)
	}
	return nil
}

// RegisterType binds a wire type to a component name. Re-binding the SAME name
// is a no-op; binding a different name panics (one writer per fact).
func (c *Components) RegisterType(t reflect.Type, name string) {
	if t == nil {
		panic("apidoc: RegisterType: nil type")
	}
	if prev, ok := c.byType[t]; ok {
		if prev == name {
			return
		}
		panic(fmt.Sprintf("apidoc: type %s already registered as %q, cannot remap to %q", t, prev, name))
	}
	c.bindType(t, name)
}

// Get returns the schema registered under name. It is the READ side of the
// registry: an assembler outside this package (the huma adapter's registry
// bridge, in another module) has to serve a registered component in its own
// document form, and re-parsing the serialized fragment would lose the typed IR
// that Merge deliberately preserves.
//
// The returned Schema shares the stored node: Schema's delta methods MUTATE, so
// treat it as read-only unless you own the component.
func (c *Components) Get(name string) (Schema, bool) {
	n, ok := c.entries[name]
	if !ok {
		return Schema{}, false
	}
	return Schema{n: n}, true
}

// TypeName returns the component name a wire type was registered under.
func (c *Components) TypeName(t reflect.Type) (string, bool) {
	name, ok := c.byType[t]
	return name, ok
}

// TypeOf is the reverse of TypeName: the WIRE type a component was registered
// for, if any. A component assembled by hand (RawFragment with no RegisterType)
// has none, and neither does an auxiliary the reflector named on its own.
//
// The node WRAPPER index does NOT participate: a wrapper (Node[W]) is not the
// component's type, W is.
//
// It exists for the huma adapter's registry bridge, which must answer huma's
// TypeFromRef — huma builds a Go type from it when it serves a response body,
// and cannot be handed a component whose type is unknown.
func (c *Components) TypeOf(name string) (reflect.Type, bool) {
	t, ok := c.byName[name]
	return t, ok
}

// ExternalRefs whitelists component names this document REFERENCES but does not
// OWN (platform-provided schemas merged in elsewhere). Verify treats them as
// resolvable. Repeats are harmless.
func (c *Components) ExternalRefs(names ...string) {
	if c.external == nil {
		c.external = map[string]bool{}
	}
	for _, n := range names {
		c.external[n] = true
	}
}

// ---------------------------------------------------------------------------
// node wrapper index
// ---------------------------------------------------------------------------

// RegisterNode binds wire type W to a component name on c: both W itself (for
// TypeName) and the WRAPPER type Node[W] (for NodeComponent, the reverse lookup
// an envelope-derivation pass needs when it walks a struct's field types and
// sees a Node field without knowing W).
func RegisterNode[W any](c *Components, component string) {
	c.RegisterType(reflect.TypeFor[W](), component)
	c.RegisterWrapperType(reflect.TypeFor[Node[W]](), component)
}

// RegisterWrapperType binds a node WRAPPER type (Node[W], or an adapter's own
// wrapper) to a component name for reverse lookup. Re-binding the same name is
// a no-op; a conflicting bind panics.
func (c *Components) RegisterWrapperType(t reflect.Type, component string) {
	if t == nil {
		panic("apidoc: RegisterWrapperType: nil type")
	}
	if prev, ok := c.nodeWrappers[t]; ok {
		if prev == component {
			return
		}
		panic(fmt.Sprintf("apidoc: node type %s already registered as %q, cannot remap to %q", t, prev, component))
	}
	if c.nodeWrappers == nil {
		c.nodeWrappers = map[reflect.Type]string{}
	}
	c.nodeWrappers[t] = component
}

// NodeComponent is the reverse lookup for envelope doc-derivation: t is the
// reflect type of a node WRAPPER (Node[W], or an adapter's own), NOT W, as seen
// on an envelope struct field; it returns the component name W was registered
// under.
func (c *Components) NodeComponent(t reflect.Type) (string, bool) {
	name, ok := c.nodeWrappers[t]
	return name, ok
}

// ---------------------------------------------------------------------------
// validation
// ---------------------------------------------------------------------------

// ValidateComponent reports whether n is a well-formed component schema: the
// same RECURSIVE invariant walk Components.Add applies at registration time,
// exported so a Reflector implementation (and the reflectortest contract suite)
// can check its own output BEFORE handing it to assembly, where the same
// violation would surface as a registration error far from its cause.
func ValidateComponent(name string, n *IRNode) error {
	return validateComponent(name, n)
}

// validateComponent runs a RECURSIVE invariant walk over a component's IR.
// checkInvariants is deliberately SHALLOW and the constructors do not call it,
// so components assembly is the gate that sees the whole tree — including the
// AdditionalProperties type check (nil | bool | *IRNode), which the serializer
// would otherwise only discover mid-encode.
func validateComponent(name string, n *IRNode) error {
	if n == nil {
		return fmt.Errorf("apidoc: component %q has no schema", name)
	}
	seen := map[*IRNode]bool{}
	var walk func(*IRNode) error
	walk = func(cur *IRNode) error {
		if cur == nil {
			return fmt.Errorf("apidoc: component %q holds a nil sub-schema", name)
		}
		if seen[cur] {
			return nil // shared or self-referential node: already validated
		}
		seen[cur] = true
		if err := cur.checkInvariants(); err != nil {
			return fmt.Errorf("apidoc: component %q: %w", name, err)
		}
		// forEachChild only yields an *IRNode-shaped AdditionalProperties, so
		// the nil|bool|*IRNode type check must run here, before descending.
		switch ap := cur.AdditionalProperties.(type) {
		case nil, bool, *IRNode:
		default:
			return fmt.Errorf("apidoc: component %q: additionalProperties must be nil, bool or *IRNode, got %T", name, ap)
		}
		return cur.forEachChild(walk)
	}
	return walk(n)
}

// ---------------------------------------------------------------------------
// Verify
// ---------------------------------------------------------------------------

// Verify reports every unresolved $ref in the assembled set at once — a
// dangling reference found one-at-a-time turns document assembly into a
// guessing loop. A reference resolves when it names a registered entry or a
// name whitelisted via ExternalRefs.
//
// The walk covers every typed sub-schema (Props, Items, arms, Not,
// AdditionalProperties — no special casing) plus the refs collected at
// construction time from Opaque nodes' raw bytes and from Extensions values.
func (c *Components) Verify() error {
	missing := map[string]bool{}
	for _, name := range slices.Sorted(maps.Keys(c.entries)) {
		for _, r := range refsOf(c.entries[name]) {
			if c.entries[r] == nil && !c.external[r] {
				missing[name+" -> "+r] = true
			}
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("apidoc: %d unresolved $ref(s): %s", len(missing), joinSorted(missing))
}

// refsOf returns every component name referenced from n, in a deterministic
// order (structure order; Opaque refs in the order collected at construction).
func refsOf(n *IRNode) []string {
	var out []string
	seenNode := map[*IRNode]bool{}
	var walk func(*IRNode) error
	walk = func(cur *IRNode) error {
		if cur == nil || seenNode[cur] {
			return nil
		}
		seenNode[cur] = true
		if cur.Ref != "" {
			out = append(out, cur.Ref)
		}
		out = append(out, cur.opaqueRefs...)
		out = append(out, cur.extRefs...)
		return cur.forEachChild(walk)
	}
	_ = walk(n) // walk never returns an error; the signature is forEachChild's
	return out
}

// ---------------------------------------------------------------------------
// Merge
// ---------------------------------------------------------------------------

// Merge inserts every registered fragment into dst (a components/schemas map),
// in sorted name order. A name already present in dst is an error — components
// never silently clobber an existing schema, and the clash check runs over
// every name BEFORE the first insert so a rejected merge leaves dst untouched.
//
// Each value is a json.RawMessage produced by the component's own MarshalJSON,
// NOT a map[string]any: routing a component through a generic map would lose
// property order and an Opaque fragment's exact bytes.
//
// CONSEQUENCE FOR THE CALLER: the final document emitter MUST encode with a
// non-HTML-escaping encoder —
//
//	enc := json.NewEncoder(w); enc.SetEscapeHTML(false)
//
// because encoding/json re-escapes <, > and & inside a nested RawMessage.
// Merge exists for BuildInto compatibility; assembly that can consume IR
// directly should do so.
func (c *Components) Merge(dst map[string]any) error {
	names := slices.Sorted(maps.Keys(c.entries))
	for _, name := range names {
		if _, clash := dst[name]; clash {
			return fmt.Errorf("apidoc: component %q collides with a pre-existing schema; refusing to clobber", name)
		}
	}
	encoded := make([][]byte, len(names))
	for i, name := range names {
		b, err := c.entries[name].MarshalJSON()
		if err != nil {
			return fmt.Errorf("apidoc: component %q: %w", name, err)
		}
		encoded[i] = b
	}
	for i, name := range names {
		dst[name] = json.RawMessage(encoded[i])
	}
	return nil
}
