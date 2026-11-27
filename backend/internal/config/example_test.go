package config

import (
	"bufio"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// exampleFile is the owner's template for a production .env (docs/deploy.md).
const exampleFile = "../../../deploy/flow.env.example"

// notLoaded are settings the example lists that Load does not read: cmd/flow reads the log level itself.
var notLoaded = []string{"BACKEND_LOG_LEVEL"}

// A setting line: NAME=value, commented or not, or a comment "# NAME: value" for one compose.yaml sets.
var exampleLine = regexp.MustCompile(`^(#\s*)?(BACKEND_[A-Z0-9_]+)(=|: )(.*)$`)

type exampleSetting struct {
	name, value string
	// commented "NAME=value" lines show the default; uncommented ones are placeholders the owner fills in, "NAME:"
	// ones what compose.yaml sets.
	commented, composeSet bool
}

func readExample(t *testing.T) []exampleSetting {
	t.Helper()
	f, err := os.Open(exampleFile)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []exampleSetting
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if m := exampleLine.FindStringSubmatch(sc.Text()); m != nil {
			out = append(out, exampleSetting{name: m[2], value: m[4], commented: m[1] != "", composeSet: m[3] != "="})
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// The example lists every setting Load reads, and nothing it does not: a new setting, or a renamed one, must reach
// the owner's template.
func TestExample_listsEverySetting(t *testing.T) {
	read := map[string]bool{}
	vars := oidcVars(nil)
	if _, err := Load(func(name string) string { read[name] = true; return vars[name] }); err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, s := range readExample(t) {
		if listed[s.name] {
			t.Errorf("%s: listed twice", s.name)
		}
		listed[s.name] = true
		if !read[s.name] && !slices.Contains(notLoaded, s.name) {
			t.Errorf("%s: listed in %s, but Load does not read it", s.name, exampleFile)
		}
	}
	for name := range read {
		if strings.HasPrefix(name, "BACKEND_") && !listed[name] {
			t.Errorf("%s: Load reads it, %s does not list it", name, exampleFile)
		}
	}
}

// A commented line shows the default: setting it to that value changes nothing.
func TestExample_defaultsAreLoadsDefaults(t *testing.T) {
	want, err := Load(lookup(oidcVars(nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range readExample(t) {
		if !s.commented || s.composeSet || slices.Contains(notLoaded, s.name) {
			continue
		}
		got, err := Load(lookup(oidcVars(map[string]string{s.name: s.value})))
		if err != nil {
			t.Errorf("%s=%s: %v", s.name, s.value, err)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s=%s is not the default", s.name, s.value)
		}
	}
}
