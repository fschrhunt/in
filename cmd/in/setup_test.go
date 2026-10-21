package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRemoteSetupIsReversible preserves user edits around owned shell integration.
func TestRemoteSetupIsReversible(t *testing.T) {
	root := filepath.Join(t.TempDir(), "in")
	t.Setenv("IN_HOME", root)
	shell := filepath.Join(t.TempDir(), "rc")
	original := "# user's config\nexport EXAMPLE=yes\n"
	os.WriteFile(shell, []byte(original), 0600)
	args := []string{"remote", "--shell-file", shell}
	if err := setup(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	if err := setup(context.Background(), args); err != nil {
		t.Fatal("repeated setup:", err)
	}
	data, _ := os.ReadFile(shell)
	if strings.Count(string(data), "# >>> in:remote >>>") != 1 {
		t.Fatal("duplicate integration")
	}
	os.WriteFile(shell, append(data, []byte("# added later\n")...), 0600)
	if err := uninstall(); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(shell)
	if string(data) != original+"# added later\n" {
		t.Fatal("uninstall lost unrelated shell changes")
	}
}

// TestUninstallRefusesModifiedOwnedBlock prevents removing user-modified integration.
func TestUninstallRefusesModifiedOwnedBlock(t *testing.T) {
	t.Setenv("IN_HOME", filepath.Join(t.TempDir(), "in"))
	shell := filepath.Join(t.TempDir(), "rc")
	if err := setup(context.Background(), []string{"remote", "--shell-file", shell}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(shell)
	changed := strings.Replace(string(data), "[ ! -r", "[ -r", 1)
	os.WriteFile(shell, []byte(changed), 0600)
	if err := uninstall(); err == nil {
		t.Fatal("modified block was removed")
	}
	got, _ := os.ReadFile(shell)
	if string(got) != changed {
		t.Fatal("configuration was modified on refusal")
	}
}

// TestApplyEditsDetectsConcurrentChange avoids overwriting a changed configuration snapshot.
func TestApplyEditsDetectsConcurrentChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rc")
	os.WriteFile(path, []byte("user update"), 0600)
	err := applyEdits([]edit{{path: path, before: []byte("old"), after: []byte("ours"), mode: 0600, existed: true}})
	if err == nil {
		t.Fatal("stale snapshot accepted")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "user update" {
		t.Fatal("user edit lost")
	}
}

// TestUninstallMultipleHostsRemovesEveryOwnedInclude preserves unrelated config in a shared file.
func TestUninstallMultipleHostsRemovesEveryOwnedInclude(t *testing.T) {
	t.Setenv("IN_HOME", filepath.Join(t.TempDir(), "in"))
	dir := t.TempDir()
	config := filepath.Join(dir, "config")
	rc := filepath.Join(dir, "rc")
	original := "Host one two\n  HostName example.invalid\n  User fixture\n  ControlPersist 600\n"
	os.WriteFile(config, []byte(original), 0600)
	for _, host := range []string{"one", "two"} {
		if err := setup(context.Background(), []string{"laptop", "--shell-file", rc, "--ssh-config", config, host}); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(config)
	os.WriteFile(config, append(data, []byte("# user addition\n")...), 0600)
	if err := uninstall(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(config)
	if string(got) != original+"# user addition\n" {
		t.Fatal("not all owned includes removed, or user config changed")
	}
}

// TestApplyEditsRollsBackWriteFailure prevents partial setup from altering an earlier config file.
func TestApplyEditsRollsBackWriteFailure(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first")
	blocker := filepath.Join(dir, "not-a-directory")
	os.WriteFile(first, []byte("original"), 0600)
	os.WriteFile(blocker, []byte("blocker"), 0600)
	err := applyEdits([]edit{{first, []byte("original"), []byte("changed"), 0600, true}, {filepath.Join(blocker, "file"), nil, []byte("new"), 0600, false}})
	if err == nil {
		t.Fatal("write failure was ignored")
	}
	data, _ := os.ReadFile(first)
	if string(data) != "original" {
		t.Fatal("partial configuration was left behind")
	}
}

// TestUninstallRefusesChangedStartMarker keeps a partially edited block and its supporting files intact.
func TestUninstallRefusesChangedStartMarker(t *testing.T) {
	root := filepath.Join(t.TempDir(), "in")
	t.Setenv("IN_HOME", root)
	shell := filepath.Join(t.TempDir(), "rc")
	if err := setup(context.Background(), []string{"remote", "--shell-file", shell}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(shell)
	changed := strings.Replace(string(data), "# >>> in:remote >>>", "# edited marker", 1)
	os.WriteFile(shell, []byte(changed), 0600)
	if err := uninstall(); err == nil {
		t.Fatal("partially edited block treated as already removed")
	}
	if _, err := os.Stat(filepath.Join(root, "bin", "in")); err != nil {
		t.Fatal("supporting binary removed")
	}
}

// TestStopWaitsForCoordinatorStartup closes the lock-before-socket window during uninstall.
func TestStopWaitsForCoordinatorStartup(t *testing.T) {
	// Short paths also fit macOS Unix socket limits.
	root, err := os.MkdirTemp("/tmp", "in-stop-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	t.Setenv("IN_HOME", root)
	path := filepath.Join(root, "l-fixture")
	owner, err := lock(path + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	done := make(chan error, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		listener, err := socketListener(path + ".sock")
		if err != nil {
			done <- err
			owner.Close()
			return
		}
		defer listener.Close()
		c, err := listener.Accept()
		if err == nil {
			c.SetDeadline(time.Now().Add(time.Second))
			data, _ := io.ReadAll(c)
			c.Close()
			if string(data) != "stop\n" {
				err = fmt.Errorf("stop request missing")
			}
		}
		owner.Close()
		done <- err
	}()
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("stop returned before coordinator exited")
	}
}

// TestStartupCannotCrossUninstall rejects late coordinators after their install is removed.
func TestStartupCannotCrossUninstall(t *testing.T) {
	root := filepath.Join(t.TempDir(), "in")
	t.Setenv("IN_HOME", root)
	if err := setup(context.Background(), []string{"remote", "--shell-file", filepath.Join(t.TempDir(), "rc")}); err != nil {
		t.Fatal(err)
	}
	gate, err := lock(filepath.Join(root, "setup.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close()
	result := make(chan error, 1)
	go func() {
		guard, err := startupGate(context.Background(), root)
		if guard != nil {
			guard.Close()
		}
		result <- err
	}()
	select {
	case <-result:
		t.Fatal("startup bypassed setup/uninstall lock")
	case <-time.After(50 * time.Millisecond):
	}
	if err := os.Remove(filepath.Join(root, "install.json")); err != nil {
		t.Fatal(err)
	}
	gate.Close()
	if err := <-result; err == nil {
		t.Fatal("late startup accepted removed installation")
	}
}

// TestStopInvalidatesDelayedStartup rejects a child launched before stop after the gate reopens.
func TestStopInvalidatesDelayedStartup(t *testing.T) {
	root := filepath.Join(t.TempDir(), "in")
	t.Setenv("IN_HOME", root)
	if err := setup(context.Background(), []string{"remote", "--shell-file", filepath.Join(t.TempDir(), "rc")}); err != nil {
		t.Fatal(err)
	}
	stamp, err := startupStamp(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IN_START_STAMP", stamp)
	gate, err := startupGate(context.Background(), root)
	if gate != nil {
		gate.Close()
	}
	if err == nil {
		t.Fatal("delayed startup crossed stop")
	}
}

// TestStopHoldsRegistrationGate keeps a pending registration out until its scan completes.
func TestStopHoldsRegistrationGate(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "in-gate-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	t.Setenv("IN_HOME", root)
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "install.json"), []byte(`{"Files":[{"Path":"fixture"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "l-fixture")
	owner, err := lock(path + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	listener, err := socketListener(path + ".sock")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	stopping := make(chan error, 1)
	go func() { stopping <- stop() }()
	var conn net.Conn
	conn, err = listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	stamp, err := startupStamp(root)
	if err != nil || stamp == "0" {
		t.Fatalf("stop did not fence registration: %q, %v", stamp, err)
	}
	registered := make(chan error, 1)
	go func() {
		gate, err := startupGate(context.Background(), root)
		if gate != nil {
			gate.Close()
		}
		registered <- err
	}()
	select {
	case <-registered:
		t.Fatal("registration bypassed stop gate")
	case <-time.After(50 * time.Millisecond):
	}
	owner.Close()
	if err := <-stopping; err != nil {
		t.Fatal(err)
	}
	if err := <-registered; err != nil {
		t.Fatal("fresh startup rejected after stop:", err)
	}
}

// TestStopIgnoresRelayStartupMutex only waits on registered relay identity locks.
func TestStopIgnoresRelayStartupMutex(t *testing.T) {
	root := filepath.Join(t.TempDir(), "in")
	t.Setenv("IN_HOME", root)
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	owner, err := lock(filepath.Join(root, "a-fixture.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	legacy, err := lock(filepath.Join(root, "r-fixture.start.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	done := make(chan error, 1)
	go func() { done <- stop() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("stop waited on a relay startup mutex")
	}
}

// TestRemoteSetupLeavesLocalTmuxAlone avoids enabling remote browsing solely because tmux exists.
func TestRemoteSetupLeavesLocalTmuxAlone(t *testing.T) {
	root := filepath.Join(t.TempDir(), "in")
	t.Setenv("IN_HOME", root)
	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("BROWSER", "")
	t.Setenv("TMUX", "local-session")
	rc := filepath.Join(t.TempDir(), "rc")
	if err := setup(context.Background(), []string{"remote", "--shell-file", rc}); err != nil {
		t.Fatal(err)
	}
	originalPath := os.Getenv("PATH")
	output, err := exec.Command("/bin/sh", "-c", `. "$1"; printf '%s\n%s' "${BROWSER:-}" "$PATH"`, "sh", rc).Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != "\n"+originalPath {
		t.Fatal("local tmux installed remote browser hooks")
	}
}

// TestRemoteSetupGuardsOpenersPATH checks SSH activation, user overrides and repeat sourcing.
func TestRemoteSetupGuardsOpenersPATH(t *testing.T) {
	root := filepath.Join(t.TempDir(), "in")
	t.Setenv("IN_HOME", root)
	rc := filepath.Join(t.TempDir(), "rc")
	if err := setup(context.Background(), []string{"remote", "--shell-file", rc}); err != nil {
		t.Fatal(err)
	}
	openers := filepath.Join(root, "openers")
	shim, err := os.ReadFile(filepath.Join(openers, "xdg-open"))
	if err != nil || !strings.Contains(string(shim), ` open "$@"`) {
		t.Fatal("owned xdg-open shim missing")
	}
	basePath := os.Getenv("PATH")
	t.Setenv("SSH_CONNECTION", "fixture")
	t.Setenv("BROWSER", "")
	command := `. "$1"; . "$1"; printf '%s\n%s' "$BROWSER" "$PATH"`
	output, err := exec.Command("/bin/sh", "-c", command, "sh", rc).Output()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "bin", "browser") + "\n" + openers + ":" + basePath
	if string(output) != want {
		t.Fatalf("remote activation: got %q, want %q", output, want)
	}
	t.Setenv("BROWSER", "user-browser")
	output, err = exec.Command("/bin/sh", "-c", command, "sh", rc).Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != "user-browser\n"+basePath {
		t.Fatal("explicit BROWSER override was replaced")
	}
	if err := uninstall(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(openers, "xdg-open")); !os.IsNotExist(err) {
		t.Fatal("uninstall retained the owned xdg-open shim")
	}
}

// TestSetupStopsOldWireBeforeUpgrade lets the old binary tear down its own protocol safely.
func TestSetupStopsOldWireBeforeUpgrade(t *testing.T) {
	root := filepath.Join(t.TempDir(), "in")
	t.Setenv("IN_HOME", root)
	shell := filepath.Join(t.TempDir(), "rc")
	args := []string{"remote", "--shell-file", shell}
	if err := setup(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(root, "bin", "in")
	marker := filepath.Join(root, "old-stopped")
	old := []byte("#!/bin/sh\n[ \"$1\" = stop ] || exit 1\nprintf stopped > " + shellQuote(marker) + "\n")
	if err := os.WriteFile(installed, old, 0700); err != nil {
		t.Fatal(err)
	}
	manifest, err := readInstallation(root)
	if err != nil {
		t.Fatal(err)
	}
	record(&manifest, installedFile{Path: installed, Hash: hashBytes(old)})
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(root, "install.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := setup(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("previous protocol was not stopped before upgrade")
	}
}
