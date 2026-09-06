package jsonutil

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeRejectsAmbiguity(t *testing.T) {
	for _, input := range []string{`{"a":1,"a":2}`, `{"nested":{"x":1,"x":2}}`, `[1,{"x":1,"x":2}]`, `{} {}`, `{"a":1}garbage`, strings.Repeat("[", 66) + "0" + strings.Repeat("]", 66), `1e1025`, `1e-1025`, strings.Repeat("9", 257)} {
		t.Run(input[:min(len(input), 50)], func(t *testing.T) {
			var v any
			if err := Decode([]byte(input), &v); err == nil {
				t.Fatalf("accepted %s", input)
			}
		})
	}
}

func TestDecodeExactFieldsAndNumbers(t *testing.T) {
	var v struct {
		Name   string      `json:"name"`
		Number json.Number `json:"number"`
	}
	for _, input := range []string{`{"Name":"x"}`, `{"name":"x","NAME":"y"}`, `{"unknown":true}`} {
		if err := Decode([]byte(input), &v); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
	if err := Decode([]byte(`{"name":"x","number":9007199254740993}`), &v); err != nil {
		t.Fatal(err)
	}
	if v.Number.String() != "9007199254740993" {
		t.Fatalf("lost precision: %v", v.Number)
	}
}
