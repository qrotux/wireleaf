// Command costlimit shows a cost-based rate limiter built on the three numbers
// the engine exposes for it: plan.Cost, the static row estimate ResolvePlan
// computed for one root document, ctx.Rows(), the rows a hydration actually
// materialized, and include.FilterSubqueries, the correlated EXISTS a resolved
// filter will run.
//
// The pattern is the one GitHub and Shopify use for GraphQL: every client owns
// a bucket of points that refills over time; a request RESERVES its estimated
// cost before any database work, is refused with 429 when the bucket cannot
// cover it, and SETTLES to the actual cost afterwards so an over-estimate is
// refunded. A cheap request costs a few points, a deep include costs
// thousands, and the bucket — not a request counter — decides who waits.
//
// The library never rate-limits by itself; this file is the reference
// implementation an application adapts (a real one keys buckets by API key
// and keeps them in Redis).
package main

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/qrotux/wireleaf/graph"
	"github.com/qrotux/wireleaf/include"
)

// ------------------------------------------------------------------ the bucket

// costBucket is a leaky bucket in points. capacity is the burst a client may
// spend at once; refill is points per second.
type costBucket struct {
	capacity float64
	refill   float64
	tokens   float64
	last     time.Time
	now      func() time.Time
}

func newBucket(capacity, refillPerSec float64, now func() time.Time) *costBucket {
	return &costBucket{capacity: capacity, refill: refillPerSec, tokens: capacity, last: now(), now: now}
}

func (b *costBucket) top() {
	t := b.now()
	b.tokens = min(b.capacity, b.tokens+b.refill*t.Sub(b.last).Seconds())
	b.last = t
}

// Reserve takes cost points up front. false means the client must wait.
func (b *costBucket) Reserve(cost int) bool {
	b.top()
	if float64(cost) > b.tokens {
		return false
	}
	b.tokens -= float64(cost)
	return true
}

// Settle replaces a reservation with the actual cost: a refund when the
// estimate was high, an extra charge (possibly overdrawing) when it was low.
func (b *costBucket) Settle(reserved, actual int) {
	b.tokens += float64(reserved - actual)
	if b.tokens > b.capacity {
		b.tokens = b.capacity
	}
}

// ------------------------------------------------------------------ the graph

type authorRow struct {
	ID   string
	Name string
}

type bookRow struct {
	ID       string
	Title    string
	AuthorID string
}

type reviewRow struct {
	ID     string
	BookID string
	Stars  int
}

type AuthorWire struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type BookWire struct {
	ID    string `json:"id"`
	Title string `json:"title" col:"title,filter"`
}

type ReviewWire struct {
	ID    string `json:"id"`
	Stars int    `json:"stars"`
}

// Two authors with ten books each and twenty reviews per book: enough rows
// that a deep include really costs what the estimate says.
var (
	authors = map[string]authorRow{
		"a1": {ID: "a1", Name: "Ursula K. Le Guin"},
		"a2": {ID: "a2", Name: "Stanisław Lem"},
	}
	books   = map[string]bookRow{}
	reviews = map[string]reviewRow{}
)

func init() {
	for _, aid := range slices.Sorted(maps.Keys(authors)) {
		for i := 1; i <= 10; i++ {
			bid := fmt.Sprintf("%s-b%02d", aid, i)
			books[bid] = bookRow{ID: bid, Title: fmt.Sprintf("Book %d", i), AuthorID: aid}
			for j := 1; j <= 20; j++ {
				rid := fmt.Sprintf("%s-r%02d", bid, j)
				reviews[rid] = reviewRow{ID: rid, BookID: bid, Stars: 1 + (i+j)%5}
			}
		}
	}
}

