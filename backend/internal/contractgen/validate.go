package contractgen

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

// Validate checks a JSON value against the named def: required fields present, no unknown fields, types and enum
// values right. It is how fixtures are held to the language-neutral source rather than to one binding.
func (d *Doc) Validate(def string, raw []byte) error {
	s, ok := d.Defs[def]
	if !ok {
		return fmt.Errorf("unknown type %q", def)
	}
	v, err := decodeValue(raw)
	if err != nil {
		return err
	}
	return d.validate(def, s, v)
}

// ValidateUnion checks an envelope of union and, when its kind is known, its data against the kind's payload. An
// unknown kind fails: fixtures and test streams use only contract kinds (a reader in production skips them instead).
func (d *Doc) ValidateUnion(union string, raw []byte) error {
	i := slices.IndexFunc(d.Unions, func(u Union) bool { return u.Name == union })
	if i < 0 {
		return fmt.Errorf("unknown union %q", union)
	}
	u := d.Unions[i]
	if err := d.Validate(u.Envelope, raw); err != nil {
		return err
	}
	var env struct {
		Kind string          `json:"kind"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return err
	}
	k := slices.IndexFunc(u.Kinds, func(k UnionKind) bool { return k.Kind == env.Kind })
	if k < 0 {
		return fmt.Errorf("unknown %s %q", u.KindType, env.Kind)
	}
	if err := d.Validate(u.Kinds[k].Payload, env.Data); err != nil {
		return fmt.Errorf("data: %w", err)
	}
	return nil
}

// KindOf returns the union member that names kind.
func (d *Doc) KindOf(union, kind string) (UnionKind, bool) {
	for _, u := range d.Unions {
		if u.Name != union {
			continue
		}
		for _, k := range u.Kinds {
			if k.Kind == kind {
				return k, true
			}
		}
	}
	return UnionKind{}, false
}

func decodeValue(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

func (d *Doc) validate(path string, s *Schema, v any) error {
	if s.Ref != "" {
		name, err := d.resolve(s.Ref)
		if err != nil {
			return err
		}
		return d.validate(path, d.Defs[name], v)
	}
	switch s.Type {
	case "":
		return nil
	case "object":
		m, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: want an object", path)
		}
		if s.Values != nil {
			var errs []error
			for _, k := range sortedKeys(m) {
				errs = append(errs, d.validate(path+"."+k, s.Values, m[k]))
			}
			return errors.Join(errs...)
		}
		var errs []error
		for _, r := range s.Required {
			if _, ok := m[r]; !ok {
				errs = append(errs, fmt.Errorf("%s: missing %q", path, r))
			}
		}
		for _, k := range sortedKeys(m) {
			ps, ok := s.Properties[k]
			if !ok {
				errs = append(errs, fmt.Errorf("%s: unknown field %q", path, k))
				continue
			}
			errs = append(errs, d.validate(path+"."+k, ps, m[k]))
		}
		return errors.Join(errs...)
	case "array":
		a, ok := v.([]any)
		if !ok {
			return fmt.Errorf("%s: want an array", path)
		}
		var errs []error
		for i, e := range a {
			errs = append(errs, d.validate(fmt.Sprintf("%s[%d]", path, i), s.Items, e))
		}
		return errors.Join(errs...)
	case "string":
		str, ok := v.(string)
		if !ok {
			return fmt.Errorf("%s: want a string", path)
		}
		if len(s.Enum) > 0 && !slices.Contains(s.Enum, str) {
			return fmt.Errorf("%s: %q is not one of %v", path, str, s.Enum)
		}
		if s.Format == "date-time" {
			if _, err := time.Parse(time.RFC3339Nano, str); err != nil {
				return fmt.Errorf("%s: %q is not RFC 3339", path, str)
			}
		}
		return nil
	case "integer":
		n, ok := v.(json.Number)
		if !ok {
			return fmt.Errorf("%s: want an integer", path)
		}
		if _, err := n.Int64(); err != nil {
			return fmt.Errorf("%s: %s is not an integer", path, n)
		}
		return nil
	case "number":
		if _, ok := v.(json.Number); !ok {
			return fmt.Errorf("%s: want a number", path)
		}
		return nil
	case "boolean":
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("%s: want a boolean", path)
		}
		return nil
	}
	return fmt.Errorf("%s: unsupported type %q", path, s.Type)
}
