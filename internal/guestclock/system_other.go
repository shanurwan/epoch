//go:build !linux

package guestclock

import "errors"

func NewSystem() (Identity, *Controller, error) {
	return Identity{}, nil, errors.New("guest clock control requires Linux inside an Epoch microVM")
}
