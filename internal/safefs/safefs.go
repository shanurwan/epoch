// Package safefs enforces the private, trusted local filesystem boundary.
// Malicious peers running as the same UID are outside the initial threat model.
package safefs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ValidateNoSymlinks requires an existing absolute path and checks each component.
func ValidateNoSymlinks(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("path must be absolute and clean: %q", path)
	}
	volume := filepath.VolumeName(path)
	current := volume + string(filepath.Separator)
	rel := strings.TrimPrefix(path, current)
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		fi, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink path component: %s", current)
		}
		if err := checkTrusted(fi); err != nil {
			return fmt.Errorf("%s: %w", current, err)
		}
	}
	return nil
}

func ValidatePrivateDir(path string) error {
	if err := ValidateNoSymlinks(path); err != nil {
		return err
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return errors.New("private path is not a directory")
	}
	return checkPrivate(fi, true)
}

func EnsurePrivateDir(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("private directory must be absolute and clean")
	}
	fi, err := os.Lstat(path)
	if err == nil {
		if !fi.IsDir() {
			return errors.New("private path is not directory")
		}
		return ValidatePrivateDir(path)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(path)
	if _, err := os.Lstat(parent); errors.Is(err, os.ErrNotExist) {
		if err := EnsurePrivateDir(parent); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if err := ValidateNoSymlinks(parent); err != nil {
		return err
	}
	if err := os.Mkdir(path, 0700); err != nil {
		return err
	}
	return ValidatePrivateDir(path)
}

func ValidatePrivateFile(path string) error {
	if err := ValidateNoSymlinks(path); err != nil {
		return err
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return errors.New("private path is not a regular file")
	}
	return checkPrivate(fi, false)
}

func AtomicJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(path, append(data, '\n'))
}

// AtomicWrite synchronizes exact bytes then publishes within the same directory.
// Directory synchronization is supported on Linux; arbitrary storage faults are
// outside this durability guarantee.
func AtomicWrite(path string, data []byte) (err error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("publication path must be absolute and clean")
	}
	parent := filepath.Dir(path)
	if err := ValidatePrivateDir(parent); err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		if err := ValidatePrivateFile(path); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.CreateTemp(parent, ".epoch-json-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if f != nil {
			_ = f.Close()
		}
		_ = os.Remove(tmp)
	}()
	if err = f.Chmod(0600); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	f = nil
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDir(parent)
}

// OwnedChild rejects traversal and validates a direct run-owned entry.
func OwnedChild(root, name string) (string, error) {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsAny(name, "/\\\x00") {
		return "", errors.New("unsafe child name")
	}
	if err := ValidatePrivateDir(root); err != nil {
		return "", err
	}
	path := filepath.Join(root, name)
	fi, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("owned child is a symlink")
	}
	// The validated 0700 parent is the access boundary. In particular, Unix
	// sockets created by a child may inherit the operator's ordinary umask.
	if err := checkOwned(fi); err != nil {
		return "", err
	}
	return path, nil
}
