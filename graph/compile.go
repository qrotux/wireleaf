package graph

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/qrotux/wireleaf/apidoc"
	"github.com/qrotux/wireleaf/include"
)

// defaultEdgeLimit is the per-edge top-N applied to an enveloped to-many edge
// that declares no Limit (spec §2).
const defaultEdgeLimit = 20

// ------------------------------------------------------------------ error type

// Finding is one compile problem, located at a node (and optionally one of its
// edges). Msg carries a stable, greppable phrase.
type Finding struct {
	Node string
	Edge string
	Msg  string
}

// String renders "Node.edge: msg".
func (f Finding) String() string {
	switch {
	case f.Node != "" && f.Edge != "":
		return f.Node + "." + f.Edge + ": " + f.Msg
	case f.Node != "":
		return f.Node + ": " + f.Msg
	case f.Edge != "":
		return "edge " + f.Edge + ": " + f.Msg
	}
	return f.Msg
}

// CompileError is the single error Compile returns: it carries EVERY finding
// of one run, never just the first (spec §1).
type CompileError struct{ Findings []Finding }

// Error joins all findings, one per line.
func (e *CompileError) Error() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "graph: %d compile finding(s):", len(e.Findings))
	for _, f := range e.Findings {
		sb.WriteString("\n  - ")
		sb.WriteString(f.String())
	}
	return sb.String()
}

// findingList accumulates findings in discovery order.
type findingList []Finding

func (l *findingList) add(node, edge, format string, args ...any) {
	*l = append(*l, Finding{Node: node, Edge: edge, Msg: fmt.Sprintf(format, args...)})
}

// ------------------------------------------------------------------ Compile

// edgeBuild is the per-edge working set of the compile passes: the flattened
// options plus the resolved kind/target, kept so the inverse, default-cycle
// and reachability passes can read edges without re-applying options.
type edgeBuild struct {
	owner  *nodeSpec
	key    string
	kind   include.EdgeKindType
	target *nodeSpec
	set    edgeSettings
}

