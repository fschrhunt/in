package main

import (
	"path/filepath"
	"strconv"
	"strings"
)

// terminalDestination extracts ET's host without interpreting remote command or tunnel values.
// Unknown options preserve ordinary ET behavior by declining automatic integration.
func terminalDestination(args []string) (string, bool) {
	values := map[string]bool{"username": true, "port": true, "command": true, "terminal-path": true, "tunnel": true, "reversetunnel": true, "jumphost": true, "jport": true, "jserverfifo": true, "verbose": true, "keepalive": true, "logdir": true, "ssh-socket": true, "serverfifo": true, "ssh-option": true}
	flags := map[string]bool{"noexit": true, "kill-other-sessions": true, "macserver": true, "logtostdout": true, "silent": true, "no-terminal": true, "forward-ssh-agent": true, "telemetry": true}
	host := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			if host != "" || i+2 != len(args) {
				return "", false
			}
			host = args[i+1]
			break
		}
		if strings.HasPrefix(arg, "--") {
			name, _, inline := strings.Cut(arg[2:], "=")
			if flags[name] {
				continue
			}
			if !values[name] {
				return "", false
			}
			if !inline {
				i++
				if i >= len(args) {
					return "", false
				}
			}
			continue
		}
		if strings.HasPrefix(arg, "-") {
			if len(arg) == 1 {
				return "", false
			}
			for j := 1; j < len(arg); j++ {
				c := arg[j]
				if strings.ContainsRune("exNf", rune(c)) {
					continue
				}
				if !strings.ContainsRune("upctrvkl", rune(c)) {
					return "", false
				}
				if j+1 == len(arg) {
					i++
					if i >= len(args) {
						return "", false
					}
				}
				break
			}
			continue
		}
		if host != "" {
			return "", false
		}
		host = arg
	}
	if at := strings.LastIndexByte(host, '@'); at >= 0 {
		host = host[at+1:]
	}
	if count := strings.Count(host, ":"); count == 1 {
		var port string
		host, port, _ = strings.Cut(host, ":")
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", false
		}
	} else if count > 1 {
		return "", false
	}
	if host == "" {
		return "", false
	}
	for i, c := range host {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			continue
		}
		if i > 0 && strings.ContainsRune("._-", c) {
			continue
		}
		return "", false
	}
	return host, true
}

// terminalOptedIn accepts only host stanzas generated and still owned by this installation.
// An ET connection to any other host must not gain access to the laptop's browser bridge.
func terminalOptedIn(root string, args []string) bool {
	host, ok := terminalDestination(args)
	if !ok {
		return false
	}
	installed, err := readInstallation(root)
	if err != nil {
		return false
	}
	for _, file := range installed.Files {
		name := filepath.Base(file.Path)
		if filepath.Dir(file.Path) != root || !strings.HasPrefix(name, "ssh-") || !strings.HasSuffix(name, ".conf") || file.Block != "" {
			continue
		}
		data, exists, err := readOwned(file.Path)
		if err != nil || !exists || hashBytes(data) != file.Hash {
			continue
		}
		first, _, _ := strings.Cut(string(data), "\n")
		fields := strings.Fields(first)
		if len(fields) == 2 && fields[0] == "Host" && strings.EqualFold(fields[1], host) {
			return true
		}
	}
	return false
}
