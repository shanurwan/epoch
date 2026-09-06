//go:build !linux

package config

import "os"

func openRead(path string) (*os.File, error) { return os.Open(path) }
