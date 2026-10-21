package main

import (
	"strings"
	"testing"
)

// TestScanSSHArgs pins literal argv boundaries and the explicit socket forms.
func TestScanSSHArgs(t *testing.T) {
	tests := []struct {
		name, key, want, config string
		argv                    []string
		found                   bool
		bad                     bool
	}{
		{"attached S", "actual", "/tmp/socket-actual", "custom config", []string{"ssh", "-vTS/tmp/socket-%C", "-Fcustom config", "host"}, true, false},
		{"separate S", "actual", "/tmp/socket-actual", "", []string{"ssh", "-S", "/tmp/socket-%C", "host"}, true, false},
		{"attached o", "actual", "/tmp/literal%-actual", "", []string{"ssh", "-oControlPath=/tmp/literal%%-%C", "host"}, true, false},
		{"separate o", "actual", "/tmp/socket-actual", "", []string{"ssh", "-o", "ControlPath=/tmp/socket-%C", "host"}, true, false},
		{"first o wins", "actual", "/tmp/first", "", []string{"ssh", "-oControlPath=/tmp/first", "-oControlPath=/tmp/second", "host"}, true, false},
		{"S replaces o", "actual", "/tmp/second", "", []string{"ssh", "-oControlPath=/tmp/first", "-S/tmp/second", "-oControlPath=/tmp/third", "host"}, true, false},
		{"unsafe space", "actual", "", "", []string{"ssh", "-S", "/tmp/a b", "host"}, false, true},
		{"remote options ignored", "actual", "", "", []string{"ssh", "host", "-S", "/tmp/fake", "-oControlPath=/tmp/fake"}, false, false},
		{"no argument override", "actual", "", "custom config", []string{"ssh", "-vT", "-F", "custom config", "host"}, false, false},
		{"unknown option", "actual", "", "", []string{"ssh", "-Z", "host", "-S/tmp/fake"}, false, true},
		{"missing argument", "actual", "", "", []string{"ssh", "-S"}, false, true},
		{"unsupported token", "actual", "", "", []string{"ssh", "-S/tmp/%h", "host"}, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			control, config, found, err := scanSSHArgs(tt.argv, tt.key)
			if (err != nil) != tt.bad || control != tt.want || found != tt.found || (!tt.bad && config != tt.config) {
				t.Fatalf("scanSSHArgs() = control %q, config %q, found %v, err %v", control, config, found, err)
			}
			if err != nil && (strings.Contains(err.Error(), tt.key) || strings.Contains(err.Error(), "/tmp/")) {
				t.Fatal("error exposed SSH arguments")
			}
		})
	}
}
