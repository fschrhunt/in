package main

import (
	"fmt"
	"net"
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

// browserOwners snapshots ancestors for a callback that has not started listening yet.
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
		if strings.HasPrefix(name, "sshd") || strings.HasPrefix(name, "tmux") || name == "mosh-server" || name == "tailscaled" || name == "etterminal" || name == "etserver" {
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

// socketIdentity decodes a pinned inode and its canonical numeric loopback address.
// A process reference returns nil; malformed socket evidence is always an error.
func socketIdentity(value string) (*listenerInfo, error) {
	text, ok := strings.CutPrefix(value, "socket:")
	if !ok {
		return nil, nil
	}
	inode, host, ok := strings.Cut(text, "@")
	n, err := strconv.ParseUint(inode, 10, 64)
	ip := net.ParseIP(host)
	if !ok || err != nil || n == 0 || strconv.FormatUint(n, 10) != inode || ip == nil || !ip.IsLoopback() || ip.String() != host {
		return nil, fmt.Errorf("invalid callback socket identity")
	}
	return &listenerInfo{host: host, inode: inode}, nil
}

// validOwnership accepts either a pinned socket or bounded process identities for a late listener.
func validOwnership(value string) error {
	pinned, err := socketIdentity(value)
	if err != nil || pinned != nil {
		return err
	}
	_, err = parseOwners(value)
	return err
}

// callbackOwner pins an existing private listener declared by the OAuth URL, regardless of process layout.
// A listener that starts after the browser hook returns must be held by a captured launching ancestor.
func callbackOwner(ep endpoint) (string, error) {
	listener, err := callbackListener(ep.Host, ep.Port)
	if err != nil {
		return "", err
	}
	if listener != nil {
		return "socket:" + listener.inode + "@" + listener.host, nil
	}
	return browserOwners()
}

// listenerOwned checks a pinned socket, or requires a captured live ancestor for late startup.
// Socket discovery separately enforces the current account and private loopback address.
func listenerOwned(inode, owners string) bool {
	pinned, err := socketIdentity(owners)
	if err != nil || pinned != nil {
		return err == nil && pinned.inode == inode
	}
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

// rcloneOwner pins the verified direct login flow to the rclone process holding its listener.
// This is a single observed exception, not a registry of arbitrary command adapters.
func rcloneOwner(raw string, ep endpoint, owners string) (string, error) {
	if !rcloneAuthURL(raw) {
		return "", unrecognizedLogin()
	}
	listener, err := callbackListener(ep.Host, ep.Port)
	if err != nil || listener == nil {
		return "", unrecognizedLogin()
	}
	refs, err := parseOwners(owners)
	if err != nil {
		return "", err
	}
	for _, ref := range refs {
		current, _, _, err := processIdentity(ref.pid)
		if err != nil || current != ref {
			continue
		}
		exe, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(ref.pid), "exe"))
		if err != nil || filepath.Base(exe) != "rclone" {
			continue
		}
		owner := strconv.Itoa(ref.pid) + ":" + ref.start
		if listenerOwned(listener.inode, owner) {
			return owner, nil
		}
	}
	return "", unrecognizedLogin()
}
