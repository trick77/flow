package contractgen

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// UpdateEnv set to anything non-empty makes Golden write the generated files instead of comparing them.
const UpdateEnv = "CONTRACTGEN_UPDATE"

// Sources, relative to the repository root.
const (
	EventsSchema = "contract/events.schema.json"
	OpenAPISpec  = "backend/api/openapi.json"
)

// Target is one generated file, its path relative to the repository root.
type Target struct {
	Out    string
	Render func() ([]byte, error)
}

// Targets lists every generated file of the repository at root.
func Targets(root string) ([]Target, error) {
	ev, err := loadFile(root, EventsSchema, LoadEvents)
	if err != nil {
		return nil, err
	}
	api, err := loadFile(root, OpenAPISpec, LoadOpenAPI)
	if err != nil {
		return nil, err
	}
	return []Target{
		{Out: "backend/internal/event/event.gen.go", Render: func() ([]byte, error) { return ev.Go("event") }},
		{Out: "ui/src/contract/events.gen.ts", Render: ev.TS},
		{Out: "sidecar/src/contract/events.gen.ts", Render: ev.TS},
		{Out: "backend/internal/apitypes/apitypes.gen.go", Render: func() ([]byte, error) { return api.Go("apitypes") }},
		{Out: "ui/src/api/types.gen.ts", Render: api.TS},
	}, nil
}

// Load reads one of the two sources of the repository at root.
func Load(root, source string) (*Doc, error) {
	if source == OpenAPISpec {
		return loadFile(root, source, LoadOpenAPI)
	}
	return loadFile(root, source, LoadEvents)
}

func loadFile(root, rel string, load func(string, []byte) (*Doc, error)) (*Doc, error) {
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel))) //nolint:gosec // the repository's own contract files
	if err != nil {
		return nil, err
	}
	return load(rel, b)
}

// Golden fails t when a target's file does not hold what its source renders; with UpdateEnv set it writes it.
func Golden(t testing.TB, root string, targets []Target) {
	t.Helper()
	update := os.Getenv(UpdateEnv) != ""
	for _, tg := range targets {
		got, err := tg.Render()
		if err != nil {
			t.Errorf("%s: %v", tg.Out, err)
			continue
		}
		path := filepath.Join(root, filepath.FromSlash(tg.Out))
		if update {
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, got, 0o600); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path) //nolint:gosec // a generated file of the repository
		if err != nil {
			t.Errorf("%s: %v (regenerate: %s)", tg.Out, err, Regenerate)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s is stale: %s (first difference at line %d)", tg.Out, Regenerate, firstDiff(got, want))
		}
	}
}

func firstDiff(a, b []byte) int {
	al, bl := bytes.Split(a, []byte("\n")), bytes.Split(b, []byte("\n"))
	for i := range max(len(al), len(bl)) {
		if i >= len(al) || i >= len(bl) || !bytes.Equal(al[i], bl[i]) {
			return i + 1
		}
	}
	return 0
}
