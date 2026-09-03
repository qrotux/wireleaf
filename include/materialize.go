// materialize.go — level-batched (breadth-first) expansion of a resolved
// PlanNode tree over a root doc set, producing one ordered json.RawMessage per
// input doc. Expansion is SEQUENTIAL (no concurrency) and covers to-one,
// forward-hasMany, reverse and in-array edges.

package include

import (
	"encoding/json"
	"fmt"

	"github.com/qrotux/wireleaf/jsonsplice"
)

// Default per-edge top-N when the edge declares no limit.
const (
	defaultHasManyLimit = 20
	defaultReverseLimit = 20
	// Per-parent row estimate for an UNBOUNDED edge (Bare / in-array) that
	// declares no EstimatedRows; feeds the plan-time cost model only.
	defaultUnboundedEstimate = 100
)

// nullRaw is the shared literal-null edge value.
var nullRaw = json.RawMessage("null")

// Materialize breadth-first materializes one level (plan, docs, ctx) and returns
// one json.RawMessage per input doc, in the SAME order:
//
//   - plan.Resource.Enrich runs FIRST (a batch side-fetch hook), before serialize;
//   - each doc is serialized level-only (no child edges) into its scalar bytes;
//   - each child edge in plan.Children is resolved IN ORDER — the whole level is
//     batched (one FetchByIDs per forward edge; one FetchByParents per reverse
//     edge) — then attached onto each doc under the edge key via assembleObject.
//     The value is wrapped per `Edge.Envelope` as the LAST step —
//     `{"<Key>":obj}` / `{"<Key>":[…],"<Pagination>":{…}}` — after every policy
//     has been applied.
//     COMPUTED children are SKIPPED entirely: no fetch, and no key in the
//     output bytes (the application splices its own value in).
//   - an IN-ARRAY child is the exception to "attached under the edge key": its
//     targets are spliced INTO the parent's own scalar bytes, inside each
//     element of the parent's array field (Edge.ArrayPath), under Edge.SubField.
//     Such an edge contributes no top-level key of its own.
//
// The result is aligned 1:1 with docs (input order preserved). The plan tree is
// finite (ResolvePlan bounds it, incl. the default-cycle guard), so the recursion
// always terminates.
func Materialize(plan *PlanNode, docs []any, ctx *Ctx) ([]json.RawMessage, error) {
	// 0) Budget and cancellation, checked once per level BEFORE any work.
	if err := ctx.StdContext().Err(); err != nil {
		return nil, fmt.Errorf("include: materialize %q: %w", plan.Path, err)
	}
	if plan.Edge == nil { // root level: arm the budget for this hydrate call
		ctx.rows, ctx.maxRows = 0, plan.MaxRows
	}
	ctx.rows += len(docs)
	if ctx.maxRows > 0 && ctx.rows > ctx.maxRows {
		return nil, NewError(INCLUDE_BUDGET_EXCEEDED, fmt.Sprintf("%q: %d rows materialized, budget %d", plan.Path, ctx.rows, ctx.maxRows))
	}

	// 1) Enrich the whole level BEFORE serialize (may issue batch side-fetches).
	if err := plan.Resource.Enrich(docs, ctx); err != nil {
		return nil, err
	}

	// 2) Serialize this level (level-only; child edges attached below).
	scalar := make([]json.RawMessage, len(docs))
	for i, doc := range docs {
		wire := plan.Resource.Serialize(doc, ctx)
		b, err := ctx.marshal(wire)
		if err != nil {
			return nil, fmt.Errorf("include: serialize %s[%d]: %w", plan.Resource.Name(), i, err)
		}
		scalar[i] = b
	}

	// 3) Resolve each child edge across the WHOLE level, IN plan order. edgeVals[c]
	//    is the per-parent (per-doc) resolved value for child c, aligned with docs.
	//    COMPUTED children are dropped here: the engine never fetches or emits
	//    them (their key is absent from these bytes; the application splices its
	//    own value in afterwards).
	//    IN-ARRAY children are split off too: they rewrite the scalar bytes in
	//    place (below) instead of contributing a top-level key.
	children := make([]*PlanNode, 0, len(plan.Children))
	for _, child := range plan.Children {
		if child.Computed {
			continue
		}
		if child.Edge != nil && EdgeKind(*child.Edge) == KindInArray {
			if err := resolveInArray(child, docs, scalar, ctx); err != nil {
				return nil, err
			}
			continue
		}
		children = append(children, child)
	}

	edgeVals := make([][]json.RawMessage, len(children))
	for ci, child := range children {
		vals, err := resolveEdge(child, plan, docs, ctx)
		if err != nil {
			return nil, err
		}
		edgeVals[ci] = vals
	}

	// 4) Assemble: append each child's edge value onto every doc's scalar bytes,
	//    preserving order (scalar fields first, then edges in plan order).
	out := make([]json.RawMessage, len(docs))
	for i := range docs {
		if len(children) == 0 {
			out[i] = scalar[i]
			continue
		}
		kvs := make([]kv, len(children))
		for ci, child := range children {
			kvs[ci] = kv{Key: child.EdgeKey, Val: edgeVals[ci][i]}
		}
		out[i] = assembleObject(scalar[i], kvs)
	}
	return out, nil
}

