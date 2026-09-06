package scenario

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const valid = `{"api_version":"epoch/v1alpha1","name":"clock","image":"image-v1","workload":"clock-probe","steps":[{"id":"past","op":"clock.set","at":"1999-12-31T23:00:00-01:00"},{"id":"read","op":"action.exec","action":"observe"}],"assertions":[{"id":"year","step":"read","pointer":"/stdout_json/year_utc","op":"eq","value":2000}]}`

func TestDefaultsAndNormalization(t *testing.T) {
	s, err := Decode([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	if s.TimeoutMS != 120000 || s.BootTimeoutMS != 30000 || s.Resources != (Resources{1, 512}) || s.Steps[1].TimeoutMS != 5000 || s.Steps[0].ToleranceMS != 1000 {
		t.Fatalf("incorrect defaults: %+v", s)
	}
	if s.Steps[0].At != "2000-01-01T00:00:00Z" {
		t.Fatalf("not UTC: %s", s.Steps[0].At)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("revalidation failed: %v", err)
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(data); err != nil {
		t.Fatalf("resolved scenario cannot decode: %v", err)
	}
}

func TestDecodeRejectsInvalidScenarios(t *testing.T) {
	cases := map[string]string{
		"duplicate":                  strings.Replace(valid, `"name":"clock"`, `"name":"clock","name":"second"`, 1),
		"case alias":                 strings.Replace(valid, `"name":"clock"`, `"Name":"clock"`, 1),
		"unknown":                    strings.Replace(valid, `"name":"clock"`, `"name":"clock","mystery":true`, 1),
		"trailing":                   valid + `{}`,
		"null resource":              strings.Replace(valid, `"steps":`, `"resources":null,"steps":`, 1),
		"zero timeout":               strings.Replace(valid, `"steps":`, `"timeout_ms":0,"steps":`, 1),
		"negative timeout":           strings.Replace(valid, `"steps":`, `"timeout_ms":-1,"steps":`, 1),
		"large timeout":              strings.Replace(valid, `"steps":`, `"timeout_ms":600001,"steps":`, 1),
		"null pointer":               strings.Replace(valid, `"pointer":"/stdout_json/year_utc"`, `"pointer":null`, 1),
		"missing pointer":            strings.Replace(valid, `"pointer":"/stdout_json/year_utc",`, "", 1),
		"bad pointer":                strings.Replace(valid, `/stdout_json/year_utc`, `/stdout_json/~2`, 1),
		"unavailable pointer":        strings.Replace(valid, `/stdout_json/year_utc`, `/not_an_observation`, 1),
		"wrong operation field":      strings.Replace(valid, `"at":"1999`, `"action":"observe","at":"1999`, 1),
		"explicit false wrong field": strings.Replace(valid, `"action":"observe"`, `"action":"observe","allow_live":false`, 1),
		"step null":                  strings.Replace(valid, `"action":"observe"`, `"action":null`, 1),
		"early timestamp":            strings.Replace(valid, `1999-12-31T23:00:00-01:00`, `1989-12-31T23:59:59Z`, 1),
		"late timestamp":             strings.Replace(valid, `1999-12-31T23:00:00-01:00`, `2100-01-01T00:00:00Z`, 1),
		"no offset":                  strings.Replace(valid, `1999-12-31T23:00:00-01:00`, `2000-01-01T00:00:00`, 1),
		"invalid offset":             strings.Replace(valid, `1999-12-31T23:00:00-01:00`, `2000-01-01T00:00:00+24:00`, 1),
		"fraction precision":         strings.Replace(valid, `1999-12-31T23:00:00-01:00`, `2000-01-01T00:00:00.1234567890Z`, 1),
		"dotted workload ID":         strings.Replace(valid, `"workload":"clock-probe"`, `"workload":"clock.probe"`, 1),
		"unknown op":                 strings.Replace(valid, `"clock.set"`, `"host.exec"`, 1),
		"duplicate step":             strings.Replace(valid, `"id":"read"`, `"id":"past"`, 1),
		"absent reference":           strings.Replace(valid, `"step":"read"`, `"step":"absent"`, 1),
		"unknown assertion":          strings.Replace(valid, `"op":"eq"`, `"op":"eval"`, 1),
		"ordering string":            strings.Replace(strings.Replace(valid, `"op":"eq"`, `"op":"gt"`, 1), `"value":2000`, `"value":"2000"`, 1),
		"exists with value":          strings.Replace(valid, `"op":"eq"`, `"op":"exists"`, 1),
		"input duplicate":            strings.Replace(valid, `"action":"observe"`, `"action":"observe","input":{"x":1,"x":2}`, 1),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode([]byte(input)); err == nil {
				t.Fatal("accepted invalid scenario")
			}
		})
	}
}

func TestPlanOrdering(t *testing.T) {
	base, err := Decode([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	clock := base.Steps[0]
	read := base.Steps[1]
	start := Step{ID: "start", Op: "service.start", Service: "worker"}
	stop := Step{ID: "stop", Op: "service.stop", Service: "worker"}
	later := Step{ID: "later", Op: "clock.set", At: "2001-01-01T00:00:00Z"}
	for name, steps := range map[string][]Step{
		"action before clock":     {read, clock},
		"service before clock":    {start, clock, read},
		"unapproved live jump":    {clock, start, later, read},
		"stop before start":       {clock, stop, read},
		"duplicate service start": {clock, start, {ID: "start2", Op: "service.start", Service: "worker"}, read},
		"excessive waits":         {clock, {ID: "wait1", Op: "wait", DurationMS: 70000}, {ID: "wait2", Op: "wait", DurationMS: 70000}, read},
	} {
		t.Run(name, func(t *testing.T) {
			s := *base
			s.Steps = steps
			if err := s.Validate(); err == nil {
				t.Fatal("accepted invalid ordering")
			}
		})
	}
	later.AllowLive = true
	base.Steps = []Step{clock, start, later, stop, read}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid live jump: %v", err)
	}
}

func TestRequiredAssertionsAndInputLimit(t *testing.T) {
	s, err := Decode([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	s.Assertions = nil
	if err := s.Validate(); err == nil {
		t.Fatal("accepted no assertions")
	}
	s, err = Decode([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	s.Steps[1].Input = json.RawMessage(`"` + strings.Repeat("a", MaxInputSize) + `"`)
	if err := s.Validate(); err == nil {
		t.Fatal("accepted oversized input")
	}
}

func TestCommittedScenariosValidate(t *testing.T) {
	files, err := filepath.Glob("../../scenarios/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("find scenarios: %v", err)
	}
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Decode(data); err != nil {
				t.Fatalf("%s: %v", file, err)
			}
		})
	}
}
