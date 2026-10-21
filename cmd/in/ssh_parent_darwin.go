//go:build darwin

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// parentSSHProcess gets bounded account-owned metadata and true argv on macOS.
func parentSSHProcess(pid int) (parent int, name string, argv []string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "uid=", "-o", "ppid=", "-o", "comm=").Output()
	if err != nil || len(output) > 4096 {
		return 0, "", nil, fmt.Errorf("cannot inspect parent process")
	}
	parts := strings.Fields(string(output))
	if len(parts) != 3 {
		return 0, "", nil, fmt.Errorf("invalid parent metadata")
	}
	uid, uidErr := strconv.Atoi(parts[0])
	parent, err = strconv.Atoi(parts[1])
	if uidErr != nil || err != nil || uid != os.Getuid() || parent < 0 {
		return 0, "", nil, fmt.Errorf("unowned parent process")
	}
	name = filepath.Base(parts[2])
	if name != "ssh" {
		return parent, name, nil, nil
	}
	argv, err = darwinSSHArgs(pid)
	return parent, name, argv, err
}

// darwinSSHArgs decodes KERN_PROCARGS2's argc and NUL-separated argv, excluding env.
func darwinSSHArgs(pid int) ([]string, error) {
	mib := [3]int32{1, 49, int32(pid)} // CTL_KERN, KERN_PROCARGS2, pid.
	var size uintptr
	_, _, errno := syscall.Syscall6(syscall.SYS___SYSCTL, uintptr(unsafe.Pointer(&mib[0])), uintptr(len(mib)), 0, uintptr(unsafe.Pointer(&size)), 0, 0)
	if errno != 0 || size < 8 || size > 2<<20 {
		return nil, fmt.Errorf("cannot read SSH arguments")
	}
	buf := make([]byte, size)
	_, _, errno = syscall.Syscall6(syscall.SYS___SYSCTL, uintptr(unsafe.Pointer(&mib[0])), uintptr(len(mib)), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0, 0)
	if errno != 0 || size > uintptr(len(buf)) || size < 8 {
		return nil, fmt.Errorf("cannot read SSH arguments")
	}
	return parseDarwinArgs(buf[:size])
}

// parseDarwinArgs skips the executable path and padding, then reads exactly argc values.
func parseDarwinArgs(buf []byte) ([]string, error) {
	if len(buf) < 8 {
		return nil, fmt.Errorf("invalid SSH arguments")
	}
	argc := binary.NativeEndian.Uint32(buf[:4])
	if argc == 0 || argc > 4096 {
		return nil, fmt.Errorf("invalid SSH arguments")
	}
	buf = buf[4:]
	end := bytes.IndexByte(buf, 0)
	if end < 0 {
		return nil, fmt.Errorf("invalid SSH arguments")
	}
	buf = buf[end+1:]
	for len(buf) > 0 && buf[0] == 0 {
		buf = buf[1:]
	}
	argv := make([]string, 0, argc)
	for range argc {
		end = bytes.IndexByte(buf, 0)
		if end < 0 {
			return nil, fmt.Errorf("invalid SSH arguments")
		}
		argv = append(argv, string(buf[:end]))
		buf = buf[end+1:]
	}
	return argv, nil
}
