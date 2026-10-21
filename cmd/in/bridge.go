package main

import (
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

// bridge owns laptop callback listeners and delegates remote streams to its session transport.
type bridge struct {
	ctx       context.Context
	cancel    context.CancelFunc
	transport callbackTransport
	mu        sync.Mutex
	routes    map[*route]bool
	wg        sync.WaitGroup
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
func newBridge(ctx context.Context, p callbackTransport) *bridge {
	ctx, cancel := context.WithCancel(ctx)
	return &bridge{ctx: ctx, cancel: cancel, transport: p, routes: map[*route]bool{}}
}

// prepare admits recognized login routes and reserves their addresses before acknowledgement.
// rclone means the remote hook verified its direct entry point against the owning executable.
func (b *bridge) prepare(raw, owners string, rclone bool) (func(), error) {
	ep, err := callback(raw)
	if err != nil {
		return nil, err
	}
	var r *route
	if ep != nil {
		if ep.Direct && (!rclone || !rcloneAuthURL(raw)) {
			return nil, unrecognizedLogin()
		}
		if err := validOwnership(owners); err != nil {
			return nil, err
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		// A different pinned socket at the same unambiguous endpoint proves the old listener closed.
		// Reclaim its idle lease immediately so cancelling login does not block the next attempt.
		pinned, _ := socketIdentity(owners)
		for old := range b.routes {
			prior, _ := socketIdentity(old.owners)
			if pinned != nil && prior != nil && old.endpoint.Port == ep.Port && pinned.host == prior.host && pinned.inode != prior.inode && len(old.slots) == 0 {
				old.cancel()
				for _, listener := range old.listeners {
					listener.Close()
				}
				delete(b.routes, old)
			}
		}
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
			if err := b.openBrowser(r, raw); err != nil && b.ctx.Err() == nil {
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
		// Serialize admission with stale-lease replacement; active responses must not be displaced.
		b.mu.Lock()
		if r.ctx.Err() != nil {
			b.mu.Unlock()
			c.Close()
			return
		}
		select {
		case r.slots <- struct{}{}:
		default:
			b.mu.Unlock()
			c.Close()
			continue
		}
		b.mu.Unlock()
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
	ch, err := b.transport.Open(r.ctx, r.endpoint, r.owners)
	if err != nil {
		return err
	}
	defer ch.Close()
	stopIO := context.AfterFunc(r.ctx, func() { _ = ch.Close(); _ = c.Close() })
	defer stopIO()
	// The remote helper retries only before connecting; no callback data has left c yet.
	if err := readyChannel(ch); err != nil {
		return err
	}
	done := make(chan struct{}, 1)
	go func() { _, _ = io.Copy(ch, c); _ = ch.CloseWrite(); done <- struct{}{} }()
	_, err = io.Copy(c, ch)
	if tcp, ok := c.(*net.TCPConn); ok {
		tcp.CloseWrite()
	}
	// Response EOF completes this callback channel; unblock any browser keepalive.
	c.Close()
	<-done
	closed, waitErr := ch.Wait()
	if waitErr == nil && closed && len(r.slots) == 1 {
		r.cancel()
	}
	if err != nil {
		return err
	}
	return waitErr
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

// openBrowser serializes opener startup with route replacement, without cancelling an already launched opener on callback completion.
func (b *bridge) openBrowser(r *route, raw string) error {
	ctx, cancel := context.WithTimeout(b.ctx, 5*time.Second)
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
	b.mu.Lock()
	if r != nil && r.ctx.Err() != nil {
		b.mu.Unlock()
		return nil
	}
	err := cmd.Start()
	b.mu.Unlock()
	if err != nil || cmd.Wait() != nil {
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
