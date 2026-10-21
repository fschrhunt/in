package main

import (
	"net"
	"os"
	"runtime"
	"strconv"
	"testing"
)

// TestListenerDiscoverySelectsOnlyPrivateOwnedSockets keeps codes away from unrelated accounts and wildcard listeners.
func TestListenerDiscoverySelectsOnlyPrivateOwnedSockets(t *testing.T) {
	uid := strconv.Itoa(os.Getuid())
	table := "  0: 0100007F:230F 00000000:0000 0A 00000000:00000000 00:00000000 00000000 " + uid + " 0 12345\n" +
		"  1: 00000000:230F 00000000:0000 0A 00000000:00000000 00:00000000 00000000 " + uid + " 0 54321\n" +
		"  2: 0100007F:230F 00000000:0000 0A 00000000:00000000 00:00000000 00000000 999999 0 98765\n"
	matches := listenersIn(table, "localhost", "8975", os.Getuid())
	if len(matches) != 1 || matches[0].host != "127.0.0.1" || matches[0].inode != "12345" {
		t.Fatalf("wrong listener selection: %v", matches)
	}
}

// TestListenerDiscoveryDecodesIPv6 ensures an IPv6-only callback does not fall back to another IPv4 service.
func TestListenerDiscoveryDecodesIPv6(t *testing.T) {
	table := "0: 00000000000000000000000001000000:230F 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000 " + strconv.Itoa(os.Getuid()) + " 0 45678\n"
	matches := listenersIn(table, "localhost", "8975", os.Getuid())
	if len(matches) != 1 || matches[0].host != "::1" {
		t.Fatalf("wrong IPv6 address: %v", matches)
	}
}

// TestListenerOwnershipPinsTheRequestProcess rejects a reused PID or unrelated live process.
func TestListenerOwnershipPinsTheRequestProcess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux remote process evidence")
	}
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	listener, err := callbackListener("127.0.0.1", port)
	if err != nil || listener == nil {
		t.Fatal("listener not discovered", err)
	}
	ref, _, _, err := processIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	owner := strconv.Itoa(ref.pid) + ":" + ref.start
	if !listenerOwned(listener.inode, owner) {
		t.Fatal("request process does not own its socket")
	}
	if listenerOwned(listener.inode, strconv.Itoa(ref.pid)+":0") {
		t.Fatal("PID reuse accepted")
	}
	ancestors, err := browserOwners()
	if err == nil && listenerOwned(listener.inode, ancestors) {
		t.Fatal("unrelated ancestors accepted")
	}
}

// TestDeclaredCallbackPinsServiceSocket allows a non-ancestor service without accepting a replacement listener.
func TestDeclaredCallbackPinsServiceSocket(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux remote socket evidence")
	}
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	ep := endpoint{Host: "127.0.0.1", Port: port}
	owner, err := callbackOwner(ep)
	if err != nil || validOwnership(owner) != nil {
		t.Fatal("existing callback was not pinned", err)
	}
	listener, err := callbackListener(ep.Host, ep.Port)
	if err != nil || listener == nil || !listenerOwned(listener.inode, owner) {
		t.Fatal("service callback rejected", err)
	}
	// The test process owns the socket but is not an ancestor of browserOwners.
	ancestors, err := browserOwners()
	if err == nil && listenerOwned(listener.inode, ancestors) {
		t.Fatal("fixture unexpectedly satisfies ancestor-only policy")
	}
	l.Close()
	replacement, err := net.Listen("tcp4", net.JoinHostPort(ep.Host, port))
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	listener, err = callbackListener(ep.Host, ep.Port)
	if err != nil || listener == nil {
		t.Fatal("replacement not found", err)
	}
	if listenerOwned(listener.inode, owner) {
		t.Fatal("replacement received the original callback permission")
	}
}

// TestSocketIdentityRejectsMalformedProof prevents empty, zero and noncanonical inode references.
func TestSocketIdentityRejectsMalformedProof(t *testing.T) {
	for _, value := range []string{"socket:", "socket:0", "socket:01", "socket:-1", "socket:1,2", "socket:18446744073709551616@127.0.0.1", "socket:1@localhost", "socket:1@0.0.0.0", "socket:1@192.168.0.1", "socket:1@::01"} {
		if validOwnership(value) == nil {
			t.Fatal("malformed socket identity accepted")
		}
	}
}