// resolveEdge dispatches one child edge over the level to its per-parent values.
func resolveEdge(child *PlanNode, parent *PlanNode, docs []any, ctx *Ctx) ([]json.RawMessage, error) {
	switch EdgeKind(*child.Edge) {
	case KindToOne:
		return resolveToOne(child, docs, ctx)
	case KindForwardHasMany:
		return resolveForwardHasMany(child, docs, ctx)
	case KindReverse:
		return resolveReverse(child, parent, docs, ctx)
	default:
		// Includes KindInArray: Materialize splits in-array children off before
		// this dispatch (they rewrite the parent's scalar bytes, they do not
		// produce a per-parent edge value).
		return nil, fmt.Errorf("include: unexpected edge kind %q for %s", EdgeKind(*child.Edge), child.EdgeKey)
	}
}

// guardPasses reports whether child's guard admits this parent doc. A nil guard
// always passes.
func guardPasses(child *PlanNode, doc any, ctx *Ctx) bool {
	if child.Edge.Guard == nil {
		return true
	}
	return child.Edge.Guard(ctx, doc)
}

// missingRequiredFor returns the missing-required policy in force for child's
// inbound edge: the edge's own override, else the engine-wide Ctx.Policies
// fallback. Meaningful only when Edge.Required.
func missingRequiredFor(child *PlanNode, ctx *Ctx) MissingRequiredPolicy {
	if child.Edge.MissingRequired != nil {
		return *child.Edge.MissingRequired
	}
	return ctx.Policies.MissingRequired
}

// missingForeignFor returns the dangling-FK policy in force for child's
// inbound edge: the edge's own override, else the engine-wide Ctx.Policies
// fallback.
func missingForeignFor(child *PlanNode, ctx *Ctx) MissingForeignPolicy {
	if child.Edge.MissingForeign != nil {
		return *child.Edge.MissingForeign
	}
	return ctx.Policies.MissingForeign
}

