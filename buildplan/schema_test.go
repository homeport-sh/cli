package buildplan

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Commands says what it writes, for a host that types what it serves from a
// JSONSchema() method (rsc-kit's, in the control plane's dashboard): a type
// with its own MarshalJSON otherwise says nothing, and reaches TypeScript as
// unknown. The schema is what MarshalJSON writes: one command a string,
// several a list, none null.
func TestCommandsSayWhatTheyWrite(t *testing.T) {
	var c any = Commands{}
	d, ok := c.(interface{ JSONSchema() map[string]any })
	if !ok {
		t.Fatal("Commands has no JSONSchema() map[string]any")
	}
	want := map[string]any{"anyOf": []any{
		map[string]any{"type": "string"},
		map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		map[string]any{"type": "null"},
	}}
	if got := d.JSONSchema(); !reflect.DeepEqual(got, want) {
		t.Fatalf("JSONSchema() = %v, want %v", got, want)
	}

	// Each shape it names is one MarshalJSON writes.
	for c, w := range map[string]Commands{`"migrate"`: One("migrate"), `["migrate","seed"]`: {"migrate", "seed"}, `null`: nil} {
		if b, _ := json.Marshal(w); string(b) != c {
			t.Errorf("%v marshals as %s, want %s", []string(w), b, c)
		}
	}
}
