//go:build darwin

package run

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func physicalMemoryBytes() (uint64, error) {
	value, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0, fmt.Errorf("read physical memory: %w", err)
	}
	return value, nil
}
