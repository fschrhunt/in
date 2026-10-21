package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// callbackStream verifies one remote listener, then relays bytes without replaying them.
// Its result reports observed listener closure, never OAuth authentication success.
func callbackStream(ctx context.Context, host, port, owners string, input io.Reader, output io.Writer) (bool, error) {
	if err := validOwnership(owners); err != nil {
		return false, err
	}
	if err := validEndpoint(host, port); err != nil {
		return false, err
	}
	deadline := time.Now().Add(10 * time.Second)
	var conn net.Conn
	var selected *listenerInfo
	for conn == nil {
		listener, err := callbackListener(host, port)
		if err != nil {
			return false, err
		}
		if listener != nil {
			if !listenerOwned(listener.inode, owners) {
				return false, fmt.Errorf("callback listener no longer matches the browser request")
			}
			c, err := (&net.Dialer{Timeout: 200 * time.Millisecond}).DialContext(ctx, "tcp", net.JoinHostPort(listener.host, port))
			if err == nil {
				current, checkErr := callbackListener(host, port)
				if checkErr != nil || current == nil || current.inode != listener.inode || !listenerOwned(current.inode, owners) {
					c.Close()
					return false, fmt.Errorf("callback listener changed during connection")
				}
				conn = c
				selected = listener
				break
			}
		}
		if listener == nil && strings.HasPrefix(owners, "socket:") {
			return false, fmt.Errorf("requested callback listener has closed")
		}
		if time.Now().After(deadline) {
			return false, fmt.Errorf("remote callback listener did not become ready")
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Minute))
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if _, err := io.WriteString(output, "READY\n"); err != nil {
		return false, err
	}

	go func() {
		_, _ = io.Copy(conn, input)
		if tcp, ok := conn.(*net.TCPConn); ok {
			tcp.CloseWrite()
		}
	}()
	if _, err := io.Copy(output, conn); err != nil {
		return false, err
	}
	// Observe listener shutdown without consuming another connection or reading OAuth.
	until := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(until) {
		current, err := callbackListener(host, port)
		if err == nil && (current == nil || current.inode != selected.inode) {
			return true, nil
		}
		select {
		case <-ctx.Done():
			return false, nil
		case <-time.After(25 * time.Millisecond):
		}
	}
	return false, nil
}
