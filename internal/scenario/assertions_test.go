package scenario

import (
	"encoding/json"
	"testing"
)

func TestAssertionsPreserveTypesAndPrecision(t *testing.T) {
	observations := map[string]json.RawMessage{"read": json.RawMessage(`{"n":9007199254740993,"decimal":0.1,"array":[1,{"a":null}],"number_string":"1","a/b":{"~key":true},"":42}`)}
	tests := []struct{ name, pointer, op, value, status string }{
		{"exact integer", "/n", "eq", "9007199254740993", "PASS"},
		{"nearby integer", "/n", "eq", "9007199254740992", "FAIL"},
		{"ordered integer", "/n", "gt", "9007199254740992", "PASS"},
		{"decimal rational", "/decimal", "eq", "1e-1", "PASS"},
		{"number type", "/number_string", "eq", "1", "FAIL"},
		{"recursive equality", "/array", "eq", "[1.0,{\"a\":null}]", "PASS"},
		{"escaped pointer", "/a~1b/~0key", "eq", "true", "PASS"},
		{"empty key", "/", "eq", "42", "PASS"},
		{"null exists", "/array/1/a", "exists", "", "PASS"},
		{"missing absent", "/array/1/missing", "absent", "", "PASS"},
		{"null absent", "/array/1/a", "absent", "", "FAIL"},
		{"missing exists", "/missing", "exists", "", "FAIL"},
		{"array append absent", "/array/-", "absent", "", "PASS"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			results, err := Evaluate([]Assertion{{ID: "check", Step: "read", Pointer: test.pointer, Op: test.op, Value: json.RawMessage(test.value)}}, observations)
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 || results[0].Status != test.status {
				t.Fatalf("got %+v, want %s", results, test.status)
			}
		})
	}
}

func TestMissingOrMalformedObservationsAreErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		pointer, op  string
		observations map[string]json.RawMessage
	}{
		"no step":                  {"/x", "exists", nil},
		"missing required pointer": {"/x", "eq", map[string]json.RawMessage{"read": json.RawMessage(`{}`)}},
		"invalid JSON":             {"/x", "eq", map[string]json.RawMessage{"read": json.RawMessage(`{"x":`)}},
		"duplicate output key":     {"/x", "eq", map[string]json.RawMessage{"read": json.RawMessage(`{"x":1,"x":2}`)}},
		"bad array index":          {"/x/01", "exists", map[string]json.RawMessage{"read": json.RawMessage(`{"x":[1,2]}`)}},
		"string order":             {"/x", "gt", map[string]json.RawMessage{"read": json.RawMessage(`{"x":"1"}`)}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Evaluate([]Assertion{{ID: "test", Step: "read", Pointer: tc.pointer, Op: tc.op, Value: json.RawMessage(`1`)}}, tc.observations)
			if err == nil {
				t.Fatal("expected execution error")
			}
		})
	}
}

func TestPartialResultsPreserved(t *testing.T) {
	a := []Assertion{{ID: "done", Step: "read", Pointer: "/x", Op: "eq", Value: json.RawMessage(`1`)}, {ID: "missing", Step: "other", Pointer: "", Op: "exists"}}
	results, err := Evaluate(a, map[string]json.RawMessage{"read": json.RawMessage(`{"x":1}`)})
	if err == nil || len(results) != 1 || results[0].Status != "PASS" {
		t.Fatalf("results=%+v error=%v", results, err)
	}
}
