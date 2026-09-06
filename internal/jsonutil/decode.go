// Package jsonutil provides bounded, unambiguous JSON decoding for local contracts.
package jsonutil

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

const MaxDepth = 64

// Decode rejects duplicate object keys, unknown struct fields and trailing data.
// Interface values contain json.Number instead of lossy float64 values.
func Decode(data []byte, dst any) error {
	if !utf8.Valid(data) {
		return fmt.Errorf("JSON must contain valid UTF-8")
	}
	check := json.NewDecoder(bytes.NewReader(data))
	check.UseNumber()
	if err := value(check, 0); err != nil {
		return err
	}
	if _, err := check.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("JSON contains trailing data")
		}
		return fmt.Errorf("JSON trailing data: %w", err)
	}
	if err := exactFields(data, reflect.TypeOf(dst)); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	d.UseNumber()
	if err := d.Decode(dst); err != nil {
		return fmt.Errorf("JSON decode: %w", err)
	}
	return nil
}

// encoding/json intentionally matches struct field names case-insensitively;
// wire contracts require the exact spelling used in their schemas.
func exactFields(data []byte, typ reflect.Type) error {
	if typ == nil {
		return nil
	}
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) || typ == reflect.TypeFor[json.RawMessage]() {
		return nil
	}
	switch typ.Kind() {
	case reflect.Struct:
		if len(bytes.TrimSpace(data)) == 0 || bytes.TrimSpace(data)[0] != '{' {
			return nil
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			return err
		}
		known := make(map[string]reflect.Type)
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if !field.IsExported() {
				continue
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			known[name] = field.Type
		}
		for name, raw := range fields {
			field, ok := known[name]
			if !ok {
				return fmt.Errorf("unknown JSON field %q", name)
			}
			if err := exactFields(raw, field); err != nil {
				return fmt.Errorf("field %s: %w", name, err)
			}
		}
	case reflect.Slice, reflect.Array:
		if typ.Elem().Kind() == reflect.Uint8 {
			return nil
		}
		var items []json.RawMessage
		if err := json.Unmarshal(data, &items); err != nil {
			return nil
		}
		for i, item := range items {
			if err := exactFields(item, typ.Elem()); err != nil {
				return fmt.Errorf("item %d: %w", i, err)
			}
		}
	case reflect.Map:
		var items map[string]json.RawMessage
		if err := json.Unmarshal(data, &items); err != nil {
			return nil
		}
		for key, item := range items {
			if err := exactFields(item, typ.Elem()); err != nil {
				return fmt.Errorf("key %s: %w", key, err)
			}
		}
	}
	return nil
}

func value(d *json.Decoder, depth int) error {
	if depth > MaxDepth {
		return fmt.Errorf("JSON nesting exceeds %d", MaxDepth)
	}
	token, err := d.Token()
	if err != nil {
		return fmt.Errorf("JSON value: %w", err)
	}
	switch t := token.(type) {
	case json.Delim:
		switch t {
		case '{':
			seen := make(map[string]struct{})
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok {
					return fmt.Errorf("JSON object key must be a string")
				}
				if _, exists := seen[name]; exists {
					return fmt.Errorf("duplicate JSON key %q", name)
				}
				seen[name] = struct{}{}
				if err := value(d, depth+1); err != nil {
					return err
				}
			}
			end, err := d.Token()
			if err != nil || end != json.Delim('}') {
				return fmt.Errorf("invalid JSON object ending: %v", err)
			}
		case '[':
			for d.More() {
				if err := value(d, depth+1); err != nil {
					return err
				}
			}
			end, err := d.Token()
			if err != nil || end != json.Delim(']') {
				return fmt.Errorf("invalid JSON array ending: %v", err)
			}
		default:
			return fmt.Errorf("unexpected JSON delimiter %q", t)
		}
	case json.Number:
		if err := ValidateNumber(t); err != nil {
			return err
		}
	}
	return nil
}

// ValidateNumber bounds work needed for exact numeric assertion comparisons.
func ValidateNumber(n json.Number) error {
	s := n.String()
	if len(s) > 256 {
		return fmt.Errorf("JSON numeric literal exceeds 256 characters")
	}
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		exponent, err := strconv.ParseInt(s[i+1:], 10, 32)
		if err != nil || exponent < -1024 || exponent > 1024 {
			return fmt.Errorf("JSON numeric exponent exceeds +/-1024")
		}
	}
	return nil
}
