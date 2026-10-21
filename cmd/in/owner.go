package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// processRef pins a browser ancestor to its PID and Linux start time, excluding PID reuse.
type processRef struct {
	pid   int
	start string
}

// processIdentity reads non-secret process metadata for the current account only.
func processIdentity(pid int) (processRef, int, string, error) {
	path := filepath.Join("/proc", strconv.Itoa(pid), "stat")
	info, err := os.Stat(path)
	if err != nil {
		return processRef{}, 0, "", err
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Getuid() {
		return processRef{}, 0, "", fmt.Errorf("process belongs to another account")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return processRef{}, 0, "", err
	}
	// comm is parenthesized and may itself contain spaces or closing parentheses.
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return processRef{}, 0, "", fmt.Errorf("invalid process metadata")
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 20 {
		return processRef{}, 0, "", fmt.Errorf("invalid process metadata")
	}
	parent, err := strconv.Atoi(fields[1])
	begin := strings.IndexByte(string(data), '(')
	if begin < 0 || begin >= end {
		return processRef{}, 0, "", fmt.Errorf("invalid process metadata")
	}
	return processRef{pid, fields[19]}, parent, string(data[begin+1 : end]), err
}

// browserOwners snapshots the browser launcher's ancestors while the original CLI is alive.
// A detached service or sibling listener has no demonstrated ownership and is unsupported.
func browserOwners() (string, error) {
	var refs []string
	pid := os.Getppid()
	for len(refs) < 32 && pid > 1 {
		ref, parent, name, err := processIdentity(pid)
		if err != nil {
			break
		}
		// A detached opener may start a new POSIX session while retaining its CLI parent.
		// Stop at supported terminal servers rather than equating session ID with launch ownership.
		if strings.HasPrefix(name, "sshd") || strings.HasPrefix(name, "tmux") || name == "mosh-server" || name == "tailscaled" {
			break
		}
		refs = append(refs, strconv.Itoa(ref.pid)+":"+ref.start)
		if parent == pid {
			break
		}
		pid = parent
	}
	if len(refs) == 0 {
		return "", fmt.Errorf("cannot establish browser request ownership on Linux")
	}
	return strings.Join(refs, ","), nil
}

// parseOwners bounds and validates the opaque process identities carried through SSH.
func parseOwners(value string) ([]processRef, error) {
	parts := strings.Split(value, ",")
	if len(parts) > 32 || len(value) > 2048 {
		return nil, fmt.Errorf("invalid browser process ownership")
	}
	var refs []processRef
	for _, part := range parts {
		pidText, start, ok := strings.Cut(part, ":")
		pid, err := strconv.Atoi(pidText)
		_, startErr := strconv.ParseUint(start, 10, 64)
		if !ok || err != nil || pid <= 1 || startErr != nil {
			return nil, fmt.Errorf("invalid browser process ownership")
		}
		refs = append(refs, processRef{pid, start})
	}
	return refs, nil
}

// listenerOwned requires a captured, still-live CLI ancestor to hold this exact socket.
func listenerOwned(inode, owners string) bool {
	refs, err := parseOwners(owners)
	if err != nil {
		return false
	}
	for _, ref := range refs {
		current, _, _, err := processIdentity(ref.pid)
		if err != nil || current != ref {
			continue
		}
		dir := filepath.Join("/proc", strconv.Itoa(ref.pid), "fd")
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			target, err := os.Readlink(filepath.Join(dir, entry.Name()))
			if err == nil && target == "socket:["+inode+"]" {
				// Check identity again after reading descriptors, before trusting their owner.
				current, _, _, err = processIdentity(ref.pid)
				return err == nil && current == ref
			}
		}
	}
	return false
}