// Compile validates every declaration and freezes the builder. It is the ONLY
// place validation happens: the builder is dead afterwards and any further
// declaration call — including a second Compile — panics.
//
// Every problem of one run surfaces together in a *CompileError; there is no
// fail-fast. On a clean build the returned *Graph is immutable and doubles as
// the engine's include.Registry.
func (b *Builder) Compile() (*Graph, error) {
	b.mustLive()
	b.dead = true

	var fs findingList

	checkEnvelope(&fs, "", "", b.envelope, b.envelopeSet)

	// --- pass A: node identity ---------------------------------------------
	byName := make(map[string]*nodeSpec, len(b.nodes))
	byWire := make(map[reflect.Type]*nodeSpec, len(b.nodes))
	compiled := make(map[*nodeSpec]*compiledNode, len(b.nodes))
	order := make([]*compiledNode, 0, len(b.nodes))

	for _, n := range b.nodes {
		if n.name == "" {
			fs.add("", "", "node name must not be empty")
			continue
		}
		if _, dup := byName[n.name]; dup {
			fs.add(n.name, "", "duplicate node name")
			continue
		}
		byName[n.name] = n
		if prev, dup := byWire[n.wireT]; dup {
			fs.add(n.name, "", "wire type %s is already used by node %s", n.wireT, prev.name)
		} else {
			byWire[n.wireT] = n
		}
		cn := &compiledNode{
			name:         n.name,
			wireT:        n.wireT,
			defaults:     n.defaults,
			docExternal:  n.docExternal,
			wireFn:       n.wireFn,
			primaryKeyFn: n.primaryKeyFn,
			enrichFn:     n.enrichFn,
			fetchIDs:     n.fetchIDs,
			fetchParents: n.fetchParents,
			edges:        map[string]include.Edge{},
		}
		compiled[n] = cn
		order = append(order, cn)
	}

	// --- pass B: per-node shape and closures --------------------------------
	for _, n := range b.nodes {
		cn := compiled[n]
		if cn == nil {
			continue
		}

		cn.slug = n.slug
		if !n.slugSet || cn.slug == "" {
			cn.slug = strings.ToLower(n.name)
		}

		sh := deriveShape(n.wireT)
		for _, msg := range sh.errs {
			fs.add(n.name, "", "%s", msg)
		}
		cn.fields, cn.verdicts, cn.cols, cn.sortCols = sh.fields, sh.verdicts, sh.cols, sh.sortCols

		checkNodeClosures(&fs, n)
		checkEnvelope(&fs, n.name, "", n.envelope, n.envelopeSet)
	}

	// --- pass C: edges ------------------------------------------------------
	edgesByNode := make(map[*nodeSpec]map[string]*edgeBuild, len(b.nodes))
	edgeOrder := make(map[*nodeSpec][]*edgeBuild, len(b.nodes))

	for _, n := range b.nodes {
		cn := compiled[n]
		if cn == nil {
			continue
		}
		seen := make(map[string]*edgeBuild, len(n.edges))
		for _, decl := range n.edges {
			if _, dup := seen[decl.key]; dup {
				fs.add(n.name, decl.key, "duplicate edge key")
				continue
			}
			if needsJSONEscape(decl.key) {
				fs.add(n.name, decl.key, "edge key %q needs JSON escaping (quotes, backslashes, control characters and invalid UTF-8 are not allowed)", decl.key)
			}
			if slices.Contains(cn.fields, decl.key) {
				fs.add(n.name, decl.key, "edge key %q collides with wire field %q (the response would carry the member twice)", decl.key, decl.key)
			}
			eb := &edgeBuild{
				owner: n,
				key:   decl.key,
				kind:  decl.kind.kind,
				set:   *decl.set,
			}
			// Target resolution: the kind addresses its target by WIRE TYPE;
			// byWire (pass A) maps it to the declaring node. Unknown types are
			// findings here so buildEdge and the later passes can treat a nil
			// target uniformly.
			if decl.kind.kind != include.KindComputed {
				switch tw := decl.kind.targetWireT; tw {
				case nil:
					fs.add(n.name, decl.key, "edge has no target node")
				default:
					if tn, ok := byWire[tw]; ok {
						eb.target = tn
					} else {
						fs.add(n.name, decl.key, "edge target: no node of this builder declares wire type %s", tw)
					}
				}
			}
			seen[decl.key] = eb
			edgeOrder[n] = append(edgeOrder[n], eb)

			edge := buildEdge(&fs, n, decl, eb, compiled, b.envelope)
			cn.edgeKeys = append(cn.edgeKeys, decl.key)
			cn.edges[decl.key] = edge
		}
		edgesByNode[n] = seen

		inDefaults := make(map[string]bool, len(n.defaults))
		for _, key := range n.defaults {
			if _, ok := seen[key]; !ok {
				fs.add(n.name, key, "Defaults names unknown edge key %q", key)
			}
			inDefaults[key] = true
		}

		// Required() IMPLIES default-included: the key is in the component's
		// `required` set, and the engine only materializes edges of the plan's
		// default ∪ client tree — so the ONLY legal configuration has every
		// Required() edge in Defaults(). One legal outcome means CONSTRUCT it,
		// not report it: required keys the application did not list are
		// appended here, after the explicit defaults, in edge-declaration
		// order (deterministic byte order). Mutating n.defaults is safe — the
		// builder is dead — and cn.defaults is re-pointed so every later pass
		// (default cycles, fetcher completeness, the engine itself) sees the
		// augmented list.
		for _, eb := range edgeOrder[n] {
			if eb.set.required && !inDefaults[eb.key] {
				n.defaults = append(n.defaults, eb.key)
				inDefaults[eb.key] = true
			}
		}
		cn.defaults = n.defaults
	}

	// --- pass C2: unbounded edges under a to-many edge ------------------------
	// A Bare/in-array edge multiplies by EstimatedRows (default 100). Where
	// its owner is itself reached through a to-many edge, that default
	// multiplies a second time, so the developer must state the estimate.
	manyTarget := make(map[*nodeSpec]bool, len(b.nodes))
	for _, n := range b.nodes {
		for _, eb := range edgeOrder[n] {
			if eb.target != nil && kindIsMany(eb.kind) {
				manyTarget[eb.target] = true
			}
		}
	}
	for _, n := range b.nodes {
		if !manyTarget[n] {
			continue
		}
		for _, eb := range edgeOrder[n] {
			if isUnbounded(eb.kind, eb.set) && !eb.set.estimatedRowsSet {
				fs.add(n.name, eb.key, "unbounded edge %q under a to-many edge: declare EstimatedRows (the cost model otherwise assumes 100 rows per parent)", eb.key)
			}
		}
	}

	// --- pass C3: filterable edges must lead somewhere ----------------------
	// A Filterable() edge whose target exposes neither a filterable column nor
	// a filterable edge is inert: no condition can ever be written through it.
	// Checked after pass C so every target's edges are known.
	for _, n := range b.nodes {
		for _, eb := range edgeOrder[n] {
			if !eb.set.filterable || eb.target == nil || compiled[eb.target] == nil {
				continue
			}
			if !hasFilterSurface(compiled[eb.target], edgeOrder[eb.target]) {
				fs.add(n.name, eb.key, "Filterable() edge to node %s, which has no filterable column and no filterable edge", eb.target.name)
			}
		}
	}

	// --- pass D: inverse pairing -------------------------------------------
	for _, n := range b.nodes {
		for _, eb := range edgeOrder[n] {
			checkInverse(&fs, eb, edgesByNode)
		}
	}

	// --- pass E: default-edge cycles ---------------------------------------
	checkDefaultCycles(&fs, b.nodes, edgesByNode)

	// --- pass F: reachability + fetcher completeness ------------------------
	checkFetchers(&fs, b, edgeOrder)

	if len(fs) > 0 {
		return nil, &CompileError{Findings: fs}
	}

	g := &Graph{byName: make(map[string]*compiledNode, len(order))}
	for _, cn := range order {
		g.byName[cn.name] = cn
	}
	seenRoot := map[string]bool{}
	for _, r := range b.roots {
		cn := compiled[r]
		if cn == nil || seenRoot[cn.name] {
			continue
		}
		seenRoot[cn.name] = true
		g.roots = append(g.roots, cn)
	}
	return g, nil
}

