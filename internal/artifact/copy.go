package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// CopyPrivate publishes a full byte copy only after validating its length/hash.
// The caller owns a validated private destination directory and reserves space.
func CopyPrivate(ctx context.Context, base File, destination string) (err error) {
	if err = CheckRegular(base.Path); err != nil {
		return err
	}
	if filepath.Clean(base.Path) == filepath.Clean(destination) {
		return fmt.Errorf("base and private disk must differ")
	}
	if _, e := os.Lstat(destination); !os.IsNotExist(e) {
		return fmt.Errorf("destination must not exist: %s", destination)
	}
	in, err := os.Open(base.Path)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	if st.Size() != base.SizeBytes {
		return fmt.Errorf("base size changed")
	}
	tmp, err := os.OpenFile(destination+".partial", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() {
		tmp.Close()
		if err != nil {
			_ = os.Remove(destination + ".partial")
		}
	}()
	h := sha256.New()
	n, err := copyContext(ctx, io.MultiWriter(tmp, h), io.LimitReader(in, base.SizeBytes+1))
	if err != nil {
		return err
	}
	if n != base.SizeBytes || hex.EncodeToString(h.Sum(nil)) != base.SHA256 {
		return fmt.Errorf("base changed while copying")
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return os.Rename(destination+".partial", destination)
}