// byParent is the reverse-batch fetcher shape shared by both reverse edges.
func byParent[R any](all map[string]R, parentOf func(R) string) func(*include.Ctx, []string, include.EdgeQuery) (map[string]graph.ParentRows[R], error) {
	return func(_ *include.Ctx, parentIDs []string, q include.EdgeQuery) (map[string]graph.ParentRows[R], error) {
		out := make(map[string]graph.ParentRows[R], len(parentIDs))
		for _, pid := range parentIDs {
			var rows []R
			for _, id := range slices.Sorted(maps.Keys(all)) {
				if r := all[id]; parentOf(r) == pid {
					rows = append(rows, r)
				}
			}
			hasMore := q.Limit > 0 && len(rows) > q.Limit
			if hasMore {
				rows = rows[:q.Limit]
			}
			out[pid] = graph.ParentRows[R]{Rows: rows, HasMore: hasMore}
		}
		return out, nil
	}
}

func byIDs[R any](all map[string]R) func(*include.Ctx, []string) ([]R, error) {
	return func(_ *include.Ctx, ids []string) ([]R, error) {
		out := make([]R, 0, len(ids))
		for _, id := range ids {
			if r, ok := all[id]; ok {
				out = append(out, r)
			}
		}
		return out, nil
	}
}

func buildGraph() (*graph.Graph, include.Resource) {
	b := graph.NewBuilder()

	author := graph.Node[authorRow, AuthorWire](b, "Author").
		Wire(func(r authorRow, _ *include.Ctx) AuthorWire { return AuthorWire{ID: r.ID, Name: r.Name} }).
		PrimaryKey(func(r authorRow) string { return r.ID })
	book := graph.Node[bookRow, BookWire](b, "Book").
		Wire(func(r bookRow, _ *include.Ctx) BookWire { return BookWire{ID: r.ID, Title: r.Title} }).
		PrimaryKey(func(r bookRow) string { return r.ID })
	review := graph.Node[reviewRow, ReviewWire](b, "Review").
		Wire(func(r reviewRow, _ *include.Ctx) ReviewWire { return ReviewWire{ID: r.ID, Stars: r.Stars} }).
		PrimaryKey(func(r reviewRow) string { return r.ID })

	// The per-edge Limit is the cost multiplier: books.reviews on one author
	// is estimated as 1 + 10 + 10×20 = 211 rows.
	author.Edge("books", graph.Reverse[BookWire]("authorId")).Limit(10).Includable().Filterable()
	book.Edge("reviews", graph.Reverse[ReviewWire]("bookId")).Limit(20).Includable()

	graph.FetchIDs(b, author, byIDs(authors))
	graph.FetchIDs(b, book, byIDs(books))
	graph.FetchIDs(b, review, byIDs(reviews))
	graph.FetchParents(b, book, byParent(books, func(r bookRow) string { return r.AuthorID }))
	graph.FetchParents(b, review, byParent(reviews, func(r reviewRow) string { return r.BookID }))

	b.Root(author)
	g, err := b.Compile()
	if err != nil {
		panic(err)
	}
	return g, g.Resource("Author")
}

// ------------------------------------------------------------------ the handler

var errRateLimited = errors.New("429 rate limited: include too expensive for the remaining budget")

