package scenario

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/shanurwan/epoch/internal/jsonutil"
)

type AssertionResult struct {
	ID      string          `json:"id"`
	Step    string          `json:"step"`
	Status  string          `json:"status"`
	Actual  json.RawMessage `json:"actual,omitempty"`
	Message string          `json:"message,omitempty"`
}

// Evaluate evaluates the supplied assertions; callers may supply only assertions
// for completed steps. Missing/malformed observations are execution errors.
func Evaluate(assertions []Assertion, observations map[string]json.RawMessage) ([]AssertionResult, error) {
	results := make([]AssertionResult, 0, len(assertions))
	for _, a := range assertions {
		raw, ok := observations[a.Step]
		if !ok {
			return results, fmt.Errorf("assertion %q: required step observation %q unavailable", a.ID, a.Step)
		}
		var observation any
		if err := jsonutil.Decode(raw, &observation); err != nil {
			return results, fmt.Errorf("assertion %q: malformed observation: %w", a.ID, err)
		}
		actual, exists, err := resolvePointer(observation, a.Pointer)
		if err != nil {
			return results, fmt.Errorf("assertion %q: %w", a.ID, err)
		}
		result := AssertionResult{ID: a.ID, Step: a.Step, Status: "FAIL"}
		if exists {
			result.Actual, err = json.Marshal(actual)
			if err != nil {
				return results, err
			}
		}
		var pass bool
		switch a.Op {
		case "exists":
			pass = exists
		case "absent":
			pass = !exists
		case "eq", "ne", "gt", "gte", "lt", "lte":
			if !exists {
				return results, fmt.Errorf("assertion %q: required observation pointer %q unavailable", a.ID, a.Pointer)
			}
			var expected any
			if err := jsonutil.Decode(a.Value, &expected); err != nil {
				return results, fmt.Errorf("assertion %q: invalid expected value: %w", a.ID, err)
			}
			if a.Op == "eq" || a.Op == "ne" {
				pass, err = equal(actual, expected)
				if a.Op == "ne" {
					pass = !pass
				}
			} else {
				x, xok := actual.(json.Number)
				y, yok := expected.(json.Number)
				if !xok || !yok {
					return results, fmt.Errorf("assertion %q: ordering requires numeric observation and value", a.ID)
				}
				var cmp int
				cmp, err = compareNumbers(x, y)
				switch a.Op {
				case "gt":
					pass = cmp > 0
				case "gte":
					pass = cmp >= 0
				case "lt":
					pass = cmp < 0
				case "lte":
					pass = cmp <= 0
				}
			}
			if err != nil {
				return results, fmt.Errorf("assertion %q: %w", a.ID, err)
			}
		default:
			return results, fmt.Errorf("assertion %q: unsupported operation %q", a.ID, a.Op)
		}
		if pass {
			result.Status = "PASS"
		} else {
			result.Message = fmt.Sprintf("%s comparison at %s failed", a.Op, a.Pointer)
		}
		results = append(results, result)
	}
	return results, nil
}

func equal(a, b any) (bool, error) {
	switch x := a.(type) {
	case nil:
		return b == nil, nil
	case bool:
		y, ok := b.(bool)
		return ok && x == y, nil
	case string:
		y, ok := b.(string)
		return ok && x == y, nil
	case json.Number:
		y, ok := b.(json.Number)
		if !ok {
			return false, nil
		}
		cmp, err := compareNumbers(x, y)
		return cmp == 0, err
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false, nil
		}
		for i := range x {
			same, err := equal(x[i], y[i])
			if !same || err != nil {
				return same, err
			}
		}
		return true, nil
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false, nil
		}
		for key, v := range x {
			other, ok := y[key]
			if !ok {
				return false, nil
			}
			same, err := equal(v, other)
			if !same || err != nil {
				return same, err
			}
		}
		return true, nil
	default:
		return false, fmt.Errorf("unsupported JSON value type %T", a)
	}
}

