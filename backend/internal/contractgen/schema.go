// Package contractgen turns Flow's language-neutral contracts (contract/events.schema.json, api/openapi.json) into
// Go and TypeScript bindings, and validates JSON values against them. It reads a deliberately small JSON Schema
// subset and refuses every keyword outside it, so a schema construct the bindings cannot express fails loudly
// instead of being dropped.
package contractgen

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Schema is one schema object of the supported subset.
type Schema struct {
	Ref         string
	Type        string // object, string, integer, number, boolean, array, or "" (any value)
	Format      string // date-time on strings, int64 on integers
	Description string
	Enum        []string
	Properties  map[string]*Schema
	Required    []string
	Items       *Schema
	// Values is additionalProperties as a schema: the object is a map of it. Nil with Type object: a struct.
	Values *Schema
	// Order is the properties in source order: the bindings keep it.
	Order []string
}

// objectKeys lists a JSON object's keys in source order.
func objectKeys(b []byte) ([]string, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	t, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("want an object: %w", err)
	}
	if t != json.Delim('{') {
		return nil, errors.New("want an object")
	}
	var keys []string
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		keys = append(keys, t.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return nil, err
		}
	}
	return keys, nil
}

// allowed are the keywords the subset reads; title, example and $comment are documentation only.
var allowed = map[string]bool{
	"$ref": true, "type": true, "format": true, "description": true, "enum": true, "properties": true,
	"required": true, "items": true, "additionalProperties": true, "title": true, "example": true, "$comment": true,
}

