package graph

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/qrotux/wireleaf/include"
)

// The generic constructors are wrappers over the *Wire ones, so the two paths
// must agree field for field. This is the only check that pins backref,
// arrayPath and subField: shapeOf below does not record them.
func TestOfConstructorsEqualGeneric(t *testing.T) {
	tg := reflect.TypeFor[TgWire]()
	for _, tc := range []struct {
		name         string
		generic, run EdgeKind
	}{
		{"ToOne", ToOne[TgWire](), ToOneWire(tg)},
		{"ToMany", ToMany[TgWire](), ToManyWire(tg)},
		{"Reverse", Reverse[TgWire]("bookId"), ReverseWire(tg, "bookId")},
		{"InArray", InArray[TgWire]("people", "user"), InArrayWire(tg, "people", "user")},
		{"pointer wire", ToOne[*TgWire](), ToOneWire(reflect.TypeFor[*TgWire]())},
	} {
		if tc.run != tc.generic {
			t.Errorf("%s: *Wire = %+v, generic = %+v", tc.name, tc.run, tc.generic)
		}
	}
}

// A wire type no node declares is a Compile finding on the *Wire path exactly
// as on the generic one — the constructor itself validates nothing, and
// Compile checks declaredness, not structness (int and an undeclared struct
// fail the same way).
func TestOfConstructorsLeaveValidationToCompile(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind EdgeKind
		want string
	}{
		{"nil wire", ToOneWire(nil), "edge has no target node"},
		{"undeclared non-struct wire", ToOneWire(reflect.TypeFor[int]()), "edge target: no node of this builder declares wire type int"},
		{"undeclared struct wire", ToOneWire(reflect.TypeFor[RvWire]()), "edge target: no node of this builder declares wire type graph.RvWire"},
		{"empty backref", ReverseWire(reflect.TypeFor[AuWire](), ""), "reverse edge requires a backref"},
	} {
		fs := compileFindings(t, func(b *Builder) {
			book, _ := okBook(b), okAuthor(b)
			book.Edge("x", tc.kind)
			b.Root(book)
		})
		if !hasFinding(fs, tc.want) {
			t.Errorf("%s: want finding %q, got%s", tc.name, tc.want, dumpFindings(fs))
		}
	}
}

// specViaWire declares the spec_test chain fixture with every edge kind built
// from a reflect.Type value.
func specViaWire(b *Builder) (book *NodeHandle[bkRow, BkWire], author *NodeHandle[auRow, AuWire]) {
	book = Node[bkRow, BkWire](b, "Book").
		Slug("bk").
		Wire(specBookWire).
		PrimaryKey(specBookPK).
		Enrich(specEnrich).
		Defaults("author").
		Envelope(specEnvelope).
		Inputs(specInputs)
	book.Edge("author", ToOneWire(reflect.TypeOf(AuWire{}))).
		ForeignKey(specBookFK).
		Inverse("books").
		Required().
		Includable().
		Policies(include.MissingRequiredError)
	author = Node[auRow, AuWire](b, "Author").
		Wire(specAuthorWire).
		PrimaryKey(specAuthorPK).
		DocExternal()
	author.Edge("books", ReverseWire(reflect.TypeOf(BkWire{}), "authorId")).
		Guard(specGuard).
		Inverse("author").
		Includable().
		Limit(10).
		Args(Arg("page", specArgOK)).
		Envelope(specEnvelope)
	FetchIDs(b, author, specFetchAuthors)
	FetchParents(b, book, specFetchBooksByAuthor)
	b.Root(book)
	return book, author
}

func TestOfConstructorsCompileAndHydrateLikeChain(t *testing.T) {
	cb, ob := NewBuilder(), NewBuilder()
	cBook, cAuthor := specViaChain(cb)
	oBook, oAuthor := specViaWire(ob)
	for _, pair := range []struct {
		name      string
		chain, of *nodeSpec
	}{{"Book", cBook.spec, oBook.spec}, {"Author", cAuthor.spec, oAuthor.spec}} {
		want, got := shapeOf(cb, pair.chain), shapeOf(ob, pair.of)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: *Wire recorded\n  %+v\nchain recorded\n  %+v", pair.name, got, want)
		}
	}

	hydrate := func(declare func(*Builder) (*NodeHandle[bkRow, BkWire], *NodeHandle[auRow, AuWire])) []byte {
		t.Helper()
		b := NewBuilder()
		declare(b)
		g, err := b.Compile()
		if err != nil {
			t.Fatalf("Compile: %v", err)
		}
		inc, _ := include.ParseInclude("author.books")
		raw, _, err := include.HydrateEntity(g.Resource("Book"), specBooks["b1"], inc, nil,
			&include.Ctx{Registry: g}, include.DefaultOptions)
		if err != nil {
			t.Fatalf("HydrateEntity: %v", err)
		}
		return raw
	}
	if chain, of := hydrate(specViaChain), hydrate(specViaWire); !bytes.Equal(chain, of) {
		t.Errorf("bytes differ:\n chain %s\n *Wire   %s", chain, of)
	}
}