// ------------------------------------------------------------------ node checks

// checkNodeClosures reports the mandatory/nil problems of one node's
// Wire/PrimaryKey/Enrich declarations. There are no type-mismatch findings any
// more: the chained methods are typed by the handle's own Row/Wire parameters,
// so a closure typed on another node's types is a Go COMPILE error, not a
// runtime finding.
func checkNodeClosures(fs *findingList, n *nodeSpec) {
	requireClosure(fs, n.name, "", n.wireSet, n.wireFn == nil,
		"Wire is required", fmt.Sprintf("Wire(nil) on node %s", n.name))
	requireClosure(fs, n.name, "", n.primaryKeySet, n.primaryKeyFn == nil,
		"PrimaryKey is required", fmt.Sprintf("PrimaryKey(nil) on node %s", n.name))
	if n.enrichSet && n.enrichFn == nil {
		fs.add(n.name, "", "Enrich(nil) on node %s", n.name)
	}
}

// requireClosure reports a mandatory closure declaration in its two failure
// modes — never declared (missing) or declared with a nil func (nilMsg). The
// same pattern covers node closures (Wire/PrimaryKey) and edge FK closures
// (ForeignKey/ForeignKeys).
func requireClosure(fs *findingList, node, edge string, declared, isNil bool, missing, nilMsg string) {
	switch {
	case !declared:
		fs.add(node, edge, "%s", missing)
	case isNil:
		fs.add(node, edge, "%s", nilMsg)
	}
}

// ------------------------------------------------------------------ envelope

// checkEnvelope validates one DECLARED include.Envelope at the layer it was
// declared on (graph: node=="" && edge==""; node: edge==""; edge: both), so
// the finding points at the declaration. An undeclared value is the zero
// value and always valid.
func checkEnvelope(fs *findingList, node, edge string, env include.Envelope, declared bool) {
	if !declared {
		return
	}
	if env.Key == "" && env.Pagination != "" {
		fs.add(node, edge, "Envelope: Pagination %q without Key (there is no wrapper to put it in)", env.Pagination)
	}
	if env.Key != "" && env.Key == env.Pagination {
		fs.add(node, edge, "Envelope: Key and Pagination collide (%q)", env.Key)
	}
	for _, k := range []string{env.Key, env.Pagination} {
		if needsJSONEscape(k) {
			fs.add(node, edge, "Envelope: member %q needs JSON escaping (quotes, backslashes, control characters and invalid UTF-8 are not allowed)", k)
		}
	}
}

// needsJSONEscape reports whether s cannot be written inside a JSON string
// literal verbatim. The engine splices member names byte-for-byte, so invalid
// UTF-8 counts too: encoding/json would escape such bytes to U+FFFD, and the
// spliced bytes would not be valid JSON.
func needsJSONEscape(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c == '"' || c == '\\' || c < 0x20 {
			return true
		}
	}
	return !utf8.ValidString(s)
}

// ------------------------------------------------------------------ edge build