// collectIDs runs the shared guard-then-collect pass every FORWARD edge kind
// starts with (to-one, forward-hasMany, in-array): iterate docs IN ORDER, check
// child's guard once per parent, pull that parent's target ids via fks, and
// dedupe them into one level-wide fetch union.
//
//   - guard FIRST: a guarded-out parent gets perParent[i] = nil and
//     guarded[i] = true, and fks is NOT called for it (its ids never reach the
//     DB); how "guarded" renders (null / empty node) is the CALLER's business —
//     guarded exists so callers can tell a guarded-out parent from one whose
//     fks legitimately came back empty;
//   - fks(i, doc) returns the parent's target ids in the order the caller wants
//     them consumed (to-one wraps its single FK in a 0/1-element slice; in-array
//     returns ARRAY ORDER); it may capture per-index state (forward-hasMany
//     records its hasMore probe through i);
//   - perParent[i] stores fks's result VERBATIM (aligned with docs) — the caller
//     rebuilds its per-parent output from it;
//   - union holds the distinct non-empty ids across the level in FIRST-SEEN
//     order ("" is never fetched — an empty id is a legitimate no-target, not a
//     DB id). It feeds ONE batch FetchByIDs via fetchAndIndex.
//
// A reverse edge runs its own variant inline (resolveReverse): it collects one
// parent id per doc via parent.Resource.IDOf — not FKs off the row — and its
// union deliberately keeps "" (the id list is echoed back by FetchByParents).
func collectIDs(child *PlanNode, docs []any, ctx *Ctx, fks func(i int, doc any) []string) (perParent [][]string, guarded []bool, union []string) {
	perParent = make([][]string, len(docs))
	guarded = make([]bool, len(docs))
	seen := make(map[string]bool)
	union = make([]string, 0, len(docs))
	for i, doc := range docs {
		if !guardPasses(child, doc, ctx) {
			guarded[i] = true
			continue
		}
		ids := fks(i, doc)
		perParent[i] = ids
		for _, id := range ids {
			if id != "" && !seen[id] {
				seen[id] = true
				union = append(union, id)
			}
		}
	}
	return perParent, guarded, union
}

// resolveToOne resolves a to-one forward edge across the level:
//
//   - guard FIRST: a guarded-out parent → null, contributes NO fk (no DB id);
//   - fk := child.Edge.ForeignKey(doc); "" → null (legitimate empty FK, no DB id);
//   - one batch FetchByIDs over the distinct non-empty fks; recurse+index by IDOf;
//   - per parent: null (guarded/empty) | idx[fk] | null (missing id = access-dropped).
//
// On a REQUIRED edge every one of those null paths is subject to the resolved
// RequiredMissing policy: under MissingRequiredError the request fails instead
// of emitting a null the published component forbids. The message names the
// edge, the parent and WHICH path produced it, because the three have different
// causes (no FK in the row / guarded out / the fetcher did not return the row).
func resolveToOne(child *PlanNode, docs []any, ctx *Ctx) ([]json.RawMessage, error) {
	// Per-parent fk as a 0/1-element slice (empty = null: empty FK; guarded-out
	// is flagged separately). collectIDs dedupes the distinct non-empty union.
	fkPerParent, guardedOut, ids := collectIDs(child, docs, ctx, func(_ int, doc any) []string {
		if child.Edge.ForeignKey == nil {
			return nil
		}
		if fk := child.Edge.ForeignKey(doc); fk != "" {
			return []string{fk}
		}
		return nil
	})

	idx, err := fetchAndIndex(child, ids, ctx)
	if err != nil {
		return nil, err
	}

	strictRequired := child.Edge.Required && missingRequiredFor(child, ctx) == MissingRequiredError
	strictForeign := missingForeignFor(child, ctx) == MissingForeignError
	env := child.Edge.Envelope

	out := make([]json.RawMessage, len(docs))
	for i, fks := range fkPerParent {
		if len(fks) == 0 {
			if strictRequired {
				return nil, missingRequiredErr(child, docs[i], guardedOut[i])
			}
			out[i] = wrapData(nullRaw, env)
			continue
		}
		fk := fks[0]
		if raw, ok := idx[fk]; ok {
			out[i] = wrapData(raw, env)
			continue
		}
		// A non-empty FK that resolved to nothing is a DANGLING reference: both
		// policies can object to it, so either one set to Error fails the
		// request (the messages differ — they name different contracts).
		if strictRequired {
			return nil, fmt.Errorf(
				"include: required edge %s resolved to no target: the fetcher for %s did not return id %q (orphaned FK, or a fetcher filtering a row the contract declares always present)",
				child.Path, child.Resource.Name(), fk)
		}
		if strictForeign {
			return nil, danglingFKErr(child, fk)
		}
		out[i] = wrapData(nullRaw, env) // missing id = access-dropped
	}
	return out, nil
}

// danglingFKErr reports a non-empty FK that the edge's fetcher did not return.
func danglingFKErr(child *PlanNode, fk string) error {
	return fmt.Errorf(
		"include: edge %s holds a dangling foreign key: the fetcher for %s did not return id %q (orphaned FK, or a fetcher filtering a row the parent still references)",
		child.Path, child.Resource.Name(), fk)
}

