package include

import "testing"

func TestReverseSortKey(t *testing.T) {
	cols := map[string]string{"startDate": "start_date", "createdAt": "created_at"}
	edge := &Edge{Sort: "startDate", SortCols: cols}

	cases := []struct {
		name string
		args map[string]any
		edge *Edge
		want string
	}{
		{"no args → edge default", nil, edge, "start_date"},
		{"client override asc", map[string]any{"sort": "createdAt"}, edge, "created_at"},
		{"client override desc", map[string]any{"sort": "-createdAt"}, edge, "-created_at"},
		{"unknown client key → default", map[string]any{"sort": "hax; DROP"}, edge, "start_date"},
		{"non-string client arg → default", map[string]any{"sort": []string{"a", "b"}}, edge, "start_date"},
		{"desc edge default", nil, &Edge{Sort: "-startDate", SortCols: cols}, "-start_date"},
		{"default not whitelisted → empty", nil, &Edge{Sort: "nope", SortCols: cols}, ""},
		{"no SortCols → inert", map[string]any{"sort": "createdAt"}, &Edge{Sort: "createdAt"}, ""},
		{"nil edge → inert", nil, nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := reverseSortKey(&PlanNode{Args: tc.args, Edge: tc.edge})
			if got != tc.want {
				t.Fatalf("reverseSortKey = %q, want %q", got, tc.want)
			}
		})
	}
}

// sortSpyReg decorates a Registry recording the EdgeQuery.Sort value each
// batched reverse call received (per resource Name). Non-invasive wrapper,
// mirroring spyReg (materialize_test.go).
type sortSpyReg struct {
	inner Registry
	sorts map[string][]string // resource Name → sort keys seen
}

var _ Registry = (*sortSpyReg)(nil)

func (s *sortSpyReg) FetchByIDs(r Resource) (FetchByIDs, bool) { return s.inner.FetchByIDs(r) }
func (s *sortSpyReg) FetchByEdge(p Resource, k string) (FetchByParents, bool) {
	return s.inner.FetchByEdge(p, k)
}

func (s *sortSpyReg) FetchByParents(r Resource) (FetchByParents, bool) {
	inner, ok := s.inner.FetchByParents(r)
	if !ok {
		return nil, false
	}
	name := r.Name()
	return func(c *Ctx, parentIDs []string, q EdgeQuery) (map[string]ParentRows, error) {
		s.sorts[name] = append(s.sorts[name], q.Sort)
		return inner(c, parentIDs, q)
	}, true
}

// TestMaterialize_Reverse_SortThreaded proves the resolved sort key reaches
// the batched fetcher: edge default, client :sort() override (desc), and the
// unknown-key fallback, on the toy graph's `kids` reverse edge. The unknown-key
// case is a materialize-time fallback; under SortStrict such a key never gets
// this far (see policy_test.go).
func TestMaterialize_Reverse_SortThreaded(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"edge default", nil, "label_col"},
		{"client desc override", map[string]any{"sort": "-id"}, "-id_col"},
		{"unknown key → edge default", map[string]any{"sort": "nope"}, "label_col"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := buildToyGraph()
			spy := &sortSpyReg{inner: g.Reg, sorts: map[string][]string{}}
			ctx := &Ctx{Registry: spy}

			kids := g.A.Edges()["kids"]
			kids.Sort = "label"
			kids.SortCols = map[string]string{"label": "label_col", "id": "id_col"}
			child := childNode("kids", kids, g.B)
			child.Args = tc.args

			if _, err := Materialize(rootWith(g.A, child), []any{toyARow{id: "a1", name: "Alpha"}}, ctx); err != nil {
				t.Fatalf("Materialize: %v", err)
			}
			got := spy.sorts[g.B.Name()]
			if len(got) != 1 || got[0] != tc.want {
				t.Fatalf("FetchByParents sort = %v, want [%q]", got, tc.want)
			}
		})
	}
}
