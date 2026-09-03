package apidoc

// edgeshape.go — the IR builders for an includable edge's VALUE schema.
//
// One place owns the edge shapes so the static component (emit.go) and the
// include-aware recompute (schemafor.go) cannot drift:
//
//	to-one   → anyOf[ $ref, null ]   (Edge.Required → a BARE $ref, no null arm)
//	bare     → anyOf[ array<$ref>, null ]
//	to-many  → anyOf[ {items: array<$ref>, hasMore: bool, nextCursor?: string}, null ]
//	in-array → anyOf[ array<{ subField: anyOf[$ref,null] }>, null ]  (subField required)
//	computed → the declared ComputedSchema's IR, spliced verbatim
//
// A non-plain `Edge.Envelope` switches to wrappedEdgeShape (see there).
//
// `nextCursor` is OPTIONAL: the engine omits the key entirely when a fetcher
// returns no continuation token (include.ParentRows.NextCursor == "").
//
// A COMPUTED edge carries no Target (the thunk is a nil func), so it never
// reaches edgeShape: callers branch on Edge.Computed first and use computedSchemaIR.

import (
	"fmt"

	"github.com/qrotux/wireleaf/include"
)

// edgeShape is the single shape table; target is whatever stands in for the
// edge's destination (a $ref node, or an inlined already-computed object — the
// shape is identical, only the leaf differs). An in-array edge REQUIRES a
// non-empty SubField; callers validate that before calling (the signature has
// no error path).
func edgeShape(e include.Edge, target *IRNode) *IRNode {
	if include.EdgeKind(e) != include.KindComputed && !e.Envelope.Plain() {
		return wrappedEdgeShape(e, target)
	}
	switch include.EdgeKind(e) {
	case include.KindComputed:
		// Falling through to the default branch would silently dress a computed
		// value up as a to-many envelope. A computed edge has no target at all;
		// its schema comes from computedSchemaIR, and reaching here means a
		// caller forgot to branch on Computed first.
		panic("apidoc: edgeShape called on a computed edge — callers must branch on Edge.Computed first")

	case include.KindInArray:
		row := newObject([]Prop{{
			Name:     e.SubField,
			Schema:   newAnyOf(target, newScalar("null")),
			Required: true,
		}})
		return newAnyOf(newArray(row), newScalar("null"))

	case include.KindToOne:
		// Edge.Required (graph.Required(), legal on to-one only) declares the
		// target ALWAYS present: the value is a bare $ref with no null arm, and
		// the stitching side lists the key in `required` (emit.go's stitchEdges,
		// SchemaFor's setRequiredProp).
		//
		// This shape does NOT vary with the edge's RequiredMissing policy
		// (include.MissingRequiredNull / …Error). That policy governs the
		// ENGINE — whether a required target that is not there becomes a null or
		// fails the request — and Required() remains what the declaration says:
		// non-null. Widening the document to anyOf[$ref,null] under the tolerant
		// policy would be the library quietly papering over a server-side fault
		// (an orphaned FK, or a fetcher hiding a row the contract promised).
		// Emitting the null while the document says non-null is the declaring
		// application's responsibility, and it stays visible as such.
		if e.Required {
			return target
		}
		return newAnyOf(target, newScalar("null"))

	default: // KindForwardHasMany / KindReverse
		if e.Bare {
			return newAnyOf(newArray(target), newScalar("null"))
		}
		env := newObject([]Prop{
			{Name: "items", Schema: newArray(target), Required: true},
			{Name: "hasMore", Schema: newScalar("boolean"), Required: true},
			{Name: "nextCursor", Schema: newScalar("string")}, // OPTIONAL
		})
		return newAnyOf(env, newScalar("null"))
	}
}

// wrappedEdgeShape is the second half of the shape table — the value schema
// of an edge whose include.Edge.Envelope is not plain:
//
//	to-one   → { <Key>: anyOf[$ref, null] }          (Required → { <Key>: $ref })
//	to-many  → { <Key>: array<{ <Key>: $ref }>, <Pagination>: {hasNextPage, nextCursor?} }
//	bare, or Pagination == "" → { <Key>: array<{ <Key>: $ref }> }
//	in-array → anyOf[ array<{ subField: { <Key>: anyOf[$ref,null] } }>, null ]
//
// The edge key is ALWAYS an object in this style — the engine writes
// {"<Key>":null} for a missing to-one target and {"<Key>":[]} for an empty
// list — so no outer null arm exists on to-one or list edges. The in-array
// outer null is the parent's own array member and stays.
func wrappedEdgeShape(e include.Edge, target *IRNode) *IRNode {
	env := e.Envelope
	wrap := func(inner *IRNode) *IRNode {
		return newObject([]Prop{{Name: env.Key, Schema: inner, Required: true}})
	}
	switch include.EdgeKind(e) {
	case include.KindInArray:
		row := newObject([]Prop{{
			Name:     e.SubField,
			Schema:   wrap(newAnyOf(target, newScalar("null"))),
			Required: true,
		}})
		return newAnyOf(newArray(row), newScalar("null"))

	case include.KindToOne:
		if e.Required {
			return wrap(target)
		}
		return wrap(newAnyOf(target, newScalar("null")))

	default: // KindForwardHasMany / KindReverse
		props := []Prop{{Name: env.Key, Schema: newArray(wrap(target)), Required: true}}
		if !e.Bare && env.Pagination != "" {
			props = append(props, Prop{
				Name: env.Pagination,
				Schema: newObject([]Prop{
					{Name: "hasNextPage", Schema: newScalar("boolean"), Required: true},
					{Name: "nextCursor", Schema: newScalar("string")}, // OPTIONAL
				}),
				Required: true,
			})
		}
		return newObject(props)
	}
}

// computedSchemaIR unboxes a computed edge's ComputedSchema. include stores it
// as `any` to stay free of an apidoc import (which would be an import cycle),
// so the doc layer asserts it back here — the ONE place that assertion lives.
func computedSchemaIR(e include.Edge) (*IRNode, error) {
	if e.ComputedSchema == nil {
		return nil, fmt.Errorf("computed edge declares no ComputedSchema")
	}
	s, ok := e.ComputedSchema.(Schema)
	if !ok {
		return nil, fmt.Errorf("computed edge ComputedSchema is a %T, want apidoc.Schema", e.ComputedSchema)
	}
	if s.n == nil {
		return nil, fmt.Errorf("computed edge ComputedSchema is an empty apidoc.Schema")
	}
	return s.n, nil
}
