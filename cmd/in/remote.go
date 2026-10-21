package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// connection identifies a transport, or the sole attached tmux client's current transport.
func connection(ctx context.Context) (string, error) {
	value := os.Getenv("SSH_CONNECTION")
	if pane := os.Getenv("TMUX_PANE"); os.Getenv("TMUX") != "" && pane != "" {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		session, err := exec.CommandContext(ctx, "tmux", "display-message", "-p", "-t", pane, "#{session_id}").Output()
		if err != nil {
			return "", fmt.Errorf("cannot identify this tmux session")
		}
		target := strings.TrimSpace(string(session))
		clients, err := exec.CommandContext(ctx, "tmux", "list-clients", "-t", target, "-F", "#{client_name}").Output()
		if err != nil || len(strings.Fields(string(clients))) != 1 {
			return "", fmt.Errorf("tmux needs exactly one attached client to route a browser request")
		}
		env, err := exec.CommandContext(ctx, "tmux", "show-environment", "-t", target, "SSH_CONNECTION").Output()
		if err != nil {
			return "", fmt.Errorf("tmux has no current SSH connection; reattach from a connected terminal")
		}
		value = strings.TrimPrefix(strings.TrimSpace(string(env)), "SSH_CONNECTION=")
	}
	fields := strings.Fields(value)
	if len(fields) != 4 || net.ParseIP(fields[0]) == nil || net.ParseIP(fields[2]) == nil {
		return "", fmt.Errorf("no current SSH connection for this browser request")
	}
	return strings.Join(fields, " "), nil
}

// browse submits exactly once; its acknowledgement means the laptop owns the request, not login success.
func browse(ctx context.Context, raw string) error {
	ep, err := callback(raw)
	if err != nil {
		return err
	}
	if ep != nil && ep.Direct && !rcloneAuthURL(raw) {
		return unrecognizedLogin()
	}
	owners := ""
	rclone := false
	if ep != nil {
		if ep.Direct {
			owners, err = browserOwners()
			if err != nil {
				return err
			}
			owners, err = rcloneOwner(raw, *ep, owners)
			rclone = true
		} else {
			owners, err = callbackOwner(*ep)
		}
		if err != nil {
			return err
		}
	}
	tuple, err := connection(ctx)
	if err != nil {
		return err
	}
	root, err := stateDir()
	if err != nil {
		return err
	}
	path := filepath.Join(root, "r-"+digest(tuple)+".sock")
	var conn net.Conn
	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, err = net.DialTimeout("unix", path, 200*time.Millisecond)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("laptop bridge is unavailable; reconnect SSH or check in status on your laptop")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	w := newWire(conn, conn)
	id := requestID()
	if w.send(message{Kind: "open", ID: id, URL: raw, Owners: owners, Rclone: rclone}) != nil {
		return fmt.Errorf("browser request could not be delivered")
	}
	reply, err := w.receive()
	if err != nil {
		return fmt.Errorf("browser request outcome is unknown; check the laptop before retrying")
	}
	if reply.ID != id || reply.Kind != "ack" {
		return fmt.Errorf("invalid browser acknowledgement")
	}
	if reply.Error != "" {
		return fmt.Errorf("%s", reply.Error)
	}
	return nil
}

// remoteRelay exposes one private browser socket per authenticated SSH transport.
func remoteRelay(parent context.Context) error {
	tuple := os.Getenv("SSH_CONNECTION")
	if len(strings.Fields(tuple)) != 4 {
		return fmt.Errorf("relay requires an SSH transport")
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
	base := filepath.Join(root, "r-"+digest(tuple))
	owner, err := lock(base + ".lock")
	if err != nil {
		return fmt.Errorf("this SSH transport already has a relay")
	}
	defer owner.Close()
	listener, err := socketListener(base + ".sock")
	if err != nil {
		return err
	}
	defer listener.Close()
	gate.Close()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	w := newWire(os.Stdin, os.Stdout)
	if err = w.send(message{Kind: "ready"}); err != nil {
		return err
	}
	var mu sync.Mutex
	pending := map[string]chan message{}
	go func() {
		defer cancel()
		for {
			m, err := w.receive()
			if err != nil {
				return
			}
			mu.Lock()
			ch := pending[m.ID]
			mu.Unlock()
			if ch != nil {
				select {
				case ch <- m:
				default:
				}
			}
		}
	}()
	go func() { <-ctx.Done(); listener.Close() }()
	slots := make(chan struct{}, requestLimit)
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		c, err := listener.Accept()
		if err != nil {
			return nil
		}

		// Parse the bounded first frame before admission so a full relay still accepts stop.
		c.SetDeadline(time.Now().Add(time.Second))
		cw := newWire(c, c)
		m, err := cw.receive()
		if err != nil {
			c.Close()
			continue
		}
		if m.Kind == "stop" {
			c.Close()
			cancel()
			return nil
		}
		select {
		case slots <- struct{}{}:
		default:
			c.Close()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			defer c.Close()
			c.SetDeadline(time.Now().Add(6 * time.Second))
			if m.Kind == "watch" {
				// Keep callback helpers tied to this relay even if an SSH mux retains their stdio.
				c.SetDeadline(time.Time{})
				stop := context.AfterFunc(ctx, func() { c.Close() })
				defer stop()
				if cw.send(message{Kind: "ready"}) == nil {
					_, _ = io.Copy(io.Discard, c)
				}
				return
			}
			if err != nil || m.Kind != "open" || len(m.ID) != 32 {
				return
			}
			ch := make(chan message, 1)
			mu.Lock()
			_, exists := pending[m.ID]
			if !exists {
				pending[m.ID] = ch
			}
			mu.Unlock()
			if exists {
				return
			}
			defer func() { mu.Lock(); delete(pending, m.ID); mu.Unlock() }()
			if err = w.send(m); err != nil {
				return
			}
			select {
			case reply := <-ch:
				_ = cw.send(reply)
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
			}
		}()
	}
}