// listAuthors is what an HTTP handler does per request, minus the HTTP.
func listAuthors(g *graph.Graph, res include.Resource, bucket *costBucket, includeQS string, where include.Filter, page int) error {
	inc, err := include.ParseInclude(includeQS)
	if err != nil {
		return err
	}
	opts := include.DefaultOptions

	// 1. Plan first: ResolvePlan is pure and cheap, and plan.Cost is the
	//    estimate for ONE root document. Multiply by the page size.
	plan, err := include.ResolvePlan(res, inc, nil, opts)
	if err != nil {
		return err // 400: INVALID_INCLUDE / INCLUDE_TOO_DEEP / INCLUDE_TOO_EXPENSIVE
	}
	// 1b. The filter is the second half of the estimate: ResolveFilter is
	//     equally pure, and FilterSubqueries counts the correlated EXISTS the
	//     adapter will emit. A nil filter costs nothing.
	resolved, err := include.ResolveFilter(res, where, opts)
	if err != nil {
		return err // 400: INVALID_FILTER / FILTER_TOO_DEEP / FILTER_TOO_EXPENSIVE
	}
	// What one correlated EXISTS is worth is a property of the schema and the
	// planner, not of anything the core reports — a placeholder an application
	// calibrates against its own plans.
	const subqueryCost = 50
	filterCost := include.FilterSubqueries(resolved) * subqueryCost
	reserved := plan.Cost*page + filterCost
	if !bucket.Reserve(reserved) {
		return errRateLimited
	}

	// 2. Hydrate. HydrateByQuery re-plans internally (same inputs, same
	//    result) and enforces MaxRows on top of the bucket.
	ctx := &include.Ctx{Registry: g}
	// This in-memory fetcher ignores q.Where; a SQL one renders it the way
	// examples/filter does. What is measured here is the cost, not the rows.
	fetch := func(_ *include.Ctx, q include.QueryArgs) ([]any, int, bool, error) {
		ids := slices.Sorted(maps.Keys(authors))
		docs := make([]any, 0, min(q.Limit, len(ids)))
		for _, id := range ids[:min(q.Limit, len(ids))] {
			docs = append(docs, authors[id])
		}
		return docs, len(ids), len(ids) > q.Limit, nil
	}
	result, _, err := include.HydrateByQuery(res, include.QueryArgs{Where: resolved, Limit: page}, inc, nil, fetch, ctx, opts)

	// 3. Settle to the real cost — refund the over-estimate, or charge the
	//    difference — whether or not hydration succeeded. The filter's share
	//    is carried into the actual: the subqueries ran regardless of how many
	//    rows came back, so unlike the include estimate there is nothing to
	//    refund there.
	bucket.Settle(reserved, ctx.Rows()+filterCost)
	if err != nil {
		return err
	}
	fmt.Printf("  include=%-15q page=%d  estimate=%4d (filter %3d)  settled=%4d (rows %3d + filter %3d)  docs=%d\n",
		includeQS, page, reserved, filterCost, ctx.Rows()+filterCost, ctx.Rows(), filterCost, len(result.Data))
	return nil
}

func main() {
	g, authorRes := buildGraph()

	// A frozen clock so the run is reproducible: no refill between calls.
	clock := time.Unix(0, 0)
	bucket := newBucket(1000, 50, func() time.Time { return clock })

	// One filtered request: authors having a book titled "Book 1" — one
	// to-many hop, so one correlated EXISTS on top of the include estimate.
	titled := include.FilterCond{
		Path:  []include.FilterStep{{Key: "books", Quant: include.QuantAny}},
		Field: "title",
		Op:    include.OpEq,
		Value: "Book 1",
	}

	for _, req := range []struct {
		inc   string
		where include.Filter
		page  int
	}{
		{"books", nil, 2},         // 2 × (1 + 10) = 22 points
		{"books", titled, 2},      // 22 + one EXISTS = 72 points
		{"books.reviews", nil, 2}, // 2 × 211 = 422 points
		{"books.reviews", nil, 2}, // 422 more
		{"books.reviews", nil, 2}, // over budget → 429 before any SQL
	} {
		if err := listAuthors(g, authorRes, bucket, req.inc, req.where, req.page); err != nil {
			fmt.Printf("  include=%-15q page=%d  -> %v\n", req.inc, req.page, err)
		}
		fmt.Printf("  bucket: %.0f/%.0f points left\n", bucket.tokens, bucket.capacity)
	}

	// Ten seconds later the bucket has refilled 500 points and the same
	// request is affordable again.
	clock = clock.Add(10 * time.Second)
	fmt.Println("...10s later")
	if err := listAuthors(g, authorRes, bucket, "books.reviews", nil, 2); err != nil {
		fmt.Println(" ", err)
	}
	fmt.Printf("  bucket: %.0f/%.0f points left\n", bucket.tokens, bucket.capacity)
}