// buildEdge validates one declared edge's kind↔option coherence and returns the
// engine-facing include.Edge. Findings are recorded, never returned: a broken
// edge still yields a (partial) value so later passes can keep walking.
func buildEdge(
	fs *findingList,
	owner *nodeSpec,
	decl edgeDecl,
	eb *edgeBuild,
	compiled map[*nodeSpec]*compiledNode,
	graphEnv include.Envelope,
) include.Edge {
	k, s := decl.kind, eb.set
	name, key := owner.name, decl.key

	// Target resolution happened in pass C (wire type → nodeSpec, findings
	// included); here it only maps onto the compiled node, nil-safe.
	var targetNode *compiledNode
	if eb.target != nil {
		targetNode = compiled[eb.target]
	}

	many := k.kind == include.KindForwardHasMany ||
		k.kind == include.KindReverse ||
		k.kind == include.KindInArray

	// (No Row-type mismatch findings here any more: EdgeBuilder[Row] types the
	// ForeignKey/ForeignKeys/Guard closures by the parent's Row, so a mismatch
	// is a Go compile error, not something Compile can ever see.)
	if s.guardSet && s.guard == nil {
		fs.add(name, key, "Guard(nil) on edge %s", key)
	}

	// --- kind ↔ option coherence -------------------------------------------
	switch k.kind {
	case include.KindToOne:
		requireForeignKey(fs, name, key, s, "to-one")
	case include.KindForwardHasMany:
		requireForeignKeys(fs, name, key, s, "forward-hasMany")
	case include.KindReverse:
		if k.backref == "" {
			fs.add(name, key, "reverse edge requires a backref")
		}
	case include.KindInArray:
		if k.arrayPath == "" || k.subField == "" {
			fs.add(name, key, "in-array edge requires arrayPath and subField")
		}
		requireForeignKeys(fs, name, key, s, "in-array")
	case include.KindComputed:
		if s.inverse != "" {
			fs.add(name, key, "computed edge cannot declare Inverse")
		}
	}

	checkOptionMatrix(fs, name, key, k.kind, s)

	if s.required && k.kind != include.KindToOne {
		fs.add(name, key, "Required() is only valid on to-one edges (this is %s)", k.kind)
	}

	// Required() promises the key is ALWAYS present and non-null in the
	// document; a Guard() on the same edge makes the engine emit null for
	// every guard-false parent. The pair guarantees a response that violates
	// the published component, so it is rejected here — the same coupling
	// logic as the Required↔Defaults check above.
	if s.required && s.guardSet {
		fs.add(name, key,
			"Required() edge cannot carry Guard(): a guard-false parent yields null under a key the document declares required and non-null")
	}

	// --- Filterable ---------------------------------------------------------
	// To-one only: a condition through a to-many edge needs a quantifier
	// (any / all / none) that the filter model does not carry yet, and an
	// inert declaration is a finding. Guard is a Go closure over the parent
	// row; a SQL-side filter cannot honour it, so the pair would leak rows the
	// include would have hidden.
	if s.filterable && k.kind != include.KindToOne {
		fs.add(name, key, "Filterable() is only valid on to-one edges (this is %s)", k.kind)
	}
	if s.filterable && s.guardSet {
		fs.add(name, key, "Filterable() edge cannot carry Guard(): a guard is a Go closure over the parent row, which a SQL-side filter cannot honour")
	}

	// --- declared args vs the built-ins -------------------------------------
	// ":limit" is engine-owned: it is coerced to an int at plan time and
	// clamped to Edge.Limit at materialize time. A declared arg of that name
	// would read as an application-owned parameter while behaving as neither,
	// so it is a wiring bug. ":sort" stays legal — a declared sort override is
	// a supported feature.
	for _, a := range s.args {
		if a.Name == "limit" {
			fs.add(name, key, "edge arg %q conflicts with the built-in :limit", a.Name)
		}
	}

	// --- sort whitelist -----------------------------------------------------
	var sortCols map[string]string
	if targetNode != nil {
		sortCols = targetNode.sortCols
	}
	if s.sort != "" {
		sortKey := strings.TrimPrefix(s.sort, "-")
		if _, ok := sortCols[sortKey]; !ok {
			tgt := "<none>"
			if targetNode != nil {
				tgt = targetNode.name
			}
			fs.add(name, key, "Sort(%q): key %q is not a sortCol of node %s", s.sort, sortKey, tgt)
		}
	}

	// --- limit --------------------------------------------------------------
	// The default ceiling is scoped to the kinds Limit is legal on (reverse +
	// forward-hasMany). In-array edges load through the forward FetchIDs batch
	// and EdgeQuery does not apply to them (spec §2), so a stamped ceiling
	// would be inert noise; they compile to Limit 0.
	limit := s.limit
	enveloped := k.kind == include.KindReverse || k.kind == include.KindForwardHasMany
	if !s.limitSet && enveloped && !s.bare {
		limit = defaultEdgeLimit
	}

	// --- envelope: edge > target node > graph --------------------------------
	checkEnvelope(fs, name, key, s.envelope, s.envelopeSet)
	env := graphEnv
	if eb.target != nil && eb.target.envelopeSet {
		env = eb.target.envelope
	}
	if s.envelopeSet {
		env = s.envelope
	}
	if k.kind == include.KindComputed {
		env = include.Envelope{} // nothing engine-produced to wrap
	}

	e := include.Edge{
		Many:       many,
		Required:   s.required,
		Includable: s.includable,
		Filterable: s.filterable,
		// No FK FIELD NAME is carried at all: v1 reads forward FKs through the
		// typed ForeignKey/ForeignKeys closures, never through a field name.
		Backref:       k.backref,
		ArrayPath:     k.arrayPath,
		SubField:      k.subField,
		Limit:         limit,
		Bare:          s.bare,
		EstimatedRows: s.estimatedRows,
		Envelope:      env,
		Sort:          s.sort,
		// SortCols feeds ONLY EdgeQuery.Sort resolution, which exists on
		// reverse edges alone; carrying the whitelist on other kinds would
		// make the engine accept a client :sort() it then silently ignores.
		SortCols: reverseSortCols(k.kind, sortCols),
		Args:     s.args,
		Guard:    s.guard,
		// Per-edge policy overrides; nil = inherit the request's Options.
		ExcludeRequired: s.policies.ExcludeRequired,
		MissingRequired: s.policies.MissingRequired,
		MissingForeign:  s.policies.MissingForeign,
		ForeignKey:      s.foreignKey,
		ForeignKeys:     s.foreignKeys,
		Computed:        k.kind == include.KindComputed,
	}
	if k.kind == include.KindComputed {
		e.ComputedSchema = k.schema
	}
	if targetNode != nil {
		e.Target = func() include.Resource { return targetNode }
	}
	return e
}

// reverseSortCols narrows a target's sortCol whitelist to the one edge kind
// whose loading contract can honour it (EdgeQuery.Sort — reverse only).
func reverseSortCols(k include.EdgeKindType, cols map[string]string) map[string]string {
	if k != include.KindReverse {
		return nil
	}
	return cols
}

