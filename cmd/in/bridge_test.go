package main

import (
	"context"
	"net"
	"net/url"
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
	if listeners, err := bindCallback(endpoint{Host: "localhost", Port: port}); err == nil {
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
	if _, err = b.prepare("https://provider.invalid/authorize?response_type=code&client_id=fixture&redirect_uri="+url.QueryEscape("http://"+addr+"/callback"), "2:1", false); err != nil {
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

// TestUnverifiedDirectRequestsNeverReservePorts rejects dev opens, including rclone-shaped URLs without evidence.
func TestUnverifiedDirectRequestsNeverReservePorts(t *testing.T) {
	b := newBridge(context.Background(), peer{})
	defer b.close()
	for _, tt := range []struct {
		url    string
		rclone bool
	}{
		{"http://localhost:3000/", false},
		{"http://localhost:3000/?redirect_uri=https://example.test/", false},
		{"http://localhost:3000/?response_type=code&client_id=fixture&redirect_uri=http://localhost:4000/callback", false},
		{"http://localhost:3000/?state=fixture", true},
		{"http://127.0.0.1:53682/auth?state=fixture", false},
	} {
		if _, err := b.prepare(tt.url, "2:1", tt.rclone); err == nil {
			t.Fatal("unverified direct request accepted")
		}
	}
	if len(b.routes) != 0 {
		t.Fatal("refused browser request reserved a callback route")
	}
}
