// Package config keeps operator paths and admission policy out of scenarios.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shanurwan/epoch/internal/jsonutil"
)

const Version = "epoch-config/v1"

type Policy struct {
	MaxVCPUs             int   `json:"max_vcpus"`
	MaxMemoryMiB         int   `json:"max_memory_mib"`
	MaxDiskBytes         int64 `json:"max_disk_bytes"`
	ReserveBytes         int64 `json:"reserve_bytes"`
	MaxTimeoutMS         int64 `json:"max_timeout_ms"`
	HostClockThresholdMS int64 `json:"host_clock_threshold_ms"`
}

type Config struct {
	APIVersion      string `json:"api_version"`
	FirecrackerPath string `json:"firecracker_path"`
	ArtifactDir     string `json:"artifact_manifest_dir"`
	EvidenceDir     string `json:"evidence_dir"`
	RuntimeRoot     string `json:"runtime_root"`
	GuestCID        uint32 `json:"guest_cid"`
	GuestPort       uint32 `json:"guest_port"`
	Policy          Policy `json:"policy"`
}

func Defaults() Config {
	return Config{APIVersion: Version, GuestCID: 3, GuestPort: 7000, Policy: Policy{
		MaxVCPUs: 2, MaxMemoryMiB: 2048, MaxDiskBytes: 4 << 30, ReserveBytes: 5 << 30,
		MaxTimeoutMS: 600000, HostClockThresholdMS: 250,
	}}
}

func Load(path string) (Config, error) {
	c := Defaults()
	b, err := ReadBounded(path, 1<<20)
	if err != nil {
		return c, err
	}
	if err = jsonutil.Decode(b, &c); err != nil {
		return c, fmt.Errorf("config: %w", err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(b, &fields); err != nil {
		return c, err
	}
	for _, key := range []string{"api_version", "firecracker_path", "artifact_manifest_dir", "evidence_dir", "runtime_root"} {
		if _, ok := fields[key]; !ok {
			return c, fmt.Errorf("config.%s is required", key)
		}
	}
	if err = rejectNulls(fields); err != nil {
		return c, err
	}
	if raw, ok := fields["policy"]; ok {
		var policy map[string]json.RawMessage
		if err = json.Unmarshal(raw, &policy); err != nil {
			return c, err
		}
		if err = rejectNulls(policy); err != nil {
			return c, err
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return c, err
	}
	for _, p := range []*string{&c.FirecrackerPath, &c.ArtifactDir, &c.EvidenceDir, &c.RuntimeRoot} {
		if *p == "" {
			return c, fmt.Errorf("all operator paths are required")
		}
		if strings.HasPrefix(*p, "~/") || strings.HasPrefix(*p, "~\\") {
			home, err := os.UserHomeDir()
			if err != nil {
				return c, err
			}
			*p = filepath.Join(home, (*p)[2:])
		} else if !filepath.IsAbs(*p) {
			*p = filepath.Join(filepath.Dir(abs), *p)
		}
		*p = filepath.Clean(*p)
	}
	return c, c.Validate()
}

func (c Config) Validate() error {
	if c.APIVersion != Version {
		return fmt.Errorf("unsupported config api_version %q", c.APIVersion)
	}
	if c.GuestCID != 3 || c.GuestPort != 7000 {
		return fmt.Errorf("single-VM profile requires guest CID 3 and port 7000")
	}
	p := c.Policy
	if p.MaxVCPUs < 1 || p.MaxVCPUs > 2 || p.MaxMemoryMiB < 128 || p.MaxMemoryMiB > 2048 ||
		p.MaxDiskBytes < 1 || p.MaxDiskBytes > 4<<30 || p.ReserveBytes < 5<<30 ||
		p.MaxTimeoutMS < 1 || p.MaxTimeoutMS > 600000 || p.HostClockThresholdMS < 1 || p.HostClockThresholdMS > 60000 {
		return fmt.Errorf("policy outside conservative single-host limits")
	}
	if c.RuntimeRoot == c.EvidenceDir || within(c.RuntimeRoot, c.EvidenceDir) || within(c.EvidenceDir, c.RuntimeRoot) {
		return fmt.Errorf("runtime and persistent evidence roots must be separate, non-nested directories")
	}
	// sockaddr_un has 108 bytes on the reference Linux host, including NUL.
	if len(filepath.Join(c.RuntimeRoot, strings.Repeat("a", 32), "vsock.sock")) > 90 {
		return fmt.Errorf("runtime_root is too long for the per-run Unix socket")
	}
	return nil
}

func rejectNulls(fields map[string]json.RawMessage) error {
	for k, v := range fields {
		if bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return fmt.Errorf("config field %s cannot be null", k)
		}
	}
	return nil
}

func within(parent, child string) bool {
	r, e := filepath.Rel(parent, child)
	return e == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator))
}