// UnmarshalJSON reads a schema object and refuses unknown keywords.
func (s *Schema) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	for k := range raw {
		if !allowed[k] {
			return fmt.Errorf("unsupported keyword %q", k)
		}
	}
	var w struct {
		Ref         string             `json:"$ref"`
		Type        string             `json:"type"`
		Format      string             `json:"format"`
		Description string             `json:"description"`
		Enum        []string           `json:"enum"`
		Properties  map[string]*Schema `json:"properties"`
		Required    []string           `json:"required"`
		Items       *Schema            `json:"items"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*s = Schema{Ref: w.Ref, Type: w.Type, Format: w.Format, Description: w.Description, Enum: w.Enum,
		Properties: w.Properties, Required: w.Required, Items: w.Items}
	if p, ok := raw["properties"]; ok {
		order, err := objectKeys(p)
		if err != nil {
			return err
		}
		s.Order = order
	}
	if ap, ok := raw["additionalProperties"]; ok {
		if string(ap) == "false" {
			return nil
		}
		var v Schema
		if err := json.Unmarshal(ap, &v); err != nil {
			return fmt.Errorf("additionalProperties: %w", err)
		}
		s.Values = &v
	}
	return nil
}

// UnionKind is one member of a tagged union: the kind string and its payload type.
type UnionKind struct {
	Kind      string `json:"kind"`
	Payload   string `json:"payload"`
	Ephemeral bool   `json:"ephemeral"`
	Sidecar   bool   `json:"sidecar"`
}

// Union is an envelope type whose kind field selects the payload type of its data field (x-unions).
type Union struct {
	Name     string
	Envelope string      `json:"envelope"`
	KindType string      `json:"kindType"`
	GoPrefix string      `json:"goPrefix"`
	Kinds    []UnionKind `json:"kinds"`
	// Also names further types whose kind field holds this union's kinds (a draft of the envelope).
	Also []string `json:"also"`
}

// kindTypes maps each type with a union kind field to the union's kind type.
func (d *Doc) kindTypes() map[string]string {
	m := map[string]string{}
	for _, u := range d.Unions {
		m[u.Envelope] = u.KindType
		for _, a := range u.Also {
			m[a] = u.KindType
		}
	}
	return m
}

// Doc is a loaded contract: its named types and unions.
type Doc struct {
	// Source names the file, for the generated header.
	Source string
	Defs   map[string]*Schema
	Unions []Union
	order  []string
	refPfx string
}

// Names lists the defs in source order.
func (d *Doc) Names() []string { return d.order }

// LoadEvents reads a JSON Schema document: types under $defs, unions under x-unions.
func LoadEvents(source string, b []byte) (*Doc, error) {
	var root struct {
		Defs   json.RawMessage  `json:"$defs"`
		Unions map[string]Union `json:"x-unions"`
	}
	if err := json.Unmarshal(b, &root); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	d := &Doc{Source: source, refPfx: "#/$defs/"}
	if err := d.readDefs(root.Defs); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	for name, u := range root.Unions {
		u.Name = name
		d.Unions = append(d.Unions, u)
	}
	slices.SortFunc(d.Unions, func(a, b Union) int { return strings.Compare(a.Name, b.Name) })
	return d, d.check()
}

// LoadOpenAPI reads the component schemas of an OpenAPI 3.1 document.
func LoadOpenAPI(source string, b []byte) (*Doc, error) {
	var root struct {
		Components struct {
			Schemas json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(b, &root); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	d := &Doc{Source: source, refPfx: "#/components/schemas/"}
	if err := d.readDefs(root.Components.Schemas); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	return d, d.check()
}

func (d *Doc) readDefs(b json.RawMessage) error {
	if len(b) == 0 {
		return errors.New("no type definitions")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	order, err := objectKeys(b)
	if err != nil {
		return err
	}
	d.order, d.Defs = order, map[string]*Schema{}
	for _, name := range order {
		var s Schema
		if err := json.Unmarshal(raw[name], &s); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		d.Defs[name] = &s
	}
	return nil
}

// check refuses what the emitters cannot express: dangling refs, unnamed enums or nested structs, unions naming
// missing types.
func (d *Doc) check() error {
	if len(d.Defs) == 0 {
		return errors.New("no type definitions")
	}
	var errs []error
	for _, name := range d.Names() {
		s := d.Defs[name]
		switch {
		case s.Type == "object":
			for _, p := range sortedKeys(s.Properties) {
				if err := d.checkField(s.Properties[p]); err != nil {
					errs = append(errs, fmt.Errorf("%s.%s: %w", name, p, err))
				}
			}
			for _, r := range s.Required {
				if _, ok := s.Properties[r]; !ok {
					errs = append(errs, fmt.Errorf("%s: required %q is not a property", name, r))
				}
			}
			if s.Values != nil {
				errs = append(errs, fmt.Errorf("%s: a named map type is not supported, use it as a field", name))
			}
		case s.Type == "string" && len(s.Enum) > 0:
		default:
			errs = append(errs, fmt.Errorf("%s: a named type is an object or a string enum", name))
		}
	}
	for _, u := range d.Unions {
		for _, n := range append([]string{u.Envelope}, u.Also...) {
			if _, ok := d.Defs[n]; !ok {
				errs = append(errs, fmt.Errorf("union %s: type %q undefined", u.Name, n))
			}
		}
		if u.KindType == "" || u.GoPrefix == "" || len(u.Kinds) == 0 {
			errs = append(errs, fmt.Errorf("union %s: kindType, goPrefix and kinds are required", u.Name))
		}
		seen := map[string]bool{}
		for _, k := range u.Kinds {
			if _, ok := d.Defs[k.Payload]; !ok {
				errs = append(errs, fmt.Errorf("union %s: kind %q: payload %q undefined", u.Name, k.Kind, k.Payload))
			}
			if seen[k.Kind] {
				errs = append(errs, fmt.Errorf("union %s: kind %q twice", u.Name, k.Kind))
			}
			seen[k.Kind] = true
		}
	}
	return errors.Join(errs...)
}

func (d *Doc) checkField(s *Schema) error {
	switch {
	case s.Ref != "":
		_, err := d.resolve(s.Ref)
		return err
	case s.Type == "array":
		if s.Items == nil {
			return errors.New("array without items")
		}
		return d.checkField(s.Items)
	case s.Type == "object":
		if s.Values == nil || len(s.Properties) > 0 {
			return errors.New("inline object: name it under the definitions")
		}
		return d.checkField(s.Values)
	case len(s.Enum) > 0:
		return errors.New("inline enum: name it under the definitions")
	case s.Type == "string", s.Type == "integer", s.Type == "number", s.Type == "boolean", s.Type == "":
		return nil
	default:
		return fmt.Errorf("unsupported type %q", s.Type)
	}
}

// resolve returns the def name a $ref points at.
func (d *Doc) resolve(ref string) (string, error) {
	name, ok := strings.CutPrefix(ref, d.refPfx)
	if !ok {
		return "", fmt.Errorf("ref %q: only %s<name>", ref, d.refPfx)
	}
	if _, ok := d.Defs[name]; !ok {
		return "", fmt.Errorf("ref %q: undefined", ref)
	}
	return name, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (s *Schema) isRequired(prop string) bool { return slices.Contains(s.Required, prop) }
