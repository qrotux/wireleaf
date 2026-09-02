package huma

// convert.go — the IR → huma.Schema converter.
//
// It is a FIELD-BY-FIELD copy, not a translation table: wireleaf's typed IR and
// huma's Schema model the same OAS 3.1 subset, and anything huma cannot
// represent was already rejected at IR assembly time. The only two places where
// the shapes genuinely differ are called out below (type arrays, contentMediaType),
// plus the Opaque escape hatch, which rides through huma's inline Extensions map.

import (
	"encoding/json"
	"fmt"

	humav2 "github.com/danielgtaylor/huma/v2"

	"github.com/qrotux/wireleaf/apidoc"
)

// toHuma converts one IR component into a huma schema, ready to be served AND
// to validate: PrecomputeMessages is applied once at the top (it recurses into
// items/properties/arms itself), because huma's validator reads the precomputed
// strings and the required map, and skips both on an unprepared schema.
func toHuma(n *apidoc.IRNode) (*humav2.Schema, error) {
	s, err := convertNode(n)
	if err != nil {
		return nil, err
	}
	s.PrecomputeMessages()
	return s, nil
}

func convertNode(n *apidoc.IRNode) (*humav2.Schema, error) {
	if n == nil {
		return nil, fmt.Errorf("adapters/huma: nil IR node")
	}

	// Opaque: the bytes ARE the fact. A zero typed Schema whose Extensions hold
	// the decoded fragment marshals back to exactly that fragment (huma copies
	// extensions over the typed fields last), at the documented cost that huma
	// does not validate against it — spec §5a's marked trade-off.
	if n.Kind == apidoc.KindOpaque {
		var m map[string]any
		if err := json.Unmarshal(n.Opaque, &m); err != nil {
			return nil, fmt.Errorf("adapters/huma: opaque fragment is not a JSON object: %w", err)
		}
		return &humav2.Schema{Extensions: m}, nil
	}

	s := &humav2.Schema{}

	// type: huma models it as ONE string plus a Nullable flag, so ["T","null"]
	// is the widest type array it can express.
	switch len(n.Types) {
	case 0:
	case 1:
		s.Type = n.Types[0]
	case 2:
		if n.Types[1] != "null" {
			return nil, fmt.Errorf("adapters/huma: type array %v has no huma representation (huma models one type plus a nullable flag)", n.Types)
		}
		s.Type = n.Types[0]
		s.Nullable = true
	default:
		return nil, fmt.Errorf("adapters/huma: type array %v has no huma representation (huma models one type plus a nullable flag)", n.Types)
	}

	// $ref: the IR stores the BARE component name; the prefix belongs to the
	// serialized form.
	if n.Ref != "" {
		s.Ref = apidoc.RefPrefix + n.Ref
	}

	// properties + required. Property ORDER is lost here — huma holds a map and
	// sorts on marshal. That is acceptable precisely because the whole served
	// document goes through this one converter, so the ordering idiom is uniform.
	//
	// An object ALWAYS gets a non-nil Properties map, empty struct included:
	// huma's own SchemaLinkTransformer (installed by DefaultConfig) writes the
	// "$schema" property into the map of every object schema it serves, and a
	// write to a nil map panics.
	if n.Kind == apidoc.KindObject || len(n.Props) > 0 {
		s.Properties = make(map[string]*humav2.Schema, len(n.Props))
		for _, p := range n.Props {
			ps, err := convertNode(p.Schema)
			if err != nil {
				return nil, fmt.Errorf("adapters/huma: property %q: %w", p.Name, err)
			}
			s.Properties[p.Name] = ps
			if p.Required {
				s.Required = append(s.Required, p.Name)
			}
		}
	}

	var err error
	if n.Items != nil {
		if s.Items, err = convertNode(n.Items); err != nil {
			return nil, fmt.Errorf("adapters/huma: items: %w", err)
		}
	}
	if n.Not != nil {
		if s.Not, err = convertNode(n.Not); err != nil {
			return nil, fmt.Errorf("adapters/huma: not: %w", err)
		}
	}
	if s.AnyOf, err = convertArms("anyOf", n.AnyOf); err != nil {
		return nil, err
	}
	if s.OneOf, err = convertArms("oneOf", n.OneOf); err != nil {
		return nil, err
	}
	if s.AllOf, err = convertArms("allOf", n.AllOf); err != nil {
		return nil, err
	}

	switch ap := n.AdditionalProperties.(type) {
	case nil:
	case bool:
		s.AdditionalProperties = ap
	case *apidoc.IRNode:
		sub, err := convertNode(ap)
		if err != nil {
			return nil, fmt.Errorf("adapters/huma: additionalProperties: %w", err)
		}
		s.AdditionalProperties = sub
	default:
		return nil, fmt.Errorf("adapters/huma: additionalProperties must be nil, bool or *apidoc.IRNode, got %T", ap)
	}

	if len(n.Enum) > 0 {
		s.Enum = append([]any(nil), n.Enum...)
	}

	// The bare null type has to be ENFORCED, not just declared. huma's validator
	// switches on Schema.Type and has no "null" case, so a {"type":"null"} arm
	// would accept every value — and since the canonical nullable-reference idiom
	// is anyOf[$ref, {"type":"null"}], that one hole would make every nullable
	// reference in the document unvalidated (an object missing its required
	// properties would pass through the null arm).
	//
	// An enum of exactly [null] closes it: huma's enum check is a plain
	// membership test that runs for every type, so only a real null matches.
	// DRIFT: the served document carries "enum":[null] on such arms. In JSON
	// Schema 2020-12 {"type":"null"} and {"type":"null","enum":[null]} are
	// semantically identical, so the document stays correct — it is just more
	// explicit than the IR.
	if len(n.Types) == 1 && n.Types[0] == "null" && len(s.Enum) == 0 {
		s.Enum = []any{nil}
	}
	s.Const = n.Const
	s.Default = n.Default
	if len(n.Examples) > 0 {
		s.Examples = append([]any(nil), n.Examples...)
	}

	// Validation keywords, 1:1. Pointers are COPIED, never aliased: a mutated
	// converted schema must not reach back into the IR.
	s.Minimum = copyFloat(n.Minimum)
	s.Maximum = copyFloat(n.Maximum)
	s.ExclusiveMinimum = copyFloat(n.ExclusiveMinimum)
	s.ExclusiveMaximum = copyFloat(n.ExclusiveMaximum)
	s.MultipleOf = copyFloat(n.MultipleOf)
	s.MinLength = copyInt(n.MinLength)
	s.MaxLength = copyInt(n.MaxLength)
	s.MinItems = copyInt(n.MinItems)
	s.MaxItems = copyInt(n.MaxItems)
	s.MinProperties = copyInt(n.MinProperties)
	s.MaxProperties = copyInt(n.MaxProperties)
	s.UniqueItems = n.UniqueItems
	s.Pattern = n.Pattern
	s.Format = n.Format
	s.Title = n.Title
	s.Description = n.Description
	s.ContentEncoding = n.ContentEncoding
	s.ReadOnly = n.ReadOnly
	s.WriteOnly = n.WriteOnly
	s.Deprecated = n.Deprecated

	if len(n.DependentRequired) > 0 {
		s.DependentRequired = make(map[string][]string, len(n.DependentRequired))
		for k, v := range n.DependentRequired {
			s.DependentRequired[k] = append([]string(nil), v...)
		}
	}

	// Extensions pass through document-only. contentMediaType joins them: huma's
	// Schema has no such field, and an inline key is the correct OAS output
	// (plan decision #11).
	if len(n.Extensions) > 0 || n.ContentMediaType != "" {
		s.Extensions = make(map[string]any, len(n.Extensions)+1)
		for k, v := range n.Extensions {
			s.Extensions[k] = v
		}
		if n.ContentMediaType != "" {
			s.Extensions["contentMediaType"] = n.ContentMediaType
		}
	}

	return s, nil
}

func convertArms(what string, arms []*apidoc.IRNode) ([]*humav2.Schema, error) {
	if len(arms) == 0 {
		return nil, nil
	}
	out := make([]*humav2.Schema, 0, len(arms))
	for i, a := range arms {
		c, err := convertNode(a)
		if err != nil {
			return nil, fmt.Errorf("adapters/huma: %s[%d]: %w", what, i, err)
		}
		out = append(out, c)
	}
	return out, nil
}

func copyFloat(v *float64) *float64 {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

func copyInt(v *int) *int {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}
