//go:build linux

package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// parentSSHProcess reads account-owned Linux ancestry and SSH's literal argv.
func parentSSHProcess(pid int) (parent int, name string, argv []string, err error) {
	ref, parent, name, err := processIdentity(pid)
	if err != nil {
		return 0, "", nil, err
	}
	if name != "ssh" {
		return parent, name, nil, nil
	}
	base := filepath.Join("/proc", strconv.Itoa(pid))
	exe, err := os.Readlink(filepath.Join(base, "exe"))
	if err != nil || filepath.Base(exe) != "ssh" {
		return 0, "", nil, fmt.Errorf("SSH process changed")
	}
	f, err := os.Open(filepath.Join(base, "cmdline"))
	if err != nil {
		return 0, "", nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (2<<20)+1))
	if err != nil || len(data) > 2<<20 || len(data) == 0 || data[len(data)-1] != 0 {
		return 0, "", nil, fmt.Errorf("invalid SSH arguments")
	}
	for _, part := range strings.Split(string(data[:len(data)-1]), "\x00") {
		argv = append(argv, part)
	}
	again, _, _, err := processIdentity(pid)
	if err != nil || again != ref {
		return 0, "", nil, fmt.Errorf("SSH process changed")
	}
	return parent, name, argv, nil
}
