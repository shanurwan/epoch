//go:build !linux

package guestagent

import (
	"context"
	"errors"
)

func Run(context.Context, string, string) error {
	return errors.New("epoch-agent runs only inside a prepared Linux Epoch guest")
}
func WorkloadExec([]string) error {
	return errors.New("workload execution requires a prepared Linux Epoch guest")
}
