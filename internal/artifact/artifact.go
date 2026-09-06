// Package artifact validates explicitly prepared local content. It never fetches.
package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/shanurwan/epoch/internal/config"
	"github.com/shanurwan/epoch/internal/jsonutil"
	"github.com/shanurwan/epoch/internal/protocol"
	"github.com/shanurwan/epoch/internal/safefs"
)

const Version = "epoch-image/v1"

var idPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

type File struct {
	Path           string `json:"path"`
	SHA256         string `json:"sha256"`
	SizeBytes      int64  `json:"size_bytes"`
	SourceIdentity string `json:"source_identity"`
	ChecksumOrigin string `json:"checksum_origin"`
}
type Agent struct {
	Version         string `json:"version"`
	ProtocolVersion string `json:"protocol_version"`
	SHA256          string `json:"sha256"`
}
type Workload struct {
	ID             string `json:"id"`
	Version        string `json:"version"`
	ManifestPath   string `json:"manifest_path"`
	ManifestSHA256 string `json:"manifest_sha256"`
}
type Recipe struct {
	Revision        string   `json:"revision"`
	Transformations []string `json:"transformations"`
}
type Manifest struct {
	APIVersion         string   `json:"api_version"`
	ID                 string   `json:"id"`
	Architecture       string   `json:"architecture"`
	Kernel             File     `json:"kernel"`
	RootFS             File     `json:"rootfs"`
	FirecrackerVersion string   `json:"firecracker_version"`
	GuestAgent         Agent    `json:"guest_agent"`
	Workload           Workload `json:"workload"`
	Recipe             Recipe   `json:"recipe"`
}
type Prepared struct {
	Manifest       Manifest
	ManifestSHA256 string
	Workload       protocol.WorkloadManifest
}

func ValidID(id string) bool { return idPattern.MatchString(id) }

func Load(ctx context.Context, dir, id string, maxDisk int64) (*Prepared, error) {
	if !ValidID(id) {
		return nil, fmt.Errorf("invalid image ID")
	}
	path := filepath.Join(dir, id+".json")
	if err := CheckRegular(path); err != nil {
		return nil, err
	}
	b, err := config.ReadBounded(path, 32<<10)
	if err != nil {
		return nil, err
	}
	p := &Prepared{ManifestSHA256: Hash(b)}
	if err = jsonutil.Decode(b, &p.Manifest); err != nil {
		return nil, fmt.Errorf("image manifest: %w", err)
	}
	m := &p.Manifest
	if m.APIVersion != Version || m.ID != id || m.Architecture != "x86_64" || m.FirecrackerVersion != "1.16.1" {
		return nil, fmt.Errorf("unsupported or mismatched image/version/architecture identity")
	}
	if m.GuestAgent.Version == "" || m.GuestAgent.ProtocolVersion != protocol.Version || !ValidDigest(m.GuestAgent.SHA256) ||
		!ValidID(m.Workload.ID) || m.Workload.Version == "" || !ValidDigest(m.Workload.ManifestSHA256) ||
		m.Recipe.Revision == "" || len(m.Recipe.Transformations) == 0 {
		return nil, fmt.Errorf("incomplete guest/workload/recipe identity")
	}
	if len(m.Recipe.Transformations) > 64 {
		return nil, fmt.Errorf("image recipe has too many transformations")
	}
	for _, change := range m.Recipe.Transformations {
		if strings.TrimSpace(change) == "" || len(change) > 1024 {
			return nil, fmt.Errorf("empty image recipe transformation")
		}
	}
	for _, v := range []struct {
		f   *File
		max int64
	}{{&m.Kernel, 128 << 20}, {&m.RootFS, maxDisk}} {
		if v.f.Path == "" || !ValidDigest(v.f.SHA256) || v.f.SizeBytes <= 0 || v.f.SizeBytes > v.max || v.f.SourceIdentity == "" ||
			(v.f.ChecksumOrigin != "locally-observed" && v.f.ChecksumOrigin != "upstream-published") {
			return nil, fmt.Errorf("invalid artifact content/source metadata")
		}
		v.f.Path = resolve(filepath.Dir(path), v.f.Path)
		if err = VerifyFile(ctx, *v.f); err != nil {
			return nil, err
		}
	}
	if m.Workload.ManifestPath == "" {
		return nil, fmt.Errorf("workload manifest_path is required")
	}
	m.Workload.ManifestPath = resolve(filepath.Dir(path), m.Workload.ManifestPath)
	if err = CheckRegular(m.Workload.ManifestPath); err != nil {
		return nil, err
	}
	w, err := config.ReadBounded(m.Workload.ManifestPath, 1<<20)
	if err != nil {
		return nil, err
	}
	if Hash(w) != m.Workload.ManifestSHA256 {
		return nil, fmt.Errorf("workload manifest digest mismatch")
	}
	if err = jsonutil.Decode(w, &p.Workload); err != nil {
		return nil, err
	}
	if err = p.Workload.Validate(); err != nil {
		return nil, err
	}
	if p.Workload.ID != m.Workload.ID || p.Workload.Version != m.Workload.Version {
		return nil, fmt.Errorf("workload identity mismatch")
	}
	return p, nil
}

func resolve(dir, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(dir, path)
}
func Hash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func ValidDigest(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && s == strings.ToLower(s)
}

// Reject symlinks at every component; content is supplied by a trusted operator.
// Same-UID concurrent replacement is outside this first profile's threat model.
func CheckRegular(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if err = safefs.ValidateNoSymlinks(abs); err != nil {
		return err
	}
	for p := abs; ; p = filepath.Dir(p) {
		st, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink rejected: %s", p)
		}
		if p == abs && !st.Mode().IsRegular() {
			return fmt.Errorf("not a regular file: %s", p)
		}
		parent := filepath.Dir(p)
		if parent == p {
			break
		}
	}
	return nil
}

func VerifyFile(ctx context.Context, a File) error {
	if err := CheckRegular(a.Path); err != nil {
		return err
	}
	f, err := os.Open(a.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() || st.Size() != a.SizeBytes {
		return fmt.Errorf("artifact size mismatch: %s", a.Path)
	}
	h := sha256.New()
	n, err := copyContext(ctx, h, io.LimitReader(f, a.SizeBytes+1))
	if err != nil {
		return err
	}
	if n != a.SizeBytes || hex.EncodeToString(h.Sum(nil)) != a.SHA256 {
		return fmt.Errorf("artifact digest mismatch: %s", a.Path)
	}
	return nil
}

func copyContext(ctx context.Context, w io.Writer, r io.Reader) (int64, error) {
	buf := make([]byte, 128<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, er := r.Read(buf)
		if n > 0 {
			nw, ew := w.Write(buf[:n])
			total += int64(nw)
			if ew != nil {
				return total, ew
			}
			if nw != n {
				return total, io.ErrShortWrite
			}
		}
		if er == io.EOF {
			return total, nil
		}
		if er != nil {
			return total, er
		}
	}
}
