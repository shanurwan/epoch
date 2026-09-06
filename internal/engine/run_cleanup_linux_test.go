//go:build linux

package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shanurwan/epoch/internal/artifact"
	"github.com/shanurwan/epoch/internal/firecracker"
)

type cleanupCancellingGuest struct {
	Guest
	cancel      context.CancelFunc
	shutdownErr error
}

func (g cleanupCancellingGuest) Call(ctx context.Context, op string, payload, result any) error {
	if op != "shutdown" {
		return g.Guest.Call(ctx, op, payload, result)
	}
	g.cancel()
	return errors.Join(g.Guest.Call(ctx, op, payload, result), g.shutdownErr)
}

type cleanupCancellingVMM struct {
	VMM
	cancel context.CancelFunc
}

func (p cleanupCancellingVMM) Stop(ctx context.Context) error {
	p.cancel()
	return p.VMM.Stop(ctx)
}

func TestLifecycleCancellationDuringCleanup(t *testing.T) {
	for _, tc := range []struct {
		name                                         string
		inShutdown                                   bool
		shutdownFailure, stopFailure, earlierFailure bool
	}{
		{name: "guest shutdown", inShutdown: true},
		{name: "guest shutdown error retained", inShutdown: true, shutdownFailure: true},
		{name: "VMM stop"},
		{name: "incomplete cleanup takes precedence", stopFailure: true},
		{name: "earlier execution error retained", earlierFailure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLifecycleFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.earlierFailure {
				f.guest.onAction = func(_ context.Context, n int) error {
					if n == 2 {
						return errors.New("earlier execution failure")
					}
					return nil
				}
			}
			if tc.stopFailure {
				f.process.stopErr = errors.New("VMM death remains unconfirmed")
			}
			if tc.inShutdown {
				ready := f.deps.ready
				f.deps.ready = func(ctx context.Context, p VMM, socket string, port uint32, id, nonce string, image *artifact.Prepared) (Guest, error) {
					g, err := ready(ctx, p, socket, port, id, nonce, image)
					if err != nil {
						return nil, err
					}
					var shutdownErr error
					if tc.shutdownFailure {
						shutdownErr = errors.New("guest shutdown reply lost")
					}
					return cleanupCancellingGuest{Guest: g, cancel: cancel, shutdownErr: shutdownErr}, nil
				}
			} else {
				start := f.deps.start
				f.deps.start = func(ctx context.Context, options firecracker.Options) (VMM, error) {
					p, err := start(ctx, options)
					if err != nil {
						return nil, err
					}
					return cleanupCancellingVMM{VMM: p, cancel: cancel}, nil
				}
			}
			r := f.run(ctx)
			assertion, cleanup, code := "PASS", "COMPLETE", 130
			if tc.earlierFailure {
				assertion = "NOT_EVALUATED"
			}
			if tc.stopFailure {
				cleanup, code = "INCOMPLETE", 4
			}
			assertLifecycleOutcome(t, r, "CANCELLED", assertion, cleanup, code)
			if tc.stopFailure {
				if _, err := os.Stat(filepath.Join(f.config.RuntimeRoot, r.RunID, "rootfs.ext4")); err != nil {
					t.Fatalf("uncertain-live disk removed: %v", err)
				}
				f.assertBaseUnchanged()
			} else {
				f.assertClean(r)
			}
			if f.guest.shutdowns != 1 || !f.guest.closed || f.process.cleanupExpired.Load() {
				t.Fatal("parent cancellation prevented independent guest/VMM cleanup")
			}
			causes := strings.Join(r.Causes, "\n")
			for _, check := range []struct {
				required bool
				text     string
			}{
				{true, "cancelled during cleanup"},
				{tc.shutdownFailure, "guest shutdown reply lost"},
				{tc.stopFailure, "VMM death remains unconfirmed"},
				{tc.earlierFailure, "earlier execution failure"},
			} {
				if check.required && !strings.Contains(causes, check.text) {
					t.Fatalf("cause %q lost: %v", check.text, r.Causes)
				}
			}
			saved, err := ReadReport(f.config, r.RunID)
			if err != nil {
				t.Fatal(err)
			}
			assertLifecycleOutcome(t, saved, "CANCELLED", assertion, cleanup, code)
		})
	}
}