// hasFilterSurface reports whether a filter condition can name anything on
// cn: a filterable column, or a filterable edge leading further.
func hasFilterSurface(cn *compiledNode, edges []*edgeBuild) bool {
	for _, c := range cn.cols {
		if c.Filterable {
			return true
		}
	}
	for _, eb := range edges {
		if eb.set.filterable {
			return true
		}
	}
	return false
}

// checkOptionMatrix reports edge options that are MEANINGLESS for the declared
// kind. A silently-inert declaration is a bug the one-validation-point design
// must surface, so every cell outside the matrix below is a finding:
//
//	ForeignKey    to-one only          ForeignKeys   forward-hasMany + in-array
//	Limit   reverse + to-many    Bare    reverse + to-many
//	Sort    reverse only         Args    reverse + computed
//	Guard   everything but computed
//	Filterable    to-one only (checked by the caller, with its Guard and target rules)
//	Envelope      everything but computed
//
// (Required is to-one only and Inverse is forbidden on computed; both are
// checked by the caller alongside the kind's own required options.)
func checkOptionMatrix(fs *findingList, node, key string, k include.EdgeKindType, s edgeSettings) {
	forbid := func(declared bool, opt string, legal ...include.EdgeKindType) {
		if !declared {
			return
		}
		for _, ok := range legal {
			if k == ok {
				return
			}
		}
		fs.add(node, key, "%s is not valid on %s edges", opt, k)
	}

	forbid(s.foreignKeySet, "ForeignKey", include.KindToOne)
	forbid(s.foreignKeysSet, "ForeignKeys", include.KindForwardHasMany, include.KindInArray)
	forbid(s.limitSet, "Limit", include.KindReverse, include.KindForwardHasMany)
	forbid(s.bare, "Bare", include.KindReverse, include.KindForwardHasMany)
	forbid(s.sort != "", "Sort", include.KindReverse)
	forbid(len(s.args) > 0, "Args", include.KindReverse, include.KindComputed)
	forbid(s.guardSet, "Guard",
		include.KindToOne, include.KindForwardHasMany, include.KindReverse, include.KindInArray)
	forbid(s.envelopeSet, "Envelope",
		include.KindToOne, include.KindForwardHasMany, include.KindReverse, include.KindInArray)
	// The two REQUIRED-scoped policy overrides are inert anywhere else
	// (Required is to-one only, checked by the caller).
	if !s.required {
		if s.policies.ExcludeRequired != nil {
			fs.add(node, key, "Policies: ExcludeRequiredPolicy is only valid on Required() edges")
		}
		if s.policies.MissingRequired != nil {
			fs.add(node, key, "Policies: MissingRequiredPolicy is only valid on Required() edges")
		}
	}
	// MissingForeignPolicy is scoped by KIND instead: only an edge that reads a
	// parent-side FK can hold a dangling one.
	forbid(s.policies.MissingForeign != nil, "Policies: MissingForeignPolicy",
		include.KindToOne, include.KindForwardHasMany, include.KindInArray)

	if s.limitSet && s.limit < 1 {
		fs.add(node, key, "Limit must be >= 1 (got %d)", s.limit)
	}
	if s.limitSet && s.bare {
		fs.add(node, key, "Bare edge cannot carry Limit (a bare edge fetches all rows)")
	}
	if s.estimatedRowsSet {
		if !isUnbounded(k, s) {
			fs.add(node, key, "EstimatedRows is only valid on Bare() and in-array edges (enveloped edges multiply by Limit)")
		}
		if s.estimatedRows < 1 {
			fs.add(node, key, "EstimatedRows must be >= 1 (got %d)", s.estimatedRows)
		}
	}
}

// kindIsMany reports whether an edge of kind k produces a collection.
func kindIsMany(k include.EdgeKindType) bool {
	return k == include.KindForwardHasMany || k == include.KindReverse || k == include.KindInArray
}

// isUnbounded reports whether an edge of kind k with settings s fetches
// without a per-parent Limit: in-array always, reverse/forward-hasMany when Bare.
func isUnbounded(k include.EdgeKindType, s edgeSettings) bool {
	return k == include.KindInArray || (s.bare && (k == include.KindReverse || k == include.KindForwardHasMany))
}

func requireForeignKey(fs *findingList, node, key string, s edgeSettings, kind string) {
	requireClosure(fs, node, key, s.foreignKeySet, s.foreignKey == nil,
		fmt.Sprintf("%s edge requires ForeignKey", kind), fmt.Sprintf("ForeignKey(nil) on edge %s", key))
}

func requireForeignKeys(fs *findingList, node, key string, s edgeSettings, kind string) {
	requireClosure(fs, node, key, s.foreignKeysSet, s.foreignKeys == nil,
		fmt.Sprintf("%s edge requires ForeignKeys", kind), fmt.Sprintf("ForeignKeys(nil) on edge %s", key))
}

// ------------------------------------------------------------------ inverse

