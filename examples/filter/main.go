// Command filter shows the two pieces the core deliberately does NOT own.
//
// `include` carries the filter MODEL — a sealed AST (FilterAnd / FilterOr /
// FilterCond) and ResolveFilter, which checks it against the compiled graph —
// but no syntax and no SQL. A syntax is a product decision and SQL is a
// dialect; both belong to the application. This file is the reference for
// them: parseWhere turns a JSON `where` body into an include.Filter, and
// render turns the ResolvedFilter that comes back into SQL text.
//
// It PRINTS the SQL instead of executing it, so the three EXISTS templates of
// docs/include.md — any / all / none — and the deny-by-default rules are
// exercised without a database. Every SQL name in the output comes from the
// graph binding (tables, joins, Column.Col) and never from the client's text;
// values leave as $n placeholders in an args slice.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/qrotux/wireleaf/graph"
	"github.com/qrotux/wireleaf/include"
)

// ------------------------------------------------------------------ the graph

type authorRow struct {
	ID   string
	Name string
	Age  int
}

type bookRow struct {
	ID       string
	Title    string
	AuthorID string
	Year     int
}

type reviewRow struct {
	ID     string
	BookID string
	Rating int
	Text   string
}

type AuthorWire struct {
	ID   string `json:"id" col:"id,filter"`
	Name string `json:"name" col:"name,filter"`
	Age  int    `json:"age" col:"age,filter"`
}

type BookWire struct {
	ID    string `json:"id" col:"id,filter"`
	Title string `json:"title" col:"title,sort,filter"`
	Year  int    `json:"year" col:"year,filter"`
}

// Text is BOUND (the adapter knows its column) but not filterable: a `col`
// tag without the `filter` option is a projection binding and nothing more.
// Case 7 below is what a client gets for naming it.
type ReviewWire struct {
	ID     string `json:"id" col:"id,filter"`
	Rating int    `json:"rating" col:"rating,filter"`
	Text   string `json:"text" col:"text"`
}

func buildGraph() (*graph.Graph, include.Resource) {
	b := graph.NewBuilder()

	author := graph.Node[authorRow, AuthorWire](b, "Author").
		Wire(func(r authorRow, _ *include.Ctx) AuthorWire {
			return AuthorWire{ID: r.ID, Name: r.Name, Age: r.Age}
		}).
		PrimaryKey(func(r authorRow) string { return r.ID })
	book := graph.Node[bookRow, BookWire](b, "Book").
		Wire(func(r bookRow, _ *include.Ctx) BookWire {
			return BookWire{ID: r.ID, Title: r.Title, Year: r.Year}
		}).
		PrimaryKey(func(r bookRow) string { return r.ID })
	graph.Node[reviewRow, ReviewWire](b, "Review").
		Wire(func(r reviewRow, _ *include.Ctx) ReviewWire {
			return ReviewWire{ID: r.ID, Rating: r.Rating, Text: r.Text}
		}).
		PrimaryKey(func(r reviewRow) string { return r.ID })

	// Filterable is independent of Includable: `author` is both, `reviews`
	// and `books` are filter-only — a client may filter through them and can
	// never ?include= them.
	book.Edge("author", graph.ToOne[AuthorWire]()).
		ForeignKey(func(r bookRow) string { return r.AuthorID }).
		Filterable().
		Includable()
	book.Edge("reviews", graph.Reverse[ReviewWire]("bookId")).Filterable()
	author.Edge("books", graph.Reverse[BookWire]("authorId")).Filterable()

	// Only the includable edge needs a fetcher; a filterable edge is SQL the
	// adapter builds and never calls a fetcher. This example fetches nothing.
	graph.FetchIDs(b, author, func(_ *include.Ctx, _ []string) ([]authorRow, error) { return nil, nil })

	b.Root(book)
	g, err := b.Compile()
	if err != nil {
		panic(err)
	}
	return g, g.Resource("Book")
}

// ------------------------------------------------------------------ the parser

// ops is the closed operator set the JSON grammar admits, mirroring the
// include.Op* constants. A word outside it is a client error, not a column.
var ops = map[string]include.FilterOp{
	"eq": include.OpEq, "ne": include.OpNe,
	"lt": include.OpLt, "lte": include.OpLte,
	"gt": include.OpGt, "gte": include.OpGte,
	"in": include.OpIn, "nin": include.OpNin,
}

