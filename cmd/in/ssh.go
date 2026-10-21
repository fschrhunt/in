package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// peer describes an existing SSH master, not a separate credential or host inventory.
type peer struct{ Host, Control, Config, Remote string }

// peerArgs reads the owned connection-hook arguments without accepting shell commands.
func peerArgs(args []string) (peer, error) {
	var p peer
	f := flag.NewFlagSet("connect", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.StringVar(&p.Host, "host", "", "")
	f.StringVar(&p.Control, "control", "", "")
	f.StringVar(&p.Config, "config", "", "")
	f.StringVar(&p.Remote, "remote", ".local/share/in/bin/in", "")
	if f.Parse(args) != nil || f.NArg() != 0 || p.Host == "" || p.Control == "" || p.Control == "none" {
		return p, fmt.Errorf("invalid SSH connection hook")
	}
	return p, nil
}

// sshCommand opens a channel on the selected master and forbids a fallback network login.
func (p peer) sshCommand(ctx context.Context, command ...string) *exec.Cmd {
	args := []string{}
	if p.Config != "" {
		args = append(args, "-F", p.Config)
	}
	args = append(args, "-S", p.Control, "-T", "-o", "ControlMaster=no", "-o", "ControlPersist=no", "-o", "PermitLocalCommand=no", "-o", "RemoteCommand=none", "-o", "BatchMode=yes", "-o", "ProxyJump=none", "-o", "ProxyCommand=false", "-o", "ClearAllForwardings=yes", "--", p.Host)
	remote := shellQuote(p.Remote)
	// Relative installed paths are resolved by the remote login shell from its home.
	if !filepath.IsAbs(p.Remote) {
		remote = "\"$HOME\"/" + shellQuote(p.Remote)
	}
	for _, arg := range command {
		remote += " " + shellQuote(arg)
	}
	args = append(args, remote)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.WaitDelay = time.Second
	return cmd
}

// connect detaches the coordinator so LocalCommand never waits for the login lifecycle.
func connect(args []string) error {
	_, err := peerArgs(args)
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer null.Close()
	cmd := exec.Command(exe, append([]string{"attach"}, args...)...)
	cmd.Stdin = null
	cmd.Stdout = null
	cmd.Stderr = null
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("cannot start laptop bridge")
	}
	return cmd.Process.Release()
}

// attach owns one coordinator per SSH master and expires all callbacks on disconnect.
func attach(parent context.Context, args []string) error {
	p, err := peerArgs(args)
	if err != nil {
		return err
	}
	root, err := stateDir()
	if err != nil {
		return err
	}
	gate, err := startupGate(parent, root)
	if err != nil {
		return err
	}
	defer gate.Close()
	base := filepath.Join(root, "l-"+digest(p.Control))
	owner, err := lock(base + ".lock")
	if err != nil {
		return nil
	}
	defer owner.Close()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	listener, err := socketListener(base + ".sock")
	if err != nil {
		return err
	}
	defer listener.Close()
	gate.Close()
	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			_ = c.SetDeadline(time.Now().Add(time.Second))
			buf := make([]byte, 16)
			n, _ := c.Read(buf)
			if strings.TrimSpace(string(buf[:n])) == "stop" {
				cancel()
			}
			c.Close()
		}
	}()
	cmd := p.sshCommand(ctx, "relay")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = io.Discard
	stopIO := context.AfterFunc(ctx, func() { stdin.Close(); stdout.Close() })
	defer stopIO()
	if cmd.Start() != nil {
		return fmt.Errorf("cannot join the existing SSH connection")
	}
	defer func() { cancel(); _ = cmd.Wait() }()
	// Joining during LocalCommand is asynchronous; bound the initial handshake.
	readyTimer := time.AfterFunc(5*time.Second, cancel)
	w := newWire(stdout, stdin)
	hello, err := w.receive()
	readyTimer.Stop()
	if err != nil || hello.Kind != "ready" {
		return fmt.Errorf("remote bridge unavailable; install in remotely and reconnect")
	}
	b := newBridge(ctx, p)
	defer b.close()
	seen := map[string]time.Time{}
	for {
		m, err := w.receive()
		if err != nil {
			return nil
		}
		if m.Kind != "open" || len(m.ID) != 32 {
			return fmt.Errorf("invalid browser request")
		}
		now := time.Now()
		for id, until := range seen {
			if now.After(until) {
				delete(seen, id)
			}
		}
		reply := message{Kind: "ack", ID: m.ID}
		if _, exists := seen[m.ID]; exists {
			reply.Error = "duplicate browser request; check the laptop before retrying"
		} else if len(seen) >= 128 {
			reply.Error = "too many browser requests; wait for pending logins"
		} else {
			seen[m.ID] = now.Add(3 * time.Minute)
			launch, err := b.prepare(m.URL, m.Owners, m.Rclone)
			if err != nil {
				reply.Error = err.Error()
			} else {
				if w.send(reply) != nil {
					return nil
				}
				launch()
				continue
			}
		}
		if w.send(reply) != nil {
			return nil
		}
		notify(reply.Error)
	}
}

// shellQuote preserves literal arguments in the one remote command string required by SSH.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