// isForward classifies an edge kind for inverse-direction checking: everything
// that reads an FK off the PARENT is forward; only KindReverse is not.
func isForward(k include.EdgeKindType) bool {
	return k == include.KindToOne || k == include.KindForwardHasMany || k == include.KindInArray
}

// checkInverse validates one Inverse declaration: the named edge must exist on
// the target, point back at this node, run the opposite direction, and — when
// it declares an Inverse of its own — name this very edge.
func checkInverse(fs *findingList, eb *edgeBuild, edgesByNode map[*nodeSpec]map[string]*edgeBuild) {
	if eb.set.inverse == "" || eb.kind == include.KindComputed {
		return
	}
	name, key := eb.owner.name, eb.key
	if eb.target == nil {
		return // already reported as a missing target
	}
	inv, ok := edgesByNode[eb.target][eb.set.inverse]
	if !ok {
		fs.add(name, key, "inverse: no edge %q on node %s", eb.set.inverse, eb.target.name)
		return
	}
	if inv.target != eb.owner {
		tgt := "<none>"
		if inv.target != nil {
			tgt = inv.target.name
		}
		fs.add(name, key, "inverse: edge %s.%s points at node %s, not %s", eb.target.name, inv.key, tgt, name)
		return
	}
	if isForward(eb.kind) == isForward(inv.kind) {
		fs.add(name, key, "inverse: edge %s.%s has the same direction (%s vs %s)",
			eb.target.name, inv.key, eb.kind, inv.kind)
	}
	if inv.set.inverse != "" && inv.set.inverse != key {
		fs.add(name, key, "inverse: edge %s.%s declares Inverse(%q), not %q",
			eb.target.name, inv.key, inv.set.inverse, key)
	}
}

// ------------------------------------------------------------------ default cycles

// checkDefaultCycles reports cycles in the graph formed by DEFAULT edges only
// (v0's runtime guard, moved to compile time — spec §1). A default include that
// loops back onto its own node would expand forever at plan time.
func checkDefaultCycles(fs *findingList, nodes []*nodeSpec, edgesByNode map[*nodeSpec]map[string]*edgeBuild) {
	const (
		white = 0
		grey  = 1
		black = 2
	)
	color := make(map[*nodeSpec]int, len(nodes))
	reported := map[*nodeSpec]bool{}

	var walk func(n *nodeSpec, path []string)
	walk = func(n *nodeSpec, path []string) {
		color[n] = grey
		path = append(path, n.name)
		for _, key := range n.defaults {
			eb, ok := edgesByNode[n][key]
			if !ok || eb.target == nil || eb.kind == include.KindComputed {
				continue
			}
			switch color[eb.target] {
			case grey:
				if !reported[n] {
					reported[n] = true
					fs.add(n.name, key, "default-edge cycle: %s -> %s",
						strings.Join(path, " -> "), eb.target.name)
				}
			case white:
				walk(eb.target, path)
			}
		}
		color[n] = black
	}

	for _, n := range nodes {
		if color[n] == white {
			walk(n, nil)
		}
	}
}

// ------------------------------------------------------------------ fetchers

// checkFetchers walks the edge graph from the roots and reports missing binds:
// a node targeted by a FORWARD edge needs FetchIDs; one targeted by a REVERSE
// edge needs FetchParents. Root nodes themselves need neither (the handler
// seeds them).
//
// The walk follows every INCLUDABLE edge and every DEFAULT edge. Defaults are
// in scope because the engine materializes Defaults() ∪ client keys at every
// level regardless of Includable — a non-includable default edge still fetches
// at runtime, so its target's bind is just as mandatory.
func checkFetchers(fs *findingList, b *Builder, edgeOrder map[*nodeSpec][]*edgeBuild) {
	visited := make(map[*nodeSpec]bool, len(b.nodes))
	queue := make([]*nodeSpec, 0, len(b.roots))
	for _, r := range b.roots {
		if !visited[r] {
			visited[r] = true
			queue = append(queue, r)
		}
	}

	needIDs := map[*nodeSpec][]string{}
	needParents := map[*nodeSpec][]string{}

	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		inDefaults := make(map[string]bool, len(n.defaults))
		for _, key := range n.defaults {
			inDefaults[key] = true
		}
		for _, eb := range edgeOrder[n] {
			if !eb.set.includable && !inDefaults[eb.key] {
				continue
			}
			if eb.target == nil {
				continue
			}
			via := n.name + "." + eb.key
			if eb.kind == include.KindReverse {
				needParents[eb.target] = append(needParents[eb.target], via)
			} else {
				needIDs[eb.target] = append(needIDs[eb.target], via)
			}
			if !visited[eb.target] {
				visited[eb.target] = true
				queue = append(queue, eb.target)
			}
		}
	}

	for _, n := range b.nodes {
		if via := needIDs[n]; len(via) > 0 && n.fetchIDs == nil {
			fs.add(n.name, "", "missing FetchIDs bind: reached by forward includable/default edge(s) %s", strings.Join(via, ", "))
		}
		if via := needParents[n]; len(via) > 0 && n.fetchParents == nil {
			fs.add(n.name, "", "missing FetchParents bind: reached by reverse includable/default edge(s) %s", strings.Join(via, ", "))
		}
	}
}