// missingRequiredErr reports an empty-FK / guarded-out null on a required edge,
// naming which of the two it was.
func missingRequiredErr(child *PlanNode, doc any, guarded bool) error {
	if guarded {
		return fmt.Errorf(
			"include: required edge %s resolved to no target: its Guard rejected the parent (a required edge must not be guarded)",
			child.Path)
	}
	return fmt.Errorf(
		"include: required edge %s resolved to no target: the parent row carries an empty FK",
		child.Path)
}

// resolveForwardHasMany resolves a parent-holds-FK-array edge across the level:
//
//   - guard FIRST: guarded-out parent → empty ({items:[],hasMore:false} / bare []);
//   - fks := child.Edge.ForeignKeys(doc); limit = the SAME resolved per-edge limit a
//     reverse edge gets (clamp(client :limit, 1, Edge.Limit)), 0 when Bare;
//   - hasMore = len(fks) > limit (never for bare); trim to limit unless bare;
//   - one batch FetchByIDs over the union of trimmed fks; recurse+index by IDOf;
//   - per parent: rebuild items in the parent's (trimmed) fk order, drop missing;
//   - envelope {items,hasMore}, or a flat array when bare (empty → [], never null).
func resolveForwardHasMany(child *PlanNode, docs []any, ctx *Ctx) ([]json.RawMessage, error) {
	bare := child.Edge.Bare
	limit := resolvedLimit(child, defaultHasManyLimit)
	env := child.Edge.Envelope

	// Per-parent trimmed id list (nil = guarded-out → empty) + hasMore probe.
	// The trim happens INSIDE the closure so only the kept window feeds the
	// fetch union; collectIDs dedupes it level-wide.
	hasMorePerParent := make([]bool, len(docs))
	trimmedPerParent, _, union := collectIDs(child, docs, ctx, func(i int, doc any) []string {
		var fks []string
		if child.Edge.ForeignKeys != nil {
			fks = child.Edge.ForeignKeys(doc)
		}
		if !bare && len(fks) > limit {
			hasMorePerParent[i] = true
			fks = fks[:limit]
		}
		return fks
	})

	idx, err := fetchAndIndex(child, union, ctx)
	if err != nil {
		return nil, err
	}

	strictForeign := missingForeignFor(child, ctx) == MissingForeignError

	out := make([]json.RawMessage, len(docs))
	for i, fks := range trimmedPerParent {
		// Rebuild items in the parent's (trimmed) fk order; drop ids the fetch
		// did not return (access-dropped / missing). Guarded-out (fks==nil) → empty.
		items := make([]json.RawMessage, 0, len(fks))
		for _, id := range fks {
			if raw, ok := idx[id]; ok {
				items = append(items, raw)
				continue
			}
			// A silently vanished list item is the same dangling reference a
			// to-one edge would render as null (an EMPTY id is not one).
			if id != "" && strictForeign {
				return nil, danglingFKErr(child, id)
			}
		}
		out[i] = marshalEdgeValue(wrapItems(items, env), hasMorePerParent[i], "", bare, env)
	}
	return out, nil
}

