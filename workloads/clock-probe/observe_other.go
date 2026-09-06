//go:build !linux

package main

import (
	"errors"
	"time"
)

func observe() (time.Time, int64, error) {
	return time.Time{}, 0, errors.New("clock-probe actual OS clock observations require Linux")
}
