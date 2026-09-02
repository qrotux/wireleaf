package include

import "testing"

func TestEdgeKind(t *testing.T) {
	r := func() Resource { return nil }
	cases := []struct {
		name string
		e    Edge
		want EdgeKindType
	}{
		{"in-array", Edge{Target: r, ArrayPath: "images", Many: true}, KindInArray},
		{"reverse", Edge{Target: r, Backref: "book_id", Many: true}, KindReverse},
		{"forward-hasMany", Edge{Target: r, Many: true}, KindForwardHasMany},
		{"to-one", Edge{Target: r}, KindToOne},
		// Computed has NO target and is checked FIRST in the discriminant
		// chain: it wins over every other set field.
		{"computed", Edge{Computed: true}, KindComputed},
		{"computed-beats-in-array", Edge{Computed: true, ArrayPath: "images", Many: true}, KindComputed},
		{"computed-beats-reverse", Edge{Computed: true, Backref: "book_id", Many: true}, KindComputed},
		{"computed-beats-many", Edge{Computed: true, Many: true}, KindComputed},
	}
	for _, c := range cases {
		if got := EdgeKind(c.e); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