// resolveInArray resolves an IN-ARRAY edge across the level and rewrites the
// parents' scalar bytes IN PLACE (scalar is aligned with docs). Unlike every
// other edge kind it produces no top-level key: the hydrated targets live
// inside the parent's own array field.
//
//   - guard FIRST: a guarded-out parent contributes NO id to the batch and
//     every one of its elements gets `SubField: null` (its collected ids are
//     not even length-checked — they never reached the DB);
//   - ids are collected via the typed child.Edge.ForeignKeys(doc) in ARRAY ORDER;
//     they are deduped LEVEL-WIDE into ONE forward FetchByIDs (via
//     fetchAndIndex, which materializes the child plan over the fetched rows,
//     so nested includes under an in-array edge work);
//   - the stitch is byte-level: jsonsplice.Member locates the array member
//     (Edge.ArrayPath — the wire FIELD name, which need not equal the edge
//     key), jsonsplice.Elements splits it, each element is spliced under
//     Edge.SubField and the rebuilt array is spliced back. Order is preserved;
//     an element that ALREADY carries SubField has that value replaced IN
//     PLACE (the member keeps its position). Element interiors and every byte
//     outside the array member survive verbatim — the one normalization is the
//     array FRAME: inter-element whitespace is dropped when the array is
//     rebuilt (elements are re-emitted comma-separated);
//   - an ABSENT or null array member → the edge is skipped for that parent (no
//     error). A non-object element, or a collected id list whose length differs
//     from the wire array's, is a developer error and fails the request.
func resolveInArray(child *PlanNode, docs []any, scalar []json.RawMessage, ctx *Ctx) error {
	// Guard first, then collect the per-parent ids and the level-wide deduped union.
	fksPerParent, guarded, union := collectIDs(child, docs, ctx, func(_ int, doc any) []string {
		if child.Edge.ForeignKeys == nil {
			return nil
		}
		return child.Edge.ForeignKeys(doc)
	})

	idx, err := fetchAndIndex(child, union, ctx)
	if err != nil {
		return err
	}

	strictForeign := missingForeignFor(child, ctx) == MissingForeignError
	env := child.Edge.Envelope

	for i := range docs {
		arr, ok, err := jsonsplice.Member(scalar[i], child.Edge.ArrayPath)
		if err != nil {
			return fmt.Errorf("include: in-array %s: %w", child.EdgeKey, err)
		}
		if !ok || isJSONNull(arr) {
			continue // absent / null array member → edge skipped for this parent
		}
		els, err := jsonsplice.Elements(arr)
		if err != nil {
			return fmt.Errorf("include: in-array %s: %w", child.EdgeKey, err)
		}
		fks := fksPerParent[i]
		if !guarded[i] && len(fks) != len(els) {
			return fmt.Errorf("include: in-array length mismatch on %s: %d ids, %d elements",
				child.EdgeKey, len(fks), len(els))
		}

		spliced := make([]json.RawMessage, len(els))
		for j, el := range els {
			val := nullRaw
			if !guarded[i] {
				if raw, hit := idx[fks[j]]; hit {
					val = raw // missing id = access-dropped → stays null
				} else if fks[j] != "" && strictForeign {
					return danglingFKErr(child, fks[j])
				}
			}
			b, err := jsonsplice.Splice(el, child.Edge.SubField, wrapData(val, env))
			if err != nil {
				return fmt.Errorf("include: in-array %s element %d: %w", child.EdgeKey, j, err)
			}
			spliced[j] = b
		}

		newArr := marshalFlatArray(spliced)
		out, err := jsonsplice.Splice(scalar[i], child.Edge.ArrayPath, newArr)
		if err != nil {
			return fmt.Errorf("include: in-array %s: %w", child.EdgeKey, err)
		}
		scalar[i] = out
	}
	return nil
}

// isJSONNull reports whether v is the literal `null` (jsonsplice hands back a
// value span with no surrounding whitespace).
func isJSONNull(v []byte) bool { return string(v) == "null" }

