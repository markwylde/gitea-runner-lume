//go:build !darwin

package run

import "errors"

func physicalMemoryBytes() (uint64, error) {
	return 0, errors.New("Lume resource admission requires macOS")
}
