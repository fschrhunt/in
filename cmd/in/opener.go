package main

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// open captures browser URLs only in the guarded SSH environment and delegates other xdg-open calls unchanged.
func open(ctx context.Context, args []string) error {
	root := os.Getenv("IN_HOME")
	browser := os.Getenv("BROWSER")
	guarded := os.Getenv("SSH_CONNECTION") != "" && (browser == "" || (root != "" && browser == filepath.Join(root, "bin", "browser")))
	if guarded && len(args) == 1 && capturedOpenURL(args[0]) {
		return browse(ctx, args[0])
	}
	return nativeOpen(ctx, args)
}

// capturedOpenURL selects HTTP(S) opens, leaving direct loopback pages to native xdg-open except rclone's verified entry point.
func capturedOpenURL(raw string) bool {
	if len(raw) == 0 || len(raw) > 64*1024 || strings.ContainsAny(raw, "\x00\r\n") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || net.ParseIP(host).IsLoopback() {
		return rcloneAuthURL(raw)
	}
	return true
}

// nativeOpen runs the system opener with original arguments after removing this installation's inherited hooks.
func nativeOpen(ctx context.Context, args []string) error {
	root := os.Getenv("IN_HOME")
	if root == "" {
		var err error
		root, err = stateDir()
		if err != nil {
			return fmt.Errorf("native xdg-open unavailable")
		}
	}
	ownedOpener := filepath.Join(root, "openers")
	ownedBrowser := filepath.Join(root, "bin", "browser")
	env := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		switch key {
		case "PATH":
			parts := strings.Split(value, ":")
			clean := parts[:0]
			for _, part := range parts {
				if part != ownedOpener {
					clean = append(clean, part)
				}
			}
			path := strings.Join(clean, ":")
			if path == "" {
				path = "/usr/bin:/bin"
			}
			env = append(env, "PATH="+path)
		case "BROWSER":
			if value != ownedBrowser {
				env = append(env, entry)
			}
		default:
			env = append(env, entry)
		}
	}
	// The shell resolves xdg-open against the cleaned PATH; "$@" keeps every argument byte intact.
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", `exec xdg-open "$@"`, "xdg-open")
	cmd.Args = append(cmd.Args, args...)
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("native xdg-open failed")
	}
	return nil
}
