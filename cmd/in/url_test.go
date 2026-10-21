package main

import (
	"net/url"
	"testing"
)

// TestCallbackRouting extracts explicit endpoints without modifying the supplied URL.
func TestCallbackRouting(t *testing.T) {
	tests := []struct{ redirect, host, port string }{
		{"http://localhost:8976/oauth/callback", "localhost", "8976"},
		{"http://127.0.0.1:43127/cb", "127.0.0.1", "43127"},
		{"http://[::1]:43127/cb", "::1", "43127"},
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			raw := "https://provider.example/auth?response_type=code&client_id=fixture&state=a%2Bb&redirect_uri=" + url.QueryEscape(tt.redirect)
			ep, err := callback(raw)
			if err != nil || ep == nil || ep.Host != tt.host || ep.Port != tt.port {
				t.Fatalf("wrong endpoint: %v %v", ep, err)
			}
		})
	}
}

// TestCallbackRefusesUnsafeRouting pins the supported URL boundary without invoking a browser.
func TestCallbackRefusesUnsafeRouting(t *testing.T) {
	for _, raw := range []string{"file:///tmp/x", "https://user:secret@example.com/", "https://example.com/\ncommand", "https://example.com/?redirect_uri=http%3A%2F%2Flocalhost%3A80%2Fcb", "https://example.com/?redirect_uri=https%3A%2F%2Flocalhost%3A8765%2Fcb", "https://example.com/?redirect_uri=a&redirect_uri=b"} {
		if _, err := callback(raw); err == nil {
			t.Error("accepted unsupported URL form")
		}
	}
	for _, raw := range []string{"https://example.com/device", "https://example.com/?redirect_uri=https%3A%2F%2Fother.example%2Fcb"} {
		if ep, err := callback(raw); ep != nil || err != nil {
			t.Fatal("ordinary URL should not create a callback route")
		}
	}
}

// TestRedirectAloneDoesNotAuthorizeForwarding avoids treating arbitrary redirect parameters as OAuth.
func TestRedirectAloneDoesNotAuthorizeForwarding(t *testing.T) {
	for _, query := range []string{"", "client_id=fixture&", "response_type=code&", "response_type=code&response_type=token&client_id=fixture&"} {
		if _, err := callback("https://example.test/?" + query + "redirect_uri=http://localhost:3000/"); err == nil {
			t.Fatal("non-OAuth redirect accepted")
		}
	}
}

// TestRcloneDirectURLIsNarrow keeps a state-shaped query from broadly opting dev URLs in.
func TestRcloneDirectURLIsNarrow(t *testing.T) {
	if !rcloneAuthURL("http://127.0.0.1:53682/auth?state=fixture") {
		t.Fatal("verified entry point rejected")
	}
	for _, raw := range []string{"http://localhost:53682/auth?state=x", "http://127.0.0.1:3000/auth?state=x", "http://127.0.0.1:53682/?state=x", "http://127.0.0.1:53682/auth?state=", "http://127.0.0.1:53682/auth?state=x&state=y"} {
		if rcloneAuthURL(raw) {
			t.Fatal("unverified direct URL accepted")
		}
	}
}
