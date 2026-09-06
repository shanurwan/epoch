package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigStrictness(t *testing.T) {
	root := filepath.VolumeName(os.TempDir()) + string(filepath.Separator)
	base := map[string]any{"api_version": Version, "firecracker_path": "fc", "artifact_manifest_dir": "images", "evidence_dir": filepath.Join(root, "epoch-e"), "runtime_root": filepath.Join(root, "epoch-r")}
	validBytes, _ := json.Marshal(base)
	validPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(validPath, validBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if c, err := Load(validPath); err != nil {
		t.Fatalf("valid baseline rejected: %v", err)
	} else if c.GuestCID != 3 || c.Policy.MaxVCPUs != 2 {
		t.Fatal("defaults missing")
	}
	cases := []struct {
		name   string
		change func(map[string]any)
	}{
		{"null policy", func(m map[string]any) { m["policy"] = nil }},
		{"null cid", func(m map[string]any) { m["guest_cid"] = nil }},
		{"null limit", func(m map[string]any) { m["policy"] = map[string]any{"max_vcpus": nil} }},
		{"missing version", func(m map[string]any) { delete(m, "api_version") }},
		{"unknown", func(m map[string]any) { m["host_clock_set"] = true }},
		{"overcommit", func(m map[string]any) { m["policy"] = map[string]any{"max_vcpus": 3} }},
		{"nested roots", func(m map[string]any) { m["runtime_root"] = filepath.Join(root, "epoch-e", "r") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := map[string]any{}
			for k, v := range base {
				m[k] = v
			}
			tc.change(m)
			b, _ := json.Marshal(m)
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, b, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}
func TestReadBoundedRejectsNonregularAndOversize(t *testing.T) {
	d := t.TempDir()
	if _, err := ReadBounded(d, 5); err == nil {
		t.Fatal("directory accepted")
	}
	p := filepath.Join(d, "large")
	if err := os.WriteFile(p, []byte("123456"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadBounded(p, 5); err == nil {
		t.Fatal("oversize accepted")
	}
}