// resolveReverse resolves a reverse (FK-on-child) edge across the WHOLE level
// with a SINGLE batched fetch:
//
//   - guard FIRST: a guarded-out parent → EMPTY node (contributes NO parent id,
//     no DB): enveloped {items:[],hasMore:false}; bare []. This is the SAME
//     empty shape a guard-pass with zero rows yields (a reverse edge is never
//     null). Only to-one returns null on guard-false.
//   - the guard-passing parents' ids (parent.Resource.IDOf(doc)) are collected
//     in DOC ORDER and deduped, preserving first occurrence;
//   - the resolved EdgeQuery (limit / sort / remaining args) plus that id list
//     go to ONE FetchByParents call for the level (skipped entirely when no
//     parent passed the guard);
//   - the result map is read per requested id: an absent parent is an empty
//     collection, keys that were never requested are dropped, and a parent's
//     rows are truncated to q.Limit defensively (Limit > 0 only);
//   - every parent's rows are materialized in ONE recursive level call, so the
//     grandchildren stay batched too;
//   - envelope {items,hasMore(,nextCursor)}, or a bare flat array — a bare edge
//     ignores HasMore and NextCursor entirely.
func resolveReverse(child *PlanNode, parent *PlanNode, docs []any, ctx *Ctx) ([]json.RawMessage, error) {
	bare := child.Edge.Bare
	env := child.Edge.Envelope
	q := EdgeQuery{
		Limit: resolvedLimit(child, defaultReverseLimit),
		Sort:  reverseSortKey(child),
		Args:  edgeQueryArgs(child),
		Edge:  EdgeRef{Parent: parent.Resource.Name(), Key: child.EdgeKey},
	}

	// Guard first: per-doc parent id ("" = guarded-out → empty node), plus the
	// deduped request list in doc order.
	idPerDoc := make([]string, len(docs))
	guarded := make([]bool, len(docs))
	seen := make(map[string]bool, len(docs))
	parentIDs := make([]string, 0, len(docs))
	for i, doc := range docs {
		if !guardPasses(child, doc, ctx) {
			guarded[i] = true
			continue
		}
		id := parent.Resource.IDOf(doc)
		idPerDoc[i] = id
		if !seen[id] {
			seen[id] = true
			parentIDs = append(parentIDs, id)
		}
	}

	// One fetch for the level. No guard-passing parent → no DB call at all.
	strictContract := ctx.Policies.FetcherContract == FetcherContractStrict
	byParent := map[string]ParentRows{}
	if len(parentIDs) > 0 {
		fetch, ok := reverseFetcher(ctx.Registry, parent.Resource, child.EdgeKey, child.Resource)
		if !ok {
			return nil, fmt.Errorf("include: no FetchByParents registered for edge %s (neither on the edge nor on node %s)", q.Edge, child.Resource.Name())
		}
		got, err := fetch(ctx, parentIDs, q)
		if err != nil {
			return nil, err
		}
		if got != nil {
			byParent = got
		}
		// Contract: only requested parent ids may come back. Tolerant mode
		// simply never reads the extras (dropped below); strict mode names
		// them.
		if strictContract {
			for id := range byParent {
				if !seen[id] {
					return nil, fmt.Errorf(
						"include: fetcher contract violated on edge %s: FetchByParents for %s returned parent id %q that was not requested",
						child.Path, child.Resource.Name(), id)
				}
			}
		}
	}

	// Flatten the requested parents' rows into ONE slice (recording each
	// parent's window) so the child level is materialized in a single call.
	type window struct {
		start, n   int
		hasMore    bool
		nextCursor string
	}
	windows := make(map[string]window, len(parentIDs))
	rows := make([]any, 0, len(parentIDs))
	for _, id := range parentIDs {
		pr, ok := byParent[id] // an absent parent → empty collection
		if !ok {
			windows[id] = window{start: len(rows)}
			continue
		}
		got := pr.Rows
		if q.Limit > 0 && len(got) > q.Limit {
			// Probe contract: "select limit+1, return at most limit rows".
			if strictContract {
				return nil, fmt.Errorf(
					"include: fetcher contract violated on edge %s: FetchByParents for %s returned %d rows for parent %q, limit is %d (the probe contract returns at most limit rows and sets HasMore)",
					child.Path, child.Resource.Name(), len(got), id, q.Limit)
			}
			got = got[:q.Limit] // defensive truncation: the fetcher over-returned
		}
		if q.Limit == 0 && pr.HasMore && strictContract {
			// A bare edge fetches ALL rows: nothing can exist beyond them.
			return nil, fmt.Errorf(
				"include: fetcher contract violated on edge %s: FetchByParents for %s set HasMore for parent %q on a bare edge (Limit 0 = fetch-all, HasMore MUST be false)",
				child.Path, child.Resource.Name(), id)
		}
		windows[id] = window{start: len(rows), n: len(got), hasMore: pr.HasMore, nextCursor: pr.NextCursor}
		rows = append(rows, got...)
	}
	// Keys the engine never asked for are simply never read — dropped.

	items, err := Materialize(child, rows, ctx)
	if err != nil {
		return nil, err
	}

	out := make([]json.RawMessage, len(docs))
	for i := range docs {
		var w window
		if !guarded[i] {
			w = windows[idPerDoc[i]]
		}
		slice := items[w.start : w.start+w.n]
		// Bare: HasMore/NextCursor ignored by marshalEdgeValue.
		out[i] = marshalEdgeValue(wrapItems(slice, env), w.hasMore, w.nextCursor, bare, env)
	}
	return out, nil
}

