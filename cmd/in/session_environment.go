package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// sessionEnvironment uses a sole attached tmux client's live environment, never an old pane's.
// Only the two routing variables are retained; other process environment values are discarded.
func sessionEnvironment(ctx context.Context) (tuple, tunnel string, err error) {
	tuple, tunnel = os.Getenv("SSH_CONNECTION"), os.Getenv("IN_SESSION_SOCKET")
	pane := os.Getenv("TMUX_PANE")
	if os.Getenv("TMUX") == "" {
		return tuple, tunnel, nil
	}
	if pane == "" {
		return "", "", fmt.Errorf("cannot identify this tmux pane")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, e := exec.CommandContext(ctx, "tmux", "display-message", "-p", "-t", pane, "#{session_id}").Output()
	if e != nil {
		return "", "", fmt.Errorf("cannot identify this tmux session")
	}
	out, e = exec.CommandContext(ctx, "tmux", "list-clients", "-t", strings.TrimSpace(string(out)), "-F", "#{client_pid}").Output()
	pids := strings.Fields(string(out))
	if e != nil || len(pids) != 1 {
		return "", "", fmt.Errorf("tmux needs exactly one attached client to route a browser request")
	}
	pid, e := strconv.Atoi(pids[0])
	if e != nil || pid <= 1 {
		return "", "", fmt.Errorf("cannot identify attached tmux client")
	}
	before, _, _, e := processIdentity(pid)
	if e != nil {
		return "", "", fmt.Errorf("cannot read attached tmux client ownership")
	}
	f, e := os.Open(filepath.Join("/proc", strconv.Itoa(pid), "environ"))
	if e != nil {
		return "", "", fmt.Errorf("cannot read attached tmux client routing")
	}
	data, e := io.ReadAll(io.LimitReader(f, 2*1024*1024+1))
	f.Close()
	after, _, _, checkErr := processIdentity(pid)
	if e != nil || checkErr != nil || before != after || len(data) > 2*1024*1024 {
		return "", "", fmt.Errorf("attached tmux client changed during routing")
	}
	tuple, tunnel = "", ""
	for _, entry := range strings.Split(string(data), "\x00") {
		key, value, _ := strings.Cut(entry, "=")
		switch key {
		case "SSH_CONNECTION":
			tuple = value
		case "IN_SESSION_SOCKET":
			tunnel = value
		}
	}
	return tuple, tunnel, nil
}

// retireRelay replaces only a known logical binding after a new authenticated connection.
// It stops the owned relay through its private socket; it never signals an SSH process.
func retireRelay(ctx context.Context, root, identity string) error {
	base := filepath.Join(root, "r-"+digest(identity))
	deadline := time.Now().Add(3 * time.Second)
	for {
		owner, err := lock(base + ".lock")
		if err == nil {
			owner.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("previous session relay is still stopping")
		}
		c, err := net.DialTimeout("unix", base+".sock", 100*time.Millisecond)
		if err == nil {
			_ = newWire(c, c).send(message{Kind: "stop"})
			c.Close()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}