func compareNumbers(a, b json.Number) (int, error) {
	for _, n := range []json.Number{a, b} {
		if err := jsonutil.ValidateNumber(n); err != nil {
			return 0, err
		}
	}
	x, ok := new(big.Rat).SetString(a.String())
	if !ok {
		return 0, fmt.Errorf("invalid number %q", a)
	}
	y, ok := new(big.Rat).SetString(b.String())
	if !ok {
		return 0, fmt.Errorf("invalid number %q", b)
	}
	return x.Cmp(y), nil
}

// pointerTokens implements the JSON string form of RFC 6901, not URI fragments.
func pointerTokens(pointer string) ([]string, error) {
	if pointer == "" {
		return nil, nil
	}
	if len(pointer) > 4096 || !strings.HasPrefix(pointer, "/") {
		return nil, fmt.Errorf("invalid JSON Pointer %q", pointer)
	}
	tokens := strings.Split(pointer[1:], "/")
	for i, token := range tokens {
		var out strings.Builder
		for j := 0; j < len(token); j++ {
			if token[j] != '~' {
				out.WriteByte(token[j])
				continue
			}
			j++
			if j >= len(token) {
				return nil, fmt.Errorf("invalid JSON Pointer escape")
			}
			switch token[j] {
			case '0':
				out.WriteByte('~')
			case '1':
				out.WriteByte('/')
			default:
				return nil, fmt.Errorf("invalid JSON Pointer escape")
			}
		}
		tokens[i] = out.String()
	}
	return tokens, nil
}

func resolvePointer(value any, pointer string) (any, bool, error) {
	tokens, err := pointerTokens(pointer)
	if err != nil {
		return nil, false, err
	}
	for _, token := range tokens {
		switch v := value.(type) {
		case map[string]any:
			var ok bool
			value, ok = v[token]
			if !ok {
				return nil, false, nil
			}
		case []any:
			if token == "-" {
				return nil, false, nil
			}
			if token == "" || (len(token) > 1 && token[0] == '0') {
				return nil, false, fmt.Errorf("invalid JSON Pointer array index %q", token)
			}
			for _, c := range token {
				if c < '0' || c > '9' {
					return nil, false, fmt.Errorf("invalid JSON Pointer array index %q", token)
				}
			}
			index, err := strconv.ParseUint(token, 10, 64)
			if err != nil || index >= uint64(len(v)) {
				return nil, false, nil
			}
			value = v[int(index)]
		default:
			return nil, false, nil
		}
	}
	return value, true, nil
}

func availablePointer(op, pointer string) error {
	tokens, err := pointerTokens(pointer)
	if err != nil {
		return err
	}
	if len(tokens) == 0 {
		return nil
	}
	allowed := map[string]bool{}
	switch op {
	case "clock.read":
		allowed = map[string]bool{"realtime": true, "monotonic_ns": true}
	case "clock.set":
		allowed = map[string]bool{"target": true, "before": true, "after": true, "elapsed_ns": true, "tolerance_ms": true, "readback_valid": true}
	case "action.exec":
		allowed = map[string]bool{"exit_code": true, "stdout_json": true, "stderr": true, "stdout_truncated": true, "stderr_truncated": true, "before": true, "after": true}
	case "service.start", "service.stop":
		allowed = map[string]bool{"service": true, "running": true, "before": true, "after": true, "stdout": true, "stderr": true}
	}
	if !allowed[tokens[0]] {
		return fmt.Errorf("pointer %q references an unavailable %s observation", pointer, op)
	}
	if len(tokens) > 1 && tokens[0] != "stdout_json" {
		if (tokens[0] != "before" && tokens[0] != "after") || len(tokens) != 2 || (tokens[1] != "realtime" && tokens[1] != "monotonic_ns") {
			return fmt.Errorf("pointer %q references an unavailable typed observation", pointer)
		}
	}
	return nil
}