// resolvedLimit resolves the effective per-parent top-N for a to-many edge:
// bare → 0 (fetch-all); otherwise clamp(client :limit, 1, ceiling) where the
// ceiling is Edge.Limit (def when the edge declares none). The client value is
// the int coerced at plan time (validateArgs); anything else is ignored.
func resolvedLimit(child *PlanNode, def int) int {
	if child.Edge.Bare {
		return 0
	}
	ceiling := child.Edge.Limit
	if ceiling <= 0 {
		ceiling = def
	}
	n, ok := child.Args["limit"].(int)
	if !ok {
		return ceiling
	}
	return min(max(n, 1), ceiling)
}

// edgeQueryArgs returns the plan args MINUS the built-ins ("limit", "sort"),
// which are already resolved into EdgeQuery.Limit/Sort. nil when nothing remains.
func edgeQueryArgs(child *PlanNode) map[string]any {
	var out map[string]any
	for k, v := range child.Args {
		if k == "limit" || k == "sort" {
			continue
		}
		if out == nil {
			out = make(map[string]any, len(child.Args))
		}
		out[k] = v
	}
	return out
}

// fetchAndIndex runs ONE batch FetchByIDs for child.Resource over ids and returns
// a map id → serialized child bytes (id via child.Resource.IDOf on each row). An
// empty id set skips the fetch entirely (empty map). Errors if no fetcher is
// registered for the resource.
func fetchAndIndex(child *PlanNode, ids []string, ctx *Ctx) (map[string]json.RawMessage, error) {
	if len(ids) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	fetch, ok := ctx.Registry.FetchByIDs(child.Resource)
	if !ok {
		return nil, fmt.Errorf("include: no FetchByIDs registered for %s (edge %s)", child.Resource.Name(), child.EdgeKey)
	}
	rows, err := fetch(ctx, ids)
	if err != nil {
		return nil, err
	}
	// Contract: only requested ids may come back. Tolerant mode leaves the
	// extras sitting unread in the index; strict mode names them.
	if ctx.Policies.FetcherContract == FetcherContractStrict {
		requested := make(map[string]bool, len(ids))
		for _, id := range ids {
			requested[id] = true
		}
		for _, row := range rows {
			if id := child.Resource.IDOf(row); !requested[id] {
				return nil, fmt.Errorf(
					"include: fetcher contract violated on edge %s: FetchIDs for %s returned row id %q that was not requested",
					child.Path, child.Resource.Name(), id)
			}
		}
	}
	serialized, err := Materialize(child, rows, ctx)
	if err != nil {
		return nil, err
	}
	idx := make(map[string]json.RawMessage, len(rows))
	for j, row := range rows {
		idx[child.Resource.IDOf(row)] = serialized[j]
	}
	return idx, nil
}

// marshalEnvelope builds `{"items":[...],"hasMore":bool}` from pre-serialized
// item bytes (byte-exact; items already JSON fragments), appending
// `,"nextCursor":"…"` ONLY when the fetcher set a non-empty cursor. The cursor
// is opaque: it is copied through as-is, merely JSON-string-quoted.
func marshalEnvelope(items []json.RawMessage, hasMore bool, nextCursor string) json.RawMessage {
	out := make([]byte, 0, 32+len(nextCursor)+jsonLen(items))
	out = append(out, `{"items":`...)
	out = appendJSONArray(out, items)
	out = append(out, `,"hasMore":`...)
	if hasMore {
		out = append(out, "true"...)
	} else {
		out = append(out, "false"...)
	}
	if nextCursor != "" {
		out = append(out, `,"nextCursor":`...)
		out = appendJSONString(out, nextCursor)
	}
	out = append(out, '}')
	return out
}

