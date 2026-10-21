package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// peer describes an existing SSH master, not a separate credential or host inventory.
type peer struct{ Host, Control, Config, Remote, Session string }

// coordinatorRequest carries resolved hook settings when a ControlPath is reused.
type coordinatorRequest struct {
	Action string
	Peer   peer
}

// coordinatorRefresh acknowledges only after the manager adopts the new authenticated hook.
type coordinatorRefresh struct {
	peer     peer
	accepted chan struct{}
}

// peerArgs reads the owned connection-hook arguments without accepting shell commands.
func peerArgs(args []string) (peer, error) {
	var p peer
	var key, expected string
	f := flag.NewFlagSet("connect", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.StringVar(&p.Host, "host", "", "")
	f.StringVar(&p.Control, "control", "", "")
	f.StringVar(&p.Config, "config", "", "")
	f.StringVar(&key, "key", "", "")
	f.StringVar(&expected, "expected-key", "", "")
	f.StringVar(&p.Remote, "remote", ".local/share/in/bin/in", "")
	if f.Parse(args) != nil || f.NArg() != 0 || p.Host == "" || p.Control == "" || p.Control == "none" {
		return p, fmt.Errorf("invalid SSH connection hook")
	}
	if key != "" {
		control, config, found, err := parentControl(key)
		if err != nil {
			return p, err
		}
		if found {
			p.Control = control
			if config != "" {
				p.Config = config
			}
		} else if expected != "" && key != expected {
			return p, fmt.Errorf("SSH connection overrides do not match the configured control socket")
		}
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
	p, err := peerArgs(args)
	if err != nil {
		return err
	}
	// A new authenticated master may reuse the old ControlPath while its old
	// coordinator is still unwinding. Refresh that coordinator instead of losing the hook.
	root, err := stateDir()
	if err != nil {
		return err
	}
	stamp, err := startupStamp(root)
	if err != nil {
		return err
	}
	c, e := net.DialTimeout("unix", filepath.Join(root, "l-"+digest(p.Control)+".sock"), 200*time.Millisecond)
	if e == nil {
		c.SetDeadline(time.Now().Add(time.Second))
		e = json.NewEncoder(c).Encode(coordinatorRequest{Action: "refresh", Peer: p})
		var reply [3]byte
		if e == nil {
			_, e = io.ReadFull(c, reply[:])
		}
		c.Close()
		if e == nil && string(reply[:]) == "ok\n" {
			return nil
		}
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
	childArgs := []string{"attach", "--host", p.Host, "--control", p.Control, "--remote", p.Remote}
	if p.Config != "" {
		childArgs = append(childArgs, "--config", p.Config)
	}
	cmd := exec.Command(exe, childArgs...)
	cmd.Env = withStartupStampEnv(os.Environ(), stamp)
	cmd.Stdin = null
	cmd.Stdout = null
	cmd.Stderr = null
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("cannot start laptop bridge")
	}
	return cmd.Process.Release()
}

// attach owns a control socket and refreshes its session only after an authenticated connection hook.
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
	var owner *os.File
	until := time.Now().Add(3 * time.Second)
	for {
		owner, err = lock(base + ".lock")
		if err == nil {
			break
		}
		if time.Now().After(until) {
			return nil
		}
		select {
		case <-parent.Done():
			return nil
		case <-time.After(20 * time.Millisecond):
		}
	}
	defer owner.Close()
	p.Session, err = localSession(root, p)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	listener, err := socketListener(base + ".sock")
	if err != nil {
		return err
	}
	defer listener.Close()
	gate.Close()
	refresh := make(chan coordinatorRefresh)
	controlPath := p.Control
	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			_ = c.SetDeadline(time.Now().Add(time.Second))
			reader := bufio.NewScanner(c)
			reader.Buffer(make([]byte, 1024), 16*1024)
			if reader.Scan() {
				line := reader.Text()
				if line == "stop" {
					cancel()
				} else {
					var request coordinatorRequest
					if json.Unmarshal([]byte(line), &request) == nil && request.Action == "refresh" && request.Peer.Control == controlPath && request.Peer.Host != "" && request.Peer.Remote != "" {
						event := coordinatorRefresh{peer: request.Peer, accepted: make(chan struct{})}
						select {
						case refresh <- event:
							select {
							case <-event.accepted:
								_, _ = io.WriteString(c, "ok\n")
							case <-ctx.Done():
							}
						case <-ctx.Done():
						}
					}
				}
			}
			c.Close()
		}
	}()
	for {
		sessionCtx, stopSession := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func(current peer) { done <- attachSession(sessionCtx, current) }(p)
		var event coordinatorRefresh
		select {
		case <-ctx.Done():
			stopSession()
			<-done
			return nil
		case event = <-refresh:
			stopSession()
			<-done
		case err := <-done:
			stopSession()
			select {
			case event = <-refresh:
			default:
				return err
			}
		}
		next := event.peer
		next.Session, err = localSession(root, next)
		if err != nil {
			return err
		}
		p = next
		close(event.accepted)
	}
}

// attachSession scopes every callback to one authenticated master generation.
func attachSession(parent context.Context, p peer) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
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
	if w.send(message{Kind: "bind", Session: p.Session}) != nil {
		return fmt.Errorf("remote bridge unavailable")
	}
	hello, err := w.receive()
	readyTimer.Stop()
	if err != nil || hello.Kind != "ready" {
		return fmt.Errorf("remote bridge unavailable; install in remotely and reconnect")
	}
	return serveRequests(ctx, w, p)
}

// serveRequests handles browser requests over an authenticated, session-bound transport.
func serveRequests(ctx context.Context, w *wire, transport callbackTransport) error {
	b := newBridge(ctx, transport)
	defer b.close()
	seen := map[string]time.Time{}
	for {
		m, err := w.receive()
		if err != nil {
			return nil
		}
		if m.Kind == "ping" {
			if w.send(message{Kind: "pong"}) != nil {
				return nil
			}
			continue
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
