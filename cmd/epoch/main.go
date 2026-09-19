package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/shanurwan/epoch/internal/config"
	"github.com/shanurwan/epoch/internal/engine"
	"github.com/shanurwan/epoch/internal/evidence"
	"github.com/shanurwan/epoch/internal/firecracker"
	"github.com/shanurwan/epoch/internal/hostcheck"
)

var version = "0.1.0-dev"

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	os.Exit(command(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func command(ctx context.Context, args []string, out, diag io.Writer) int {
	if len(args) == 0 {
		usage(diag)
		return 2
	}
	if args[0] == "version" {
		if len(args) != 1 {
			return 2
		}
		fmt.Fprintf(out, "epoch %s (%s; %s/%s)\nTemporal Forking Infrastructure for Deterministic Batch & Expiry Testing\n", version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
		return 0
	}
	if args[0] == "--help" || args[0] == "help" {
		usage(out)
		return 0
	}
	cmd := args[0]
	switch cmd {
	case "doctor", "validate", "run", "report", "recover":
	default:
		usage(diag)
		return 2
	}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(diag)
	configPath := fs.String("config", "epoch.local.json", "operator configuration")
	var asJSON, probe, apply bool
	if cmd == "doctor" || cmd == "report" || cmd == "run" {
		fs.BoolVar(&asJSON, "json", false, "machine-readable output")
	}
	if cmd == "doctor" {
		fs.BoolVar(&probe, "probe", false, "explicit empty-KVM probe")
	}
	if cmd == "recover" {
		fs.BoolVar(&apply, "apply", false, "apply owned cleanup")
	}
	// Accept the documented positional-before-flags form without a CLI dependency.
	flags, positionals, err := partition(args[1:])
	if err != nil {
		fmt.Fprintln(diag, err)
		return 2
	}
	if err = fs.Parse(flags); err != nil {
		return 2
	}
	want := 1
	if cmd == "doctor" {
		want = 0
	}
	if len(positionals) != want {
		usage(diag)
		return 2
	}
	c, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(diag, "configuration:", evidence.Quote(err.Error()))
		return 2
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	switch cmd {
	case "doctor":
		r, checkErr := hostcheck.Doctor(bounded, c.FirecrackerPath, probe)
		var identity *firecracker.Identity
		if checkErr == nil {
			id, e := firecracker.ValidateExecutable(bounded, c.FirecrackerPath, "1.16.1")
			if e != nil {
				r.Problems = append(r.Problems, e.Error())
				checkErr = e
			} else {
				identity = &id
			}
		}
		if asJSON {
			err = json.NewEncoder(out).Encode(struct {
				Host       hostcheck.Report      `json:"host"`
				Executable *firecracker.Identity `json:"executable,omitempty"`
			}{r, identity})
		} else {
			fmt.Fprintf(out, "Host: %s/%s; UID %d; KVM accessible=%t; SELinux enforcing=%t\n", r.OS, r.Architecture, r.UID, r.KVMAccessible, r.SELinuxEnforcing)
			for _, p := range r.Problems {
				fmt.Fprintln(out, "Problem:", evidence.Quote(p))
			}
			for _, p := range r.Notes {
				fmt.Fprintln(out, "Note:", evidence.Quote(p))
			}
			if identity != nil {
				fmt.Fprintf(out, "%s; SHA-256 %s\n", identity.Version, identity.SHA256)
			}
		}
		if checkErr != nil || err != nil {
			return 3
		}
		return 0
	case "validate":
		p, e := engine.Validate(bounded, c, positionals[0])
		if e != nil {
			fmt.Fprintln(diag, "validation:", evidence.Quote(e.Error()))
			return 2
		}
		fmt.Fprintf(out, "Validated %s: %d steps, %d assertions; local artifact hashes match. No VM booted.\n", p.Scenario.Name, len(p.Scenario.Steps), len(p.Scenario.Assertions))
		return 0
	case "run":
		r, e := engine.Run(bounded, c, positionals[0])
		if r != nil {
			if asJSON {
				if err = json.NewEncoder(out).Encode(r); err != nil {
					return 3
				}
			} else {
				printReport(out, r)
			}
			if r.ReportSaved && !asJSON {
				fmt.Fprintf(out, "Evidence: %s/%s\n", c.EvidenceDir, r.RunID)
			}
			return r.ExitCode()
		}
		if e != nil {
			fmt.Fprintln(diag, "run:", evidence.Quote(e.Error()))
		}
		var invalid *engine.ValidationError
		if errors.As(e, &invalid) {
			return 2
		}
		if ctx.Err() == context.Canceled {
			return 130
		}
		return 3
	case "report":
		if !engine.ValidRunID(positionals[0]) {
			fmt.Fprintln(diag, "invalid run ID")
			return 2
		}
		r, e := engine.ReadReport(c, positionals[0])
		if e != nil {
			fmt.Fprintln(diag, "report:", evidence.Quote(e.Error()))
			return 3
		}
		if asJSON {
			if err = json.NewEncoder(out).Encode(r); err != nil {
				return 3
			}
		} else {
			printReport(out, r)
		}
		if !r.ReportSaved {
			return 3
		}
		return 0
	case "recover":
		if !engine.ValidRunID(positionals[0]) {
			fmt.Fprintln(diag, "invalid run ID")
			return 2
		}
		r, e := engine.Recover(bounded, c, positionals[0], apply)
		if r != nil {
			fmt.Fprintf(out, "Run %s: %s; process alive=%t; cleanup permitted=%t; apply=%t\n", r.RunID, evidence.Quote(r.Message), r.ProcessAlive, r.CanClean, r.Apply)
		}
		if e != nil {
			fmt.Fprintln(diag, "recovery:", evidence.Quote(e.Error()))
			return 4
		}
		if r == nil || !r.CanClean {
			return 4
		}
		return 0
	}
	return 2
}

func partition(args []string) (flags, pos []string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			if a == "--config" || a == "-config" {
				i++
				if i == len(args) {
					return nil, nil, fmt.Errorf("--config requires a path")
				}
				flags = append(flags, args[i])
			}
		} else {
			pos = append(pos, a)
		}
	}
	return flags, pos, nil
}
func printReport(out io.Writer, r *engine.Report) {
	fmt.Fprintf(out, "Run %s: execution=%s assertions=%s cleanup=%s\n", r.RunID, r.ExecutionStatus, r.AssertionStatus, r.CleanupStatus)
	for _, a := range r.Assertions {
		fmt.Fprintf(out, "  %s %s\n", a.Status, evidence.Quote(a.ID))
	}
	for _, cause := range r.Causes {
		fmt.Fprintln(out, "  Cause:", evidence.Quote(cause))
	}
}
func usage(w io.Writer) {
	fmt.Fprintln(w, `Usage:
  epoch version
  epoch doctor --config epoch.local.json [--probe] [--json]
  epoch validate SCENARIO --config epoch.local.json
  epoch run SCENARIO --config epoch.local.json [--json]
  epoch report RUN_ID --config epoch.local.json [--json]
  epoch recover RUN_ID --config epoch.local.json [--apply]`)
}
