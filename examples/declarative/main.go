// Command declarative is the struct-literal way to declare a graph: nodes as
// graph.Spec (node_spec.go) and the links between them as one-to-many /
// many-to-many relations (graph_to_many.go). It hydrates one book with every
// relation pulled in, in both directions.
package main

import (
	"fmt"

	"github.com/qrotux/wireleaf/include"
)

func main() {
	g, err := buildGraph()
	if err != nil {
		panic(err)
	}

	// author.mentor is the Spec-owned edge, author.books the one-to-many
	// reverse side, tags the many-to-many forward side and tags.tagged its
	// reverse side — each reverse edge loaded by its own bound fetcher.
	inc, err := include.ParseInclude("author.mentor,author.books,tags.tagged")
	if err != nil {
		panic(err)
	}
	raw, _, err := include.HydrateByID(g.Resource("Book"), "b2", inc, nil,
		func(_ *include.Ctx, id string) (any, error) {
			bk, ok := books[id]
			if !ok {
				return nil, nil
			}
			return bk, nil
		},
		&include.Ctx{Registry: g}, include.DefaultOptions)
	if err != nil {
		panic(err)
	}
	fmt.Println(string(raw))
}
