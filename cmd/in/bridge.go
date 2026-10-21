package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"syscall"
	"time"
)

const callbackLease = 3 * time.Minute

// bridge owns laptop callback listeners; OpenSSH owns authentication and data channels.
type bridge struct {
	ctx    context.Context
	cancel context.CancelFunc
	peer   peer
	mu     sync.Mutex
	routes map[*route]bool
	wg     sync.WaitGroup
}

// route owns a finite lease and every connection accepted under it.
type route struct {
	ctx       context.Context
	cancel    context.CancelFunc
	listeners []net.Listener
	endpoint  endpoint
	owners    string
	slots     chan struct{}
}

// newBridge creates request-scoped state without starting a background service.
func newBridge(ctx context.Context, p peer) *bridge {
	ctx, cancel := context.WithCancel(ctx)
	return &bridge{ctx: ctx, cancel: cancel, peer: p, routes: map[*route]bool{}}
}

// prepare reserves all required addresses before acknowledging a browser request.
func (b *bridge) prepare(raw, owners string) (func(), error) {
	ep, err := callback(raw)
	if err != nil {
		return nil, err
	}
	var r *route
	if ep != nil {
		if _, err := parseOwners(owners); err != nil {
			return nil, err
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		if len(b.routes) >= requestLimit {
			return nil, fmt.Errorf("too many active callback routes")
		}
		ctx, cancel := context.WithTimeout(b.ctx, callbackLease)
		r = &route{ctx: ctx, cancel: cancel, endpoint: *ep, owners: owners, slots: make(chan struct{}, 8)}
		listeners, err := bindCallback(*ep)
		if err != nil {
			cancel()
			return nil, err
		}
		r.listeners = listeners
		b.routes[r] = true
		b.wg.Add(1)
		go func() {
			defer b.wg.Done()
			<-ctx.Done()
			for _, l := range r.listeners {
				l.Close()
			}
			b.mu.Lock()
			delete(b.routes, r)
			b.mu.Unlock()
		}()
		for _, l := range listeners {
			b.wg.Add(1)
			go func(l net.Listener) { defer b.wg.Done(); b.accept(r, l) }(l)
		}
	}
	return func() {
		b.wg.Add(1)
		go func() {
			defer b.wg.Done()
			if err := openBrowser(b.ctx, raw); err != nil {
				if r != nil {
					r.cancel()
				}
				notify(err.Error())
			}
		}()
	}, nil
}

// bindCallback binds localhost on both available families and rolls back partial conflicts.
func bindCallback(ep endpoint) ([]net.Listener, error) {
	hosts := []string{ep.Host}
	if ep.Host == "localhost" {
		hosts = []string{"127.0.0.1", "::1"}
	}
	var listeners []net.Listener
	for _, host := range hosts {
		network := "tcp4"
		if net.ParseIP(host).To4() == nil {
			network = "tcp6"
		}
		l, err := net.Listen(network, net.JoinHostPort(host, ep.Port))
		if err != nil {
			if ep.Host == "localhost" && (errors.Is(err, syscall.EAFNOSUPPORT) || errors.Is(err, syscall.EADDRNOTAVAIL)) {
				continue
			}
			for _, old := range listeners {
				old.Close()
			}
			return nil, fmt.Errorf("cannot reserve laptop callback port %s; another listener may be using it", ep.Port)
		}
		listeners = append(listeners, l)
	}
	if len(listeners) == 0 {
		return nil, fmt.Errorf("no usable laptop loopback address")
	}
	return listeners, nil
}

// accept bounds callback concurrency and gives each accepted stream the route's deadline.
func (b *bridge) accept(r *route, l net.Listener) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		select {
		case r.slots <- struct{}{}:
		default:
			c.Close()
			continue
		}
		b.wg.Add(1)
		go func() {
			defer b.wg.Done()
			defer func() { <-r.slots }()
			defer c.Close()
			if deadline, ok := r.ctx.Deadline(); ok {
				c.SetDeadline(deadline)
			}
			stop := context.AfterFunc(r.ctx, func() { c.Close() })
			defer stop()
			if err := b.forward(r, c); err != nil && r.ctx.Err() == nil {
				notify("callback transport failed; check the remote CLI and retry login")
			}
		}()
	}
}

// forward establishes a remote listener connection before sending bytes and never replays them.
func (b *bridge) forward(r *route, c net.Conn) error {
	cmd := b.peer.sshCommand(r.ctx, "pipe", r.endpoint.Host, r.endpoint.Port, r.owners)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stopIO := context.AfterFunc(r.ctx, func() { stdin.Close(); stdout.Close() })
	defer stopIO()
	var diagnostic boundedBuffer
	cmd.Stderr = &diagnostic
	if err = cmd.Start(); err != nil {
		return err
	}
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	reader := bufio.NewReaderSize(stdout, 4096)
	// The remote helper retries only before connecting; no callback data has left c yet.
	header := make([]byte, 6)
	_, err = io.ReadFull(reader, header)
	if err != nil || string(header) != "READY\n" {
		return fmt.Errorf("remote listener unavailable")
	}
	done := make(chan struct{}, 1)
	go func() { _, _ = io.Copy(stdin, c); stdin.Close(); done <- struct{}{} }()
	_, err = io.Copy(c, reader)
	if tcp, ok := c.(*net.TCPConn); ok {
		tcp.CloseWrite()
	}
	// The server's response EOF completes this SSH channel; unblock any browser keepalive.
	c.Close()
	<-done
	waitErr := cmd.Wait()
	waited = true
	if waitErr == nil && bytes.Contains(diagnostic.Bytes(), []byte("IN_CALLBACK_CLOSED\n")) && len(r.slots) == 1 {
		r.cancel()
	}
	return err
}

// close cancels owned routes and waits for stream goroutines to release their resources.
func (b *bridge) close() {
	b.cancel()
	b.mu.Lock()
	for r := range b.routes {
		r.cancel()
	}
	b.mu.Unlock()
	b.wg.Wait()
}

// openBrowser invokes a fixed executable without interpreting the URL as shell syntax.
func openBrowser(parent context.Context, raw string) error {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	command := os.Getenv("IN_BROWSER_EXEC")
	if command == "" {
		if runtime.GOOS == "darwin" {
			command = "/usr/bin/open"
		} else {
			command = "xdg-open"
		}
	}
	cmd := exec.CommandContext(ctx, command, raw)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if cmd.Run() != nil {
		return fmt.Errorf("could not open the laptop browser")
	}
	return nil
}

// notify reports a fixed diagnostic locally even when the remote launcher discards stderr.
func notify(message string) {
	saveDiagnostic(message)
	fmt.Fprintln(os.Stderr, "in:", message)
	if runtime.GOOS == "darwin" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		// All diagnostics passed here are controlled text, never request data.
		cmd := exec.CommandContext(ctx, "/usr/bin/osascript", "-e", `display notification "A remote login needs attention. Run in status using its installed path." with title "in"`)
		_ = cmd.Run()
	}
}