// remotePipe connects once, signals readiness, then carries uninterpreted callback bytes.
func remotePipe(parent context.Context, host, port, owners string) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	if err := validOwnership(owners); err != nil {
		return err
	}
	if err := validEndpoint(host, port); err != nil {
		return err
	}
	watch, err := watchRelay(ctx, cancel)
	if err != nil {
		return err
	}
	defer watch.Close()
	deadline := time.Now().Add(10 * time.Second)
	var conn net.Conn
	var selected *listenerInfo
	for conn == nil {
		listener, err := callbackListener(host, port)
		if err != nil {
			return err
		}
		if listener != nil {
			if !listenerOwned(listener.inode, owners) {
				return fmt.Errorf("callback listener no longer matches the browser request")
			}
			c, err := (&net.Dialer{Timeout: 200 * time.Millisecond}).DialContext(ctx, "tcp", net.JoinHostPort(listener.host, port))
			if err == nil {
				current, checkErr := callbackListener(host, port)
				if checkErr != nil || current == nil || current.inode != listener.inode || !listenerOwned(current.inode, owners) {
					c.Close()
					return fmt.Errorf("callback listener changed during connection")
				}
				conn = c
				selected = listener
				break
			}
		}
		if listener == nil && strings.HasPrefix(owners, "socket:") {
			return fmt.Errorf("requested callback listener has closed")
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("remote callback listener did not become ready")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Minute))
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if _, err := io.WriteString(os.Stdout, "READY\n"); err != nil {
		return err
	}

	go func() {
		_, _ = io.Copy(conn, os.Stdin)
		if tcp, ok := conn.(*net.TCPConn); ok {
			tcp.CloseWrite()
		}
	}()
	_, _ = io.Copy(os.Stdout, conn)
	// Observe listener shutdown without consuming another connection or reading OAuth.
	until := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(until) {
		current, err := callbackListener(host, port)
		if err == nil && (current == nil || current.inode != selected.inode) {
			_, _ = io.WriteString(os.Stderr, "IN_CALLBACK_CLOSED\n")
			break
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(25 * time.Millisecond):
		}
	}
	return nil
}

// watchRelay cancels a stream when its transport relay stops, independent of inherited SSH pipes.
func watchRelay(ctx context.Context, cancel context.CancelFunc) (net.Conn, error) {
	root, err := stateDir()
	if err != nil {
		return nil, err
	}
	tuple := os.Getenv("SSH_CONNECTION")
	c, err := net.DialTimeout("unix", filepath.Join(root, "r-"+digest(tuple)+".sock"), time.Second)
	if err != nil {
		return nil, fmt.Errorf("callback transport relay is unavailable")
	}
	c.SetDeadline(time.Now().Add(time.Second))
	w := newWire(c, c)
	if w.send(message{Kind: "watch"}) != nil {
		c.Close()
		return nil, fmt.Errorf("cannot watch callback transport")
	}
	m, err := w.receive()
	if err != nil || m.Kind != "ready" {
		c.Close()
		return nil, fmt.Errorf("callback transport is stopping or busy")
	}
	c.SetDeadline(time.Time{})
	go func() {
		stop := context.AfterFunc(ctx, func() { c.Close() })
		defer stop()
		defer cancel()
		var one [1]byte
		_, _ = c.Read(one[:])
	}()
	return c, nil
}
