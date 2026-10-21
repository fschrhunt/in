package main

import (
	"context"
	"net"
	"testing"
	"time"
)

// TestCallbackConflictKeepsExistingListener protects listeners owned by other applications.
func TestCallbackConflictKeepsExistingListener(t *testing.T) {
	existing, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer existing.Close()
	_, port, _ := net.SplitHostPort(existing.Addr().String())
	if listeners, err := bindCallback(endpoint{"localhost", port}); err == nil {
		for _, l := range listeners {
			l.Close()
		}
		t.Fatal("occupied callback port was accepted")
	}
	conn, err := net.DialTimeout("tcp", existing.Addr().String(), time.Second)
	if err != nil {
		t.Fatal("existing listener was disturbed")
	}
	conn.Close()
}

// TestBridgeCancellationReleasesReservedPort protects cleanup before any callback arrives.
func TestBridgeCancellationReleasesReservedPort(t *testing.T) {
	candidate, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := candidate.Addr().String()
	candidate.Close()
	ctx, cancel := context.WithCancel(context.Background())
	b := newBridge(ctx, peer{})
	if _, err = b.prepare("http://"+addr+"/callback", "2:1"); err != nil {
		t.Fatal(err)
	}
	cancel()
	b.close()
	again, err := net.Listen("tcp4", addr)
	if err != nil {
		t.Fatal("callback port leaked")
	}
	again.Close()
}
