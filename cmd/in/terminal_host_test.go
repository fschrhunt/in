package main

import (
	"context"
	"path/filepath"
	"testing"
)

// TestETOptInUsesDestinationOnly prevents command/tunnel arguments from granting browser access.
func TestETOptInUsesDestinationOnly(t *testing.T) {
	root := filepath.Join(t.TempDir(), "in")
	t.Setenv("IN_HOME", root)
	config := filepath.Join(t.TempDir(), "ssh.conf")
	rc := filepath.Join(t.TempDir(), "rc")
	if err := setup(context.Background(), []string{"laptop", "--shell-file", rc, "--ssh-config", config, "trusted"}); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		args    []string
		allowed bool
	}{
		{[]string{"trusted"}, true},
		{[]string{"user@TRUSTED:2023", "-c", "untrusted"}, true},
		{[]string{"--command", "trusted", "untrusted"}, false},
		{[]string{"--reversetunnel", "trusted", "untrusted"}, false},
		{[]string{"--new-option", "trusted"}, false},
		{[]string{"-p2022", "-eccommand", "trusted"}, true},
		{[]string{"--", "trusted"}, true},
		{[]string{"--command"}, false},
	}
	for _, test := range tests {
		if got := terminalOptedIn(root, test.args); got != test.allowed {
			t.Fatalf("ET opt-in = %v, want %v for %v", got, test.allowed, test.args)
		}
	}
}