// parseWhere turns `{"where": <node>}` into an include.Filter.
//
// Node grammar (one key per object, always):
//
//	{"and": [<node>, ...]}          {"or": [<node>, ...]}
//	{"<dotted.path>": {"<op>": <value>}}
//
// The dotted path is edge keys followed by the field. Quantifier suffixes are
// the spelling docs/include.md recommends: none = any, `*` = all, `~` = none;
// the suffix sits on the SEGMENT when the path crosses several to-many hops,
// and on the FIELD when it crosses exactly one.
//
// The field-suffix sugar is only unambiguous with the graph in hand, so the
// parser takes the compiled root and walks Edges() to learn which segments are
// Many. That is a legitimate use of the graph: it is a syntax decision the
// application owns, made against the same declaration ResolveFilter checks.
// Everything else stays ResolveFilter's job — an unknown edge key is left
// alone here and reported there, with a bounded path.
func parseWhere(root include.Resource, body []byte) (include.Filter, error) {
	var outer map[string]json.RawMessage
	if err := json.Unmarshal(body, &outer); err != nil {
		return nil, fmt.Errorf("body is not a JSON object: %w", err)
	}
	raw, ok := outer["where"]
	if !ok {
		return nil, errors.New(`body has no "where" key`)
	}
	return parseNode(root, raw)
}

func parseNode(root include.Resource, raw json.RawMessage) (include.Filter, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("filter node is not a JSON object: %w", err)
	}
	if len(obj) != 1 {
		// This is what forbids {"and": [...], "title": {...}}: a group key and
		// a field key in one object have no defined precedence.
		return nil, fmt.Errorf("filter node must have exactly one key, got %d (%s)", len(obj), strings.Join(clipAll(sortedKeys(obj)), ", "))
	}
	key := sortedKeys(obj)[0]
	if key == "and" || key == "or" {
		var items []json.RawMessage
		if err := json.Unmarshal(obj[key], &items); err != nil {
			return nil, fmt.Errorf("%q must hold an array of filter nodes", clip(key))
		}
		members := make([]include.Filter, 0, len(items))
		for i, it := range items {
			m, err := parseNode(root, it)
			if err != nil {
				return nil, fmt.Errorf("%s[%d]: %w", clip(key), i, err)
			}
			members = append(members, m)
		}
		if key == "and" {
			return include.FilterAnd(members), nil
		}
		return include.FilterOr(members), nil
	}
	return parseCond(root, key, obj[key])
}

func parseCond(root include.Resource, path string, raw json.RawMessage) (include.Filter, error) {
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("%q: value must be an object {\"<op>\": <value>}", clip(path))
	}
	if len(body) != 1 {
		return nil, fmt.Errorf("%q: exactly one operator expected, got %d (%s)", clip(path), len(body), strings.Join(clipAll(sortedKeys(body)), ", "))
	}
	opKey := sortedKeys(body)[0]
	op, ok := ops[opKey]
	if !ok {
		return nil, fmt.Errorf("%q: unknown operator %q", clip(path), clip(opKey))
	}
	// The value passes through exactly as encoding/json decoded it —
	// float64, string, bool, []any, nil. No coercion: the adapter binds it by
	// Column.Type, and the engine judges the OPERATOR, never the value. A
	// JSON number therefore arrives as a float64 even for an int column; a
	// real adapter converts it by Column.Type, this printer does not.
	var value any
	if err := json.Unmarshal(body[opKey], &value); err != nil {
		return nil, fmt.Errorf("%q: %w", clip(path), err)
	}

	segs := strings.Split(path, ".")
	field, quant := splitQuant(segs[len(segs)-1])
	steps := make([]include.FilterStep, 0, len(segs)-1)
	for _, s := range segs[:len(segs)-1] {
		key, q := splitQuant(s)
		steps = append(steps, include.FilterStep{Key: key, Quant: q})
	}

	// Walk the declared edges to classify each segment. A key the graph does
	// not know stops the walk: the rest is unknowable here, and ResolveFilter
	// is the one that reports it.
	many := make([]int, 0, len(steps))
	cur := root
	known := true
	for i, st := range steps {
		e, ok := cur.Edges()[st.Key]
		if !ok || e.Target == nil {
			known = false
			break
		}
		if e.Many {
			many = append(many, i)
		}
		cur = e.Target()
		if cur == nil {
			known = false
			break
		}
	}

	if quant != "" && known {
		// A suffix on the field names the path's single to-many segment.
		if len(many) != 1 {
			return nil, fmt.Errorf("%q: a quantifier suffix on the field needs exactly one to-many segment in the path, found %d — put the suffix on the segment instead", clip(path), len(many))
		}
		if steps[many[0]].Quant != "" {
			return nil, fmt.Errorf("%q: quantifier given twice, on the field and on segment %q", clip(path), clip(steps[many[0]].Key))
		}
		steps[many[0]].Quant = quant
	} else if quant != "" && !known {
		return nil, fmt.Errorf("%q: a quantifier suffix on the field needs a known path", clip(path))
	}
	// A to-many segment nobody quantified is `any`, the no-suffix default.
	if known {
		for _, i := range many {
			if steps[i].Quant == "" {
				steps[i].Quant = include.QuantAny
			}
		}
	}
	if len(steps) == 0 {
		steps = nil
	}
	return include.FilterCond{Path: steps, Field: field, Op: op, Value: value}, nil
}

