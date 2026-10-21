package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// connection selects an exact current session, including the sole attached tmux client.
func connection(ctx context.Context) (string, error) {
	tuple, tunnel, err := sessionEnvironment(ctx)
	if err != nil {
		return "", err
	}
	if tunnel != "" {
		if !filepath.IsAbs(tunnel) || len(tunnel) > 100 || strings.ContainsAny(tunnel, "\x00\r\n") {
			return "", fmt.Errorf("invalid private session socket")
		}
		return "tunnel:" + tunnel, nil
	}
	root, err := stateDir()
	if err != nil {
		return "", err
	}
	return sessionIdentity(root, tuple)
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
	if tunnel, ok := strings.CutPrefix(tuple, "tunnel:"); ok {
		if err := startTunnelRelay(ctx, tunnel); err != nil {
			return err
		}
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

// remoteRelay exposes one private browser socket per authenticated logical SSH session.
func remoteRelay(parent context.Context) error {
	tuple := os.Getenv("SSH_CONNECTION")
	if len(strings.Fields(tuple)) != 4 {
		return fmt.Errorf("relay requires an SSH transport")
	}
	w := newWire(os.Stdin, os.Stdout)
	timer := time.AfterFunc(5*time.Second, func() { os.Stdin.Close() })
	hello, err := w.receive()
	timer.Stop()
	if err != nil || hello.Kind != "bind" || len(hello.Session) != 32 {
		return fmt.Errorf("invalid session binding")
	}
	root, err := stateDir()
	if err != nil {
		return err
	}
	if err = bindSession(root, hello.Session, tuple); err != nil {
		return err
	}
	identity := "session:" + hello.Session
	if err = retireRelay(parent, root, identity); err != nil {
		return err
	}
	return relay(parent, identity, w, nil)
}

// relay serves a private browser socket over one authenticated session's control wire.
// handle admits transport-specific control messages; false leaves acknowledgement handling unchanged.
func relay(parent context.Context, identity string, w *wire, handle func(message) bool) error {
	root, err := stateDir()
	if err != nil {
		return err
	}
	gate, err := startupGate(parent, root)
	if err != nil {
		return err
	}
	defer gate.Close()
	base := filepath.Join(root, "r-"+digest(identity))
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
	if err = w.send(message{Kind: "ready"}); err != nil {
		return err
	}
	// Bound a detached agent's life after its authenticated stream becomes silent.
	// Heartbeats carry no URLs and never resubmit a browser request.
	watchdog := time.AfterFunc(90*time.Second, cancel)
	defer watchdog.Stop()
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			if w.send(message{Kind: "ping"}) != nil {
				cancel()
				return
			}
		}
	}()
	var mu sync.Mutex
	pending := map[string]chan message{}
	go func() {
		defer cancel()
		for {
			m, err := w.receive()
			if err != nil {
				return
			}
			watchdog.Reset(90 * time.Second)
			if m.Kind == "pong" {
				continue
			}
			if handle != nil && handle(m) {
				continue
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
func remotePipe(parent context.Context, host, port, owners, session string) error {
	if session != "" && !sessionID(session) {
		return fmt.Errorf("invalid session binding")
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	if err := validOwnership(owners); err != nil {
		return err
	}
	if err := validEndpoint(host, port); err != nil {
		return err
	}
	watch, err := watchRelay(ctx, cancel, session)
	if err != nil {
		return err
	}
	defer watch.Close()
	closed, err := callbackStream(ctx, host, port, owners, os.Stdin, os.Stdout)
	if closed {
		_, _ = io.WriteString(os.Stderr, "IN_CALLBACK_CLOSED\n")
	}
	return err
}

// watchRelay cancels a stream when its transport relay stops, independent of inherited SSH pipes.
func watchRelay(ctx context.Context, cancel context.CancelFunc, session string) (net.Conn, error) {
	root, err := stateDir()
	if err != nil {
		return nil, err
	}
	tuple := os.Getenv("SSH_CONNECTION")
	if session != "" {
		tuple = "session:" + session
	}
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
