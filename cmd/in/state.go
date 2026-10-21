package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// stateDir returns the private application directory, with an override for isolated installs.
func stateDir() (string, error) {
	root := os.Getenv("IN_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(home, ".local", "share", "in")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if err = privateDir(root); err != nil {
		return "", err
	}
	return root, nil
}

// privateDir refuses symlinks and directories accessible to another account.
func privateDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return fmt.Errorf("cannot create private application directory")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("application directory must be an owned private directory (mode 700)")
	}
	if st, ok := info.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Getuid() {
		return fmt.Errorf("application directory belongs to another account")
	}
	return nil
}

// digest names sockets without storing connection addresses in filenames.
func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:12])
}

// requestID creates a fresh request identity, unrelated to OAuth state.
func requestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("random source unavailable")
	}
	return hex.EncodeToString(b[:])
}

// lock obtains a process-lifetime advisory lock without stealing another process's ownership.
func lock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// socketListener removes stale socket files only while the caller owns its matching lock.
func socketListener(path string) (net.Listener, error) {
	if len(path) > 100 {
		return nil, fmt.Errorf("application path is too long for a private socket; choose a shorter IN_HOME")
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("refusing to replace a non-socket")
		}
		if err = os.Remove(path); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		listener.Close()
		return nil, err
	}
	return listener, nil
}

// saveDiagnostic keeps only the latest bounded diagnostic; callers must never pass request data.
func saveDiagnostic(message string) {
	root, err := stateDir()
	if err != nil {
		return
	}
	if len(message) > 1024 {
		message = "bridge failed; diagnostic exceeded size limit"
	}
	_ = atomicWrite(filepath.Join(root, "last-error"), []byte(message+"\n"), 0600)
}

// startupGate serializes runtime registration with setup/uninstall and rejects removed installs.
func startupGate(parent context.Context, root string) (*os.File, error) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	for {
		gate, err := lock(filepath.Join(root, "setup.lock"))
		if err == nil {
			installation, err := readInstallation(root)
			if err != nil || len(installation.Files) == 0 {
				gate.Close()
				return nil, fmt.Errorf("integration is not installed; run setup before connecting")
			}
			return gate, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return nil, fmt.Errorf("cannot coordinate bridge startup")
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("setup or uninstall is still running")
		case <-time.After(20 * time.Millisecond):
		}
	}
}