// clip bounds client text echoed in a parser error at 16 bytes plus "…", the
// same rule the core's clientEcho applies: an error body must not be sized by
// the client. The cut lands on a rune boundary.
func clip(s string) string {
	const max = 16
	if len(s) <= max {
		return s
	}
	end := 0
	for i := range s {
		if i > max {
			break
		}
		end = i
	}
	return s[:end] + "…"
}

// clipAll clips each key of a list for an error message.
func clipAll(keys []string) []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = clip(k)
	}
	return out
}

// splitQuant strips a trailing `*` (all) or `~` (none) from one path segment.
func splitQuant(s string) (string, include.Quant) {
	switch {
	case strings.HasSuffix(s, "*"):
		return strings.TrimSuffix(s, "*"), include.QuantAll
	case strings.HasSuffix(s, "~"):
		return strings.TrimSuffix(s, "~"), include.QuantNone
	}
	return s, ""
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ------------------------------------------------------------------ the bindings

// joinSpec is the SQL behind one hop: child.ChildCol = parent.ParentCol.
// include.Edge deliberately carries no foreign-key column name (Backref is a
// wire-side name, never a column), so the adapter binds it by (From, Key).
type joinSpec struct {
	ParentCol string
	ChildCol  string
}

var tables = map[string]string{
	"Author": "authors",
	"Book":   "books",
	"Review": "reviews",
}

var joins = map[[2]string]joinSpec{
	{"Book", "author"}:  {ParentCol: "author_id", ChildCol: "id"},
	{"Book", "reviews"}: {ParentCol: "id", ChildCol: "book_id"},
	{"Author", "books"}: {ParentCol: "id", ChildCol: "author_id"},
}

// checkBindings is the start-up check docs/include.md prescribes: Compile
// validates the graph, not the SQL bindings behind it, so a filterable edge or
// column with no binding must fail loudly here rather than at the first
// request that names it.
func checkBindings(g *graph.Graph) error {
	var problems []string
	seen := map[string]bool{}
	var walk func(res include.Resource)
	walk = func(res include.Resource) {
		if res == nil || seen[res.Name()] {
			return
		}
		seen[res.Name()] = true
		if tables[res.Name()] == "" {
			problems = append(problems, "no table bound for node "+res.Name())
		}
		cols := include.ColumnsOf(res)
		for _, k := range sortedKeys(cols) {
			if cols[k].Filterable && cols[k].Col == "" {
				problems = append(problems, "no column bound for "+res.Name()+"."+k)
			}
		}
		edges := res.Edges()
		for _, k := range sortedKeys(edges) {
			e := edges[k]
			if !e.Filterable || e.Target == nil {
				continue
			}
			if _, ok := joins[[2]string{res.Name(), k}]; !ok {
				problems = append(problems, "no join bound for "+res.Name()+"."+k)
			}
			walk(e.Target())
		}
	}
	for _, r := range g.Roots() {
		walk(r)
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// ------------------------------------------------------------------ the renderer

var sqlOps = map[include.FilterOp]string{
	include.OpEq: "=", include.OpNe: "<>",
	include.OpLt: "<", include.OpLte: "<=",
	include.OpGt: ">", include.OpGte: ">=",
	include.OpIn: "IN", include.OpNin: "NOT IN",
}

// scope is one FROM list: the outer query, or the body of one EXISTS. A to-one
// hop joins into the scope it is reached in — a join inside a subquery cannot
// be hoisted out of it — and repeated hops share an alias within that scope.
type scope struct {
	outer   bool // the top-level FROM list, as opposed to an EXISTS body
	joins   []string
	aliasOf map[string]string
}

type renderer struct {
	n    int
	args []any
}

func (r *renderer) alias() string {
	a := "t" + strconv.Itoa(r.n)
	r.n++
	return a
}

func newScope(outer bool) *scope { return &scope{outer: outer, aliasOf: map[string]string{}} }

// render prints the whole statement for a resolved filter. It never executes
// anything: the point is to read the SQL the templates produce.
func render(root include.Resource, f include.ResolvedFilter) (string, []any, error) {
	if f == nil {
		return "", nil, errors.New("nil filter")
	}
	r := &renderer{}
	sc := newScope(true)
	rootAlias := r.alias()
	expr, err := r.node(f, sc, rootAlias, "")
	if err != nil {
		return "", nil, err
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "SELECT %s.* FROM %s %s", rootAlias, tables[root.Name()], rootAlias)
	for _, j := range sc.joins {
		sb.WriteString(" " + j)
	}
	sb.WriteString(" WHERE " + expr)
	return sb.String(), r.args, nil
}

func (r *renderer) node(f include.ResolvedFilter, sc *scope, alias, path string) (string, error) {
	switch n := f.(type) {
	case include.ResolvedAnd:
		return r.group(n, sc, alias, path, " AND ")
	case include.ResolvedOr:
		return r.group(n, sc, alias, path, " OR ")
	case include.ResolvedCond:
		return r.hops(n, n.Hops, sc, alias, path)
	}
	// ResolveFilter returns the three concrete types only.
	return "", fmt.Errorf("unrecognized resolved node %T", f)
}

func (r *renderer) group(members []include.ResolvedFilter, sc *scope, alias, path, sep string) (string, error) {
	parts := make([]string, 0, len(members))
	for _, m := range members {
		s, err := r.node(m, sc, alias, path)
		if err != nil {
			return "", err
		}
		parts = append(parts, s)
	}
	return "(" + strings.Join(parts, sep) + ")", nil
}

// hops renders the remaining hops of one condition, then its leaf. A to-one
// hop becomes a LEFT JOIN in the outer scope and an inner join inside an
// EXISTS body; a to-many hop becomes one of the three correlated templates of
// docs/include.md, with the rest of the condition standing in for `cond`.
func (r *renderer) hops(c include.ResolvedCond, hops []include.FilterHop, sc *scope, alias, path string) (string, error) {
	if len(hops) == 0 {
		return r.leaf(c, alias)
	}
	h := hops[0]
	spec, ok := joins[[2]string{h.From.Name(), h.Key}]
	if !ok {
		return "", fmt.Errorf("no join bound for %s.%s", h.From.Name(), h.Key)
	}
	table := tables[h.To.Name()]
	if table == "" {
		return "", fmt.Errorf("no table bound for node %s", h.To.Name())
	}
	key := path + "." + h.Key

	if h.Quant == "" {
		// To-one: one row, nothing to quantify; two conditions through the
		// same hop in the same scope share the join. In the OUTER FROM the
		// join must be a LEFT JOIN: an inner one would drop a root row whose
		// FK is null or dangling even when another OR branch holds for it.
		// With the LEFT JOIN, a missing target makes the leaf NULL — not
		// true — under AND / OR, and a root whose to-one target is missing
		// falls through to vacuous truth under `all`, because the correlated
		// body of the EXISTS below it is then empty: the empty-relation
		// reading. Inside an EXISTS body a plain JOIN is right: a child that
		// fails to join is neither a match nor a violation, exactly as the
		// doc says.
		kw := "JOIN"
		if sc.outer {
			kw = "LEFT JOIN"
		}
		a, ok := sc.aliasOf[key]
		if !ok {
			a = r.alias()
			sc.aliasOf[key] = a
			sc.joins = append(sc.joins, fmt.Sprintf("%s %s %s ON %s.%s = %s.%s", kw, table, a, a, spec.ChildCol, alias, spec.ParentCol))
		}
		return r.hops(c, hops[1:], sc, a, key)
	}

	a := r.alias()
	// The graph in this file cannot reach a to-one hop inside an EXISTS, but
	// this is the scope that would carry its join into the subquery's FROM.
	sub := newScope(false)
	inner, err := r.hops(c, hops[1:], sub, a, key)
	if err != nil {
		return "", err
	}
	var body strings.Builder
	fmt.Fprintf(&body, "SELECT 1 FROM %s %s", table, a)
	for _, j := range sub.joins {
		body.WriteString(" " + j)
	}
	fmt.Fprintf(&body, " WHERE %s.%s = %s.%s AND ", a, spec.ChildCol, alias, spec.ParentCol)
	switch h.Quant {
	case include.QuantAny:
		return "EXISTS (" + body.String() + inner + ")", nil
	case include.QuantAll:
		return "NOT EXISTS (" + body.String() + "NOT (" + inner + "))", nil
	case include.QuantNone:
		return "NOT EXISTS (" + body.String() + inner + ")", nil
	}
	return "", fmt.Errorf("unknown quantifier %q", h.Quant)
}

// leaf renders one comparison. Everything SQL-side here comes from the graph
// binding — the alias the adapter allocated, Column.Col from a struct tag, and
// the operator from the closed FilterOp set — and NOTHING from the client's
// text; the value leaves as a $n placeholder.
func (r *renderer) leaf(c include.ResolvedCond, alias string) (string, error) {
	op, ok := sqlOps[c.Op]
	if !ok {
		return "", fmt.Errorf("unknown operator %q", c.Op)
	}
	if c.Op == include.OpIn || c.Op == include.OpNin {
		list, ok := c.Value.([]any)
		if !ok {
			return "", fmt.Errorf("%s.%s: %s needs a list value, got %T", alias, c.Column.Col, c.Op, c.Value)
		}
		if len(list) == 0 {
			return "", fmt.Errorf("%s.%s: %s needs a non-empty list", alias, c.Column.Col, c.Op)
		}
		ph := make([]string, 0, len(list))
		for _, v := range list {
			r.args = append(r.args, v)
			ph = append(ph, "$"+strconv.Itoa(len(r.args)))
		}
		return fmt.Sprintf("%s.%s %s (%s)", alias, c.Column.Col, op, strings.Join(ph, ", ")), nil
	}
	r.args = append(r.args, c.Value)
	return fmt.Sprintf("%s.%s %s $%d", alias, c.Column.Col, op, len(r.args)), nil
}

// ------------------------------------------------------------------ main

func main() {
	g, bookRes := buildGraph()
	if err := checkBindings(g); err != nil {
		panic("filter bindings incomplete: " + err.Error())
	}

	bodies := []string{
		`{"where":{"title":{"eq":"Dune"}}}`,
		`{"where":{"author.name":{"eq":"Herbert"}}}`,
		`{"where":{"reviews.rating":{"gt":4}}}`,
		`{"where":{"reviews.rating*":{"gt":4}}}`,
		`{"where":{"or":[{"reviews.rating~":{"lt":2}},{"and":[{"year":{"gte":1960}},{"author.age":{"in":[40,50]}}]}]}}`,
		`{"where":{"author.books*.year":{"lt":1950}}}`,
		`{"where":{"reviews.text":{"eq":"x"}}}`,
		`{"where":{"author.name*":{"eq":"x"}}}`,
		`{"where":{"and":[{"author.name":{"eq":"H"}},{"author.age":{"gt":40}}]}}`,
	}

	for i, body := range bodies {
		fmt.Printf("%d. %s\n", i+1, body)
		f, err := parseWhere(bookRes, []byte(body))
		if err != nil {
			fmt.Printf("   parse error: %v\n\n", err)
			continue
		}
		resolved, err := include.ResolveFilter(bookRes, f, include.DefaultOptions)
		if err != nil {
			var ie *include.Error
			if errors.As(err, &ie) {
				fmt.Printf("   %s at %q (status %d)\n\n", ie.Code, ie.Path, ie.Status)
			} else {
				fmt.Printf("   resolve error: %v\n\n", err)
			}
			continue
		}
		sql, args, err := render(bookRes, resolved)
		if err != nil {
			fmt.Printf("   render error: %v\n\n", err)
			continue
		}
		fmt.Printf("   %s\n", sql)
		fmt.Printf("   args=%v  subqueries=%d\n\n", args, include.FilterSubqueries(resolved))
	}
}
