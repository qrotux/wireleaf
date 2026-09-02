package huma

// fixwave_test.go — the final-review fix wave's own tests.
//
//   - C1: SchemaFromRef is on huma's request-validation path and populates the
//     conversion cache lazily, so it must be safe from many goroutines at once.
//   - I1: the response body must carry NO trailing newline (spec §6) and NO
//     HTML escaping.

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	humav2 "github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"

	"github.com/qrotux/wireleaf/apidoc"
	"github.com/qrotux/wireleaf/reflector"
)

// ---------------------------------------------------------------------------
// C1 — concurrent SchemaFromRef against a COLD cache
// ---------------------------------------------------------------------------

// RaceBody and its auxiliaries give the bridge several distinct component names
// to convert, so the goroutines below contend on more than one map key.
type RaceBody struct {
	Name  string    `json:"name"`
	Aux   RaceAux   `json:"aux"`
	Other RaceOther `json:"other"`
	Maybe *RaceAux  `json:"maybe"`
}

type RaceAux struct {
	Label string `json:"label"`
}

type RaceOther struct {
	N int `json:"n"`
}

// TestSchemaFromRefIsRaceFree hammers a COLD conversion cache from 8 goroutines.
// Under -race, an unsynchronized read/write of Registry.converted (or of
// Registry.types via TypeFromRef) is reported here.
//
// The assertion beyond "no race" is that every goroutine gets the SAME
// *humav2.Schema pointer for a name — the insert resolves the duplicate-work
// race so huma never holds two schemas for one component.
func TestSchemaFromRefIsRaceFree(t *testing.T) {
	c := apidoc.NewComponents()
	bridge := NewRegistry(c, &reflector.Reflector{}).(*Registry)

	// Registration (wiring time, single-threaded) — the cache stays COLD: Schema
	// with allowRef=true returns a $ref without converting anything.
	bridge.Schema(reflect.TypeFor[RaceBody](), true, "RaceBody")

	names := []string{"RaceBody", "RaceAux", "RaceOther"}
	for _, n := range names {
		if _, ok := c.Get(n); !ok {
			t.Fatalf("component %q was not registered: cache is not cold over the names under test", n)
		}
	}

	const goroutines = 8
	got := make([][]*humav2.Schema, goroutines)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for g := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // maximize overlap on the cold cache
			row := make([]*humav2.Schema, len(names))
			for i, n := range names {
				row[i] = bridge.SchemaFromRef(apidoc.RefPrefix + n)
			}
			got[g] = row
		}()
	}
	close(start)
	wg.Wait()

	for i, n := range names {
		want := got[0][i]
		if want == nil {
			t.Fatalf("SchemaFromRef(%q) = nil", n)
		}
		for g := 1; g < goroutines; g++ {
			if got[g][i] != want {
				t.Errorf("component %q: goroutine %d got a DIFFERENT *Schema than goroutine 0 — the cache handed out two schemas for one name", n, g)
			}
		}
	}
}

// TestMapIsRaceFree covers the other request-time reader: Map walks every name
// through schemaFor, and huma marshals it on the document path.
func TestMapIsRaceFree(t *testing.T) {
	c := apidoc.NewComponents()
	bridge := NewRegistry(c, &reflector.Reflector{}).(*Registry)
	bridge.Schema(reflect.TypeFor[RaceBody](), true, "RaceBody")

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if m := bridge.Map(); len(m) == 0 {
				t.Error("Map() is empty")
			}
		}()
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// I1 — the wireleaf JSON format
// ---------------------------------------------------------------------------

// EscapeBody carries a string field whose value contains the three bytes
// encoding/json escapes by default.
type EscapeBody struct {
	Text string `json:"text"`
}

type escapeInput struct {
	Body EscapeBody
}

type escapeOutput struct {
	Body EscapeBody
}

// TestResponseBodyHasNoTrailingNewlineOrEscaping pins BOTH properties of
// jsonFormat through a real huma round trip.
func TestResponseBodyHasNoTrailingNewlineOrEscaping(t *testing.T) {
	cfg := NewConfig("test", "1.0.0")
	_, api := humatest.New(t, cfg)

	humav2.Register(api, humav2.Operation{
		OperationID: "post-escape",
		Method:      http.MethodPost,
		Path:        "/escape",
	}, func(ctx context.Context, in *escapeInput) (*escapeOutput, error) {
		return &escapeOutput{Body: in.Body}, nil
	})

	const raw = `a<b&c>d`
	resp := api.Post("/escape", map[string]any{"text": raw})
	if resp.Code != http.StatusOK {
		t.Fatalf("request = %d: %s", resp.Code, resp.Body.String())
	}
	body := resp.Body.String()

	if strings.HasSuffix(body, "\n") {
		t.Errorf("response body ends with a newline (spec §6 forbids it): %q", body)
	}
	if !strings.Contains(body, raw) {
		t.Errorf("response body escaped the string: got %q, want it to contain %q verbatim", body, raw)
	}
	// The three sequences encoding/json emits with SetEscapeHTML(true).
	for _, esc := range []string{"\\u003c", "\\u003e", "\\u0026"} {
		if strings.Contains(body, esc) {
			t.Errorf("response body contains the HTML escape %s: %q", esc, body)
		}
	}
	if body != `{"text":"a<b&c>d"}` {
		t.Errorf("response body = %q, want exactly %q", body, `{"text":"a<b&c>d"}`)
	}
}

// TestNewConfigInstallsWireleafJSONFormat pins that BOTH format keys are the
// wireleaf format, not huma's stock one (which appends a newline).
func TestNewConfigInstallsWireleafJSONFormat(t *testing.T) {
	cfg := NewConfig("test", "1.0.0")
	for _, key := range []string{"application/json", "json"} {
		f, ok := cfg.Formats[key]
		if !ok {
			t.Fatalf("format %q missing", key)
		}
		var sb strings.Builder
		if err := f.Marshal(&sb, map[string]any{"k": "<v>"}); err != nil {
			t.Fatal(err)
		}
		if got, want := sb.String(), `{"k":"<v>"}`; got != want {
			t.Errorf("format %q marshalled %q, want %q", key, got, want)
		}
	}
}