// ------------------------------------------------------------------ wire shape

// shape is the derived node shape: the serialized field names in walk order,
// their nullability verdicts, the column bindings and the sortCol whitelist
// (the Sortable projection of the bindings).
type shape struct {
	fields   []string
	verdicts map[string]apidoc.Verdict
	cols     map[string]include.Column
	sortCols map[string]string
	errs     []string
}

// wireField is one CANDIDATE serialized field found by the walk, before
// encoding/json's depth-dominance rule picks the winners.
type wireField struct {
	name   string
	depth  int  // 0 = declared on the outer struct, 1 = one embed deep, …
	tagged bool // the name came from an explicit json tag (encoding/json's tie-break)
	col    include.Column
	hasCol bool // a col / sortCol tag was present on a json-tagged field
	field  reflect.StructField
}

// deriveShape reflects a Wire type ONCE, following encoding/json's field rules
// — tag name wins, untagged exported field keeps its Go name, `json:"-"` is
// skipped, anonymous untagged structs are flattened, and a json name declared
// at several embedding depths is resolved by DEPTH DOMINANCE: the unique
// shallowest field wins (the standard Base-embed-override idiom, which
// marshals fine); a tie at the shallowest depth is broken in favour of a
// SINGLE json-tagged candidate (encoding/json's own tie-break); only a tie
// that survives both rules — several candidates, all tagged or all untagged —
// makes encoding/json drop the field entirely and is therefore a finding.
//
// Winning fields are registered in walk order (encoding/json's own output
// order) together with the nullability verdict from the ONE policy
// (apidoc.DefaultNullability — never re-derived here), the column bindings
// from the `col` / `sortCol` tags, and the sortCol whitelist (their Sortable
// projection).
func deriveShape(t reflect.Type) shape {
	s := shape{verdicts: map[string]apidoc.Verdict{}}
	st := derefType(t)
	if st == nil || st.Kind() != reflect.Struct {
		s.errs = append(s.errs, fmt.Sprintf("wire type %s is not a struct", t))
		return s
	}

	var cands []wireField
	collectWireFields(st, 0, map[reflect.Type]bool{}, &cands, &s.errs)

	// depth dominance: per name, the shallowest depth, how many candidates
	// share it and how many of THOSE carry an explicit json tag name.
	best := make(map[string]int, len(cands))
	tied := make(map[string]int, len(cands))
	tagged := make(map[string]int, len(cands))
	for _, c := range cands {
		d, ok := best[c.name]
		switch {
		case !ok || c.depth < d:
			best[c.name], tied[c.name] = c.depth, 1
			tagged[c.name] = 0
			if c.tagged {
				tagged[c.name] = 1
			}
		case c.depth == d:
			tied[c.name]++
			if c.tagged {
				tagged[c.name]++
			}
		}
	}

	reported := map[string]bool{}
	for _, c := range cands {
		if c.depth != best[c.name] {
			continue // dominated by a shallower declaration: legally shadowed
		}
		if tied[c.name] > 1 {
			// encoding/json's tie-break: at equal depth, a SINGLE json-tagged
			// candidate still wins (the struct marshals fine).
			if tagged[c.name] == 1 {
				if !c.tagged {
					continue // the tagged sibling wins
				}
			} else {
				// The tie survives (all tagged or all untagged): encoding/json
				// drops every field of that name and the wire silently loses it.
				if !reported[c.name] {
					reported[c.name] = true
					s.errs = append(s.errs, fmt.Sprintf("duplicate json name %q", c.name))
				}
				continue
			}
		}
		s.fields = append(s.fields, c.name)
		s.verdicts[c.name] = apidoc.DefaultNullability(c.field)
		if c.hasCol {
			if s.cols == nil {
				s.cols = map[string]include.Column{}
			}
			s.cols[c.name] = c.col
			// The json keys "and" / "or" are the group keys of the JSON
			// filter format an adapter parses; a filterable column of that
			// name could never be addressed.
			if c.col.Filterable && (c.name == "and" || c.name == "or") {
				s.errs = append(s.errs, fmt.Sprintf("filterable column %q: the json keys \"and\" and \"or\" are reserved for filter groups", c.name))
			}
			if c.col.Sortable {
				if s.sortCols == nil {
					s.sortCols = map[string]string{}
				}
				s.sortCols[c.name] = c.col.Col
			}
		}
	}
	return s
}

