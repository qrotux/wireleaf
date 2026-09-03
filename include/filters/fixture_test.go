package filters_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/qrotux/wireleaf/include"
)

// stubRes is an inline Resource WITH columns (the ColumnSource seam) for the
// parser tests: the parsers read Edges() and Columns() and nothing else.
type stubRes struct {
	name  string
	edges map[string]include.Edge
	cols  map[string]include.Column
}

func (r *stubRes) Name() string                       { return r.name }
func (r *stubRes) Slug() string                       { return r.name }
func (r *stubRes) Fields() []string                   { return nil }
func (r *stubRes) Defaults() []string                 { return nil }
func (r *stubRes) Edges() map[string]include.Edge     { return r.edges }
func (r *stubRes) IDOf(any) string                    { return "" }
func (r *stubRes) Serialize(any, *include.Ctx) any    { return nil }
func (r *stubRes) Enrich([]any, *include.Ctx) error   { return nil }
func (r *stubRes) Columns() map[string]include.Column { return r.cols }

// filterRoot is the Author root of the parser tests: string "name", int "age",
// bool "active" (all filterable); the to-many filterable edge "works" to Book
// (string "title") and the to-one filterable edge "self" back to Author.
func filterRoot(t *testing.T) include.Resource {
	t.Helper()
	str := reflect.TypeFor[string]()
	author := &stubRes{name: "Author", cols: map[string]include.Column{
		"name":   {Col: "name", Type: str, Filterable: true},
		"age":    {Col: "age", Type: reflect.TypeFor[int](), Filterable: true},
		"active": {Col: "active", Type: reflect.TypeFor[bool](), Filterable: true},
	}}
	book := &stubRes{name: "Book", cols: map[string]include.Column{
		"title": {Col: "title", Type: str, Filterable: true},
	}}
	author.edges = map[string]include.Edge{
		"self":  {Target: func() include.Resource { return author }, Filterable: true},
		"works": {Target: func() include.Resource { return book }, Many: true, Backref: "authorId", Filterable: true},
	}
	return author
}

// stampedRoot has one filterable time.Time column for the coercion test.
func stampedRoot() include.Resource {
	return &stubRes{name: "Stamped", cols: map[string]include.Column{
		"at": {Col: "at", Type: reflect.TypeFor[time.Time](), Filterable: true},
	}}
}
