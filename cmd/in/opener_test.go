package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestCapturedOpenURL keeps direct development pages out of callback forwarding.
func TestCapturedOpenURL(t *testing.T) {
	for _, tt := range []struct {
		raw  string
		want bool
	}{
		{"https://accounts.example.test/authorize?state=fixture", true},
		{"https://example.test/article", true},
		{"http://127.0.0.1:53682/auth?state=fixture", true},
		{"http://127.0.0.1:3000/authorize", false},
		{"http://localhost:8080/dev?redirect_uri=http://localhost:9090/callback", false},
		{"http://[::1]:3000/", false},
		{"file:///tmp/index.html", false},
		{"--help", false},
		{"https://example.test/\nsecret", false},
	} {
		if got := capturedOpenURL(tt.raw); got != tt.want {
			t.Errorf("capture classification for %q: got %v, want %v", tt.raw, got, tt.want)
		}
	}
}

// TestNativeOpenPreservesArgumentsAndUserBrowser proves delegation removes owned hooks without altering arguments.
func TestNativeOpenPreservesArgumentsAndUserBrowser(t *testing.T) {
	root := filepath.Join(t.TempDir(), "in")
	t.Setenv("IN_HOME", root)
	native := t.TempDir()
	capture := filepath.Join(t.TempDir(), "capture")
	script := "#!/bin/sh\nprintf '%s\\n' \"${BROWSER-unset}\" > \"$IN_TEST_CAPTURE\"\nprintf '%s\\n' \"$PATH\" >> \"$IN_TEST_CAPTURE\"\nprintf '%s\\0' \"$@\" >> \"$IN_TEST_CAPTURE\"\n"
	if err := os.WriteFile(filepath.Join(native, "xdg-open"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IN_TEST_CAPTURE", capture)
	t.Setenv("PATH", filepath.Join(root, "openers")+":"+native)
	t.Setenv("BROWSER", filepath.Join(root, "bin", "browser"))
	args := []string{"--help", "a b", "$(touch /tmp/never-execute)", "line\nbreak", ""}
	if err := nativeOpen(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitN(string(data), "\n", 3)
	if len(lines) != 3 || lines[0] != "unset" || lines[1] != native || !reflect.DeepEqual(strings.Split(strings.TrimSuffix(lines[2], "\x00"), "\x00"), args) {
		t.Fatalf("delegation changed owned environment or argument bytes: %q", data)
	}
	t.Setenv("BROWSER", "user-browser")
	if err := nativeOpen(context.Background(), []string{"http://localhost:3000/"}); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(capture)
	if !strings.HasPrefix(string(data), "user-browser\n"+native+"\n") {
		t.Fatal("delegation lost the user's BROWSER override")
	}
}

// TestOpenDelegatesDirectDevelopmentURL checks the generic entry point never forwards an unknown local page.
func TestOpenDelegatesDirectDevelopmentURL(t *testing.T) {
	root := filepath.Join(t.TempDir(), "in")
	t.Setenv("IN_HOME", root)
	native := t.TempDir()
	capture := filepath.Join(t.TempDir(), "url")
	if err := os.WriteFile(filepath.Join(native, "xdg-open"), []byte("#!/bin/sh\nprintf '%s' \"$1\" > \"$IN_TEST_CAPTURE\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Join(root, "openers")+":"+native)
	t.Setenv("IN_TEST_CAPTURE", capture)
	t.Setenv("SSH_CONNECTION", "fixture")
	t.Setenv("BROWSER", filepath.Join(root, "bin", "browser"))
	raw := "http://localhost:3000/?token=fixture"
	if err := open(context.Background(), []string{raw}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(capture)
	if string(got) != raw {
		t.Fatal("direct development URL was not delegated")
	}
}

// TestOpenRespectsInheritedHooksAndFailedBrowse keeps local or overridden sessions native and never retries failed capture.
func TestOpenRespectsInheritedHooksAndFailedBrowse(t *testing.T) {
	root := filepath.Join(t.TempDir(), "in")
	t.Setenv("IN_HOME", root)
	native := t.TempDir()
	capture := filepath.Join(t.TempDir(), "url")
	if err := os.WriteFile(filepath.Join(native, "xdg-open"), []byte("#!/bin/sh\nprintf '%s' \"$1\" > \"$IN_TEST_CAPTURE\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Join(root, "openers")+":"+native)
	t.Setenv("IN_TEST_CAPTURE", capture)
	raw := "https://example.test/login"
	for _, env := range []struct{ ssh, browser string }{
		{"", filepath.Join(root, "bin", "browser")},
		{"fixture", "user-browser"},
	} {
		t.Setenv("SSH_CONNECTION", env.ssh)
		t.Setenv("BROWSER", env.browser)
		if err := open(context.Background(), []string{raw}); err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(capture)
		if string(got) != raw {
			t.Fatal("inherited shim ignored the session guard")
		}
	}
	if err := os.Remove(capture); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BROWSER", filepath.Join(root, "bin", "browser"))
	if err := open(context.Background(), []string{raw}); err == nil {
		t.Fatal("unavailable login bridge was reported as successful")
	}
	if _, err := os.Stat(capture); !os.IsNotExist(err) {
		t.Fatal("failed browser capture was retried through native xdg-open")
	}
}