func derefType(t reflect.Type) reflect.Type {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

// collectWireFields appends every serialized field of t (recursing through
// flattened embeds) to out, tagging each with its embedding depth.
func collectWireFields(t reflect.Type, depth int, inProgress map[reflect.Type]bool, out *[]wireField, errs *[]string) {
	if inProgress[t] {
		return // a self-embedding struct cannot terminate; stop the recursion
	}
	inProgress[t] = true
	defer delete(inProgress, t)

	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		tagName, _, _ := strings.Cut(tag, ",")
		elem := derefType(f.Type)
		embedded := f.Anonymous && tagName == "" && tag != "-" &&
			elem != nil && elem.Kind() == reflect.Struct

		// A sortCol tag on a field that is never serialized under its own name
		// is inert, and an inert declaration is a finding. Checked BEFORE the
		// embed recursion so an unexported embed is flagged too.
		//
		// TWO inert cases are NOT findings and are silently DROPPED: a field
		// tagged `json:"-"` and an UNTAGGED exported field. Both are skipped
		// further down before the whitelist is written, so their sortCol never
		// reaches SortCols and no error is raised. Neither is caught here
		// because the checks below key off the json tag, which those two cases
		// do not carry in a distinguishable form — a client :sort() naming such
		// a key simply misses the whitelist and falls back.
		if tagName, present := colTagName(f); present {
			switch {
			case !f.IsExported():
				*errs = append(*errs, fmt.Sprintf("%s tag on unexported field %s", tagName, f.Name))
			case embedded:
				*errs = append(*errs, fmt.Sprintf("%s tag on embedded field %s is inert (its fields are promoted)", tagName, f.Name))
			}
		}

		if embedded {
			// encoding/json promotes the exported fields of an embedded struct
			// even when the embedded type itself is unexported.
			collectWireFields(elem, depth+1, inProgress, out, errs)
			continue
		}
		if !f.IsExported() || tag == "-" {
			continue
		}

		key := tagName
		if key == "" {
			key = f.Name
		}
		// The whitelist is keyed by the json tag only: an untagged or
		// json:"-" field carries no binding, and its tag (well-formed or not)
		// is dropped without a finding, as sortCol always was.
		var col include.Column
		var hasCol bool
		if tagName != "" && tagName != "-" {
			var cerrs []string
			col, hasCol, cerrs = parseColTag(f)
			*errs = append(*errs, cerrs...)
		}
		*out = append(*out, wireField{
			name: key, depth: depth, tagged: tagName != "", col: col, hasCol: hasCol, field: f,
		})
	}
}

// colTagName reports which column tag a field carries — "col", or the legacy
// "sortCol" — and whether either is present at all (an empty value counts as
// present: it is a finding, not an absence).
func colTagName(f reflect.StructField) (string, bool) {
	if _, ok := f.Tag.Lookup("col"); ok {
		return "col", true
	}
	if _, ok := f.Tag.Lookup("sortCol"); ok {
		return "sortCol", true
	}
	return "", false
}

// filterableKinds is the closed set of wire field kinds a `filter` column may
// have — what a closed operator set can compare without knowing the dialect.
// time.Time is admitted by type, below.
var filterableKinds = map[reflect.Kind]bool{
	reflect.Bool: true, reflect.String: true,
	reflect.Int: true, reflect.Int8: true, reflect.Int16: true, reflect.Int32: true, reflect.Int64: true,
	reflect.Uint: true, reflect.Uint8: true, reflect.Uint16: true, reflect.Uint32: true, reflect.Uint64: true,
	reflect.Float32: true, reflect.Float64: true,
}

var timeType = reflect.TypeFor[time.Time]()

// parseColTag reads f's column binding: `col:"sql_name[,sort][,filter]"`, or
// the legacy `sortCol:"sql_name"` (≡ `col:"sql_name,sort"`). ok=false when
// the field carries neither. Findings are returned, not fatal: both tags on
// one field, an empty name, an unknown or repeated option, and `filter` on a
// field whose type the operator set cannot compare.
func parseColTag(f reflect.StructField) (c include.Column, ok bool, errs []string) {
	raw, hasCol := f.Tag.Lookup("col")
	legacy, hasLegacy := f.Tag.Lookup("sortCol")
	switch {
	case hasCol && hasLegacy:
		return c, false, []string{fmt.Sprintf("field %s carries both col and sortCol tags (declare one)", f.Name)}
	case hasLegacy:
		raw = legacy + ",sort"
	case !hasCol:
		return c, false, nil
	}
	name, opts, _ := strings.Cut(raw, ",")
	if name == "" {
		return c, false, []string{fmt.Sprintf("col tag on field %s has an empty column name", f.Name)}
	}
	c = include.Column{Col: name, Type: derefType(f.Type)}
	if opts != "" {
		for _, o := range strings.Split(opts, ",") {
			switch o {
			case "sort":
				if c.Sortable {
					errs = append(errs, fmt.Sprintf("col tag on field %s repeats option %q", f.Name, o))
				}
				c.Sortable = true
			case "filter":
				if c.Filterable {
					errs = append(errs, fmt.Sprintf("col tag on field %s repeats option %q", f.Name, o))
				}
				c.Filterable = true
			default:
				errs = append(errs, fmt.Sprintf("col tag on field %s has unknown option %q (sort, filter)", f.Name, o))
			}
		}
	}
	if c.Filterable && !filterableKinds[c.Type.Kind()] && c.Type != timeType {
		errs = append(errs, fmt.Sprintf("col tag on field %s: filter needs a bool, number, string or time.Time field (got %s)", f.Name, f.Type))
	}
	return c, true, errs
}
