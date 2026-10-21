package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// sessionTestRoot gives each test a private application directory.
func sessionTestRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestSessionResume verifies old and new authenticated tuples reach one stable identity.
func TestSessionResume(t *testing.T) {
	root := sessionTestRoot(t)
	p := peer{Control: "/tmp/master-a", Remote: ".local/share/in/bin/in"}
	id, err := localSession(root, p)
	if err != nil || !sessionID(id) {
		t.Fatalf("create local session: %v", err)
	}
	again, err := localSession(root, p)
	if err != nil || again != id {
		t.Fatalf("session ID changed: %v", err)
	}
	other, err := localSession(root, peer{Control: p.Control, Remote: "other/path"})
	if err != nil || other != id {
		t.Fatalf("changing a helper path lost the authenticated session: %v", err)
	}
	old := "192.0.2.10 40001 198.51.100.20 22"
	newer := "192.0.2.10 40002 198.51.100.20 22"
	for _, tuple := range []string{old, newer} {
		if err := bindSession(root, id, tuple); err != nil {
			t.Fatalf("bind tuple: %v", err)
		}
	}
	for _, tuple := range []string{old, newer} {
		got, err := sessionIdentity(root, tuple)
		if err != nil || got != "session:"+id {
			t.Fatalf("resolve tuple: got %q, %v", got, err)
		}
	}
	unbound := "192.0.2.11 40001 198.51.100.20 22"
	got, err := sessionIdentity(root, unbound)
	if err != nil || got != unbound {
		t.Fatalf("unbound tuple changed: got %q, %v", got, err)
	}
	if _, err = sessionIdentity(root, "fixture"); err == nil {
		t.Fatal("invalid transport tuple was accepted")
	}
}

// TestSessionAliasOwnership ensures another logical session cannot capture a live tuple.
func TestSessionAliasOwnership(t *testing.T) {
	root := sessionTestRoot(t)
	a, _ := localSession(root, peer{Control: "a", Remote: "in"})
	b, _ := localSession(root, peer{Control: "b", Remote: "in"})
	tuple := "192.0.2.1 42000 192.0.2.2 22"
	if err := bindSession(root, a, tuple); err != nil {
		t.Fatal(err)
	}
	if err := bindSession(root, b, tuple); err == nil {
		t.Fatal("second session stole a live alias")
	}
	got, err := sessionIdentity(root, tuple)
	if err != nil || got != "session:"+a {
		t.Fatalf("alias owner changed: %q, %v", got, err)
	}
}

// TestSessionRefusesDamagedState pins refusal of corrupt records and symlinked indexes.
func TestSessionRefusesDamagedState(t *testing.T) {
	t.Run("corrupt binding", func(t *testing.T) {
		root := sessionTestRoot(t)
		id, _ := localSession(root, peer{Control: "a", Remote: "in"})
		tuple := "192.0.2.1 42000 192.0.2.2 22"
		if err := bindSession(root, id, tuple); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "session", "binding-"+id+".json")
		if err := os.WriteFile(path, []byte(`{"id":"wrong","tuples":[]}`), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := sessionIdentity(root, tuple); err == nil {
			t.Fatal("corrupt binding was accepted")
		}
		if err := bindSession(root, id, tuple); err == nil {
			t.Fatal("corrupt binding was overwritten")
		}
	})
	t.Run("symlink alias", func(t *testing.T) {
		root := sessionTestRoot(t)
		id, _ := localSession(root, peer{Control: "a", Remote: "in"})
		tuple := "192.0.2.1 42000 192.0.2.2 22"
		path := filepath.Join(root, "session", "alias-"+digest(tuple)+".json")
		if err := os.Symlink(filepath.Join(root, "target"), path); err != nil {
			t.Fatal(err)
		}
		if err := bindSession(root, id, tuple); err == nil {
			t.Fatal("symlinked alias was replaced")
		}
		if _, err := sessionIdentity(root, tuple); err == nil {
			t.Fatal("symlinked alias was accepted")
		}
	})
}

// TestSessionAliasCap verifies the oldest alias expires while recent reconnects remain bound.
func TestSessionAliasCap(t *testing.T) {
	root := sessionTestRoot(t)
	id, err := localSession(root, peer{Control: "a", Remote: "in"})
	if err != nil {
		t.Fatal(err)
	}
	var first, last string
	for i := 0; i <= sessionAliasLimit; i++ {
		tuple := fmt.Sprintf("192.0.2.1 %d 192.0.2.2 22", 40000+i)
		if i == 0 {
			first = tuple
		}
		last = tuple
		if err := bindSession(root, id, tuple); err != nil {
			t.Fatalf("bind alias %d: %v", i, err)
		}
	}
	got, err := sessionIdentity(root, first)
	if err != nil || got != first {
		t.Fatalf("oldest alias did not expire: %q, %v", got, err)
	}
	got, err = sessionIdentity(root, last)
	if err != nil || got != "session:"+id {
		t.Fatalf("newest alias disappeared: %q, %v", got, err)
	}
}