// wrapData wraps ONE node value (an object, or the literal null) as
// `{"<Key>":<raw>}` under env; the plain style returns raw untouched (no
// copy). An empty raw is treated as null. The key is inserted verbatim:
// graph.Compile guarantees it needs no JSON escaping.
func wrapData(raw json.RawMessage, env Envelope) json.RawMessage {
	if env.Plain() {
		return raw
	}
	if len(raw) == 0 {
		raw = nullRaw
	}
	out := make([]byte, 0, len(env.Key)+len(raw)+5)
	out = append(out, '{', '"')
	out = append(out, env.Key...)
	out = append(out, '"', ':')
	out = append(out, raw...)
	out = append(out, '}')
	return out
}

// wrapItems applies wrapData to every list element. The plain style returns
// the input slice itself.
func wrapItems(items []json.RawMessage, env Envelope) []json.RawMessage {
	if env.Plain() {
		return items
	}
	out := make([]json.RawMessage, len(items))
	for i, it := range items {
		out[i] = wrapData(it, env)
	}
	return out
}

// marshalEdgeValue builds a to-many edge VALUE from pre-serialized (already
// wrapped, if the style wraps) item bytes:
//
//   - plain:   `{"items":[…],"hasMore":b(,"nextCursor":"…")}`, or a flat
//     `[…]` when bare (marshalEnvelope / marshalFlatArray, unchanged);
//   - wrapped: `{"<Key>":[…]}`, plus `,"<Pagination>":{"hasNextPage":b
//     (,"nextCursor":"…")}` ONLY when the edge is not bare AND env.Pagination
//     is set. A wrapped list is never null and never a bare array.
func marshalEdgeValue(items []json.RawMessage, hasMore bool, nextCursor string, bare bool, env Envelope) json.RawMessage {
	if env.Plain() {
		if bare {
			return marshalFlatArray(items)
		}
		return marshalEnvelope(items, hasMore, nextCursor)
	}
	out := make([]byte, 0, 48+len(env.Key)+len(env.Pagination)+len(nextCursor)+jsonLen(items))
	out = append(out, '{', '"')
	out = append(out, env.Key...)
	out = append(out, '"', ':')
	out = appendJSONArray(out, items)
	if !bare && env.Pagination != "" {
		out = append(out, ',', '"')
		out = append(out, env.Pagination...)
		out = append(out, `":{"hasNextPage":`...)
		if hasMore {
			out = append(out, "true"...)
		} else {
			out = append(out, "false"...)
		}
		if nextCursor != "" {
			out = append(out, `,"nextCursor":`...)
			out = appendJSONString(out, nextCursor)
		}
		out = append(out, '}')
	}
	out = append(out, '}')
	return out
}

// appendJSONString appends s as a JSON string literal (no HTML escaping — the
// cursor is opaque and must survive the round trip byte-for-byte).
func appendJSONString(dst []byte, s string) []byte {
	b, err := MarshalNoEscape(s)
	if err != nil {
		// A string never fails to marshal; degrade to the empty literal.
		return append(dst, `""`...)
	}
	return append(dst, b...)
}

// marshalFlatArray builds a bare `[...]` from pre-serialized item bytes (empty → `[]`).
func marshalFlatArray(items []json.RawMessage) json.RawMessage {
	out := make([]byte, 0, 2+jsonLen(items))
	return appendJSONArray(out, items)
}

// appendJSONArray appends `[i0,i1,...]` of raw JSON fragments onto dst.
func appendJSONArray(dst []byte, items []json.RawMessage) []byte {
	dst = append(dst, '[')
	for i, it := range items {
		if i > 0 {
			dst = append(dst, ',')
		}
		dst = append(dst, it...)
	}
	dst = append(dst, ']')
	return dst
}

// jsonLen sums the byte lengths of items (+ commas) for buffer pre-sizing.
func jsonLen(items []json.RawMessage) int {
	n := 0
	for _, it := range items {
		n += len(it) + 1
	}
	return n
}
