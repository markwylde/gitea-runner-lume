// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// dirContainsPath reports whether target is dir or lives beneath it,
// case-insensitively, so a name-prefix sibling (job1 vs job10) is not a match.
func dirContainsPath(dir, target string) bool {
	return pathWithin(filepath.Clean(dir), filepath.Clean(target), true)
}

// KillProcessesWithCWDUnder terminates every process in the runner's session
// whose cwd is one of dirs or below, catching leftovers the path scan misses
// (Win32_Process exposes no cwd) that still pin a handle. Best-effort.
func KillProcessesWithCWDUnder(ctx context.Context, dirs []string) (int, error) {
	if len(dirs) == 0 {
		return 0, nil
	}

	pids, err := listProcessIDs()
	if err != nil {
		return 0, err
	}

	self := uint32(os.Getpid())
	var ownSession uint32
	if err := windows.ProcessIdToSessionId(self, &ownSession); err != nil {
		return 0, fmt.Errorf("look up own session: %w", err)
	}

	var killed int
	var errs []error
	for _, pid := range pids {
		if ctx.Err() != nil {
			break
		}
		// Never touch ourselves, System Idle (0) or System (4).
		if pid == self || pid == 0 || pid == 4 {
			continue
		}
		var session uint32
		if err := windows.ProcessIdToSessionId(pid, &session); err != nil || session != ownSession {
			continue
		}
		ok, err := terminateIfCWDUnder(pid, dirs)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if ok {
			killed++
		}
	}

	return killed, errors.Join(errs...)
}

// terminateIfCWDUnder opens pid once and both reads its cwd and terminates it
// through that one handle, so a PID recycled between the two cannot be hit.
func terminateIfCWDUnder(pid uint32, dirs []string) (bool, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return false, nil // best-effort: gone, higher integrity, or another user
	}
	defer func() { _ = windows.CloseHandle(h) }()

	cwd, err := processCWDHandle(h)
	if err != nil || cwd == "" {
		return false, nil
	}

	for _, d := range dirs {
		if dirContainsPath(d, cwd) {
			if err := windows.TerminateProcess(h, 1); err != nil {
				return false, fmt.Errorf("terminate pid %d: %w", pid, err)
			}
			return true, nil
		}
	}
	return false, nil
}

// listProcessIDs returns the PIDs of every process in a Toolhelp snapshot.
func listProcessIDs() ([]uint32, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = windows.CloseHandle(snap) }()

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))

	pids := make([]uint32, 0, 256)
	err = windows.Process32First(snap, &entry)
	for err == nil {
		pids = append(pids, entry.ProcessID)
		err = windows.Process32Next(snap, &entry)
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return pids, err
	}
	return pids, nil
}

// processCWDHandle reads a process's working directory from its PEB (walking
// PEB -> ProcessParameters -> CurrentDirectory.DosPath). h must carry
// PROCESS_QUERY_INFORMATION|PROCESS_VM_READ.
func processCWDHandle(h windows.Handle) (string, error) {
	var pbi windows.PROCESS_BASIC_INFORMATION
	var retLen uint32
	if err := windows.NtQueryInformationProcess(h, windows.ProcessBasicInformation, unsafe.Pointer(&pbi), uint32(unsafe.Sizeof(pbi)), &retLen); err != nil {
		return "", err
	}
	if pbi.PebBaseAddress == nil {
		return "", errors.New("nil PEB base address")
	}
	pebAddr := uintptr(unsafe.Pointer(pbi.PebBaseAddress))

	paramsAddr, err := readRemotePtr(h, pebAddr+unsafe.Offsetof(windows.PEB{}.ProcessParameters))
	if err != nil || paramsAddr == 0 {
		return "", err
	}

	// DosPath is the leading NTUnicodeString of CURDIR, so it shares CurrentDirectory's address.
	dosPathAddr := paramsAddr + unsafe.Offsetof(windows.RTL_USER_PROCESS_PARAMETERS{}.CurrentDirectory)
	length, err := readRemoteU16(h, dosPathAddr+unsafe.Offsetof(windows.NTUnicodeString{}.Length))
	if err != nil || length == 0 {
		return "", err
	}
	bufAddr, err := readRemotePtr(h, dosPathAddr+unsafe.Offsetof(windows.NTUnicodeString{}.Buffer))
	if err != nil || bufAddr == 0 {
		return "", err
	}

	u16 := make([]uint16, (length+1)/2) // round up so an odd Length cannot overflow the buffer
	var n uintptr
	if err := windows.ReadProcessMemory(h, bufAddr, (*byte)(unsafe.Pointer(&u16[0])), uintptr(length), &n); err != nil {
		return "", err
	}
	if n != uintptr(length) {
		return "", errors.New("short read of working directory")
	}
	return windows.UTF16ToString(u16), nil
}

// readRemotePtr reads a single pointer-sized value from another process.
func readRemotePtr(h windows.Handle, addr uintptr) (uintptr, error) {
	var v uintptr
	var n uintptr
	if err := windows.ReadProcessMemory(h, addr, (*byte)(unsafe.Pointer(&v)), unsafe.Sizeof(v), &n); err != nil {
		return 0, err
	}
	if n != unsafe.Sizeof(v) {
		return 0, errors.New("short pointer read")
	}
	return v, nil
}

// readRemoteU16 reads a single uint16 from another process.
func readRemoteU16(h windows.Handle, addr uintptr) (uint16, error) {
	var v uint16
	var n uintptr
	if err := windows.ReadProcessMemory(h, addr, (*byte)(unsafe.Pointer(&v)), unsafe.Sizeof(v), &n); err != nil {
		return 0, err
	}
	if n != unsafe.Sizeof(v) {
		return 0, errors.New("short uint16 read")
	}
	return v, nil
}
