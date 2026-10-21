package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// parentControl finds an explicit socket override on the owning SSH command.
// The key is the hook's expanded %C; absence of an SSH ancestor is not an error.
func parentControl(key string) (control, config string, found bool, err error) {
	pid := os.Getppid()
	for depth := 0; depth < 6 && pid > 1; depth++ {
		parent, name, argv, readErr := parentSSHProcess(pid)
		if readErr != nil {
			return "", "", false, fmt.Errorf("cannot inspect SSH parent")
		}
		switch filepath.Base(name) {
		case "ssh":
			return scanSSHArgs(argv, key)
		case "sh", "bash", "zsh", "in":
			if parent == pid {
				return "", "", false, nil
			}
			pid = parent
		default:
			return "", "", false, nil
		}
	}
	return "", "", false, nil
}

// scanSSHArgs reads only options before the destination from a literal SSH argv.
// Unknown options fail closed because their argument could otherwise look like a host.
func scanSSHArgs(argv []string, key string) (control, config string, found bool, err error) {
	if len(argv) == 0 {
		return "", "", false, fmt.Errorf("invalid SSH arguments")
	}
	for i := 1; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--" {
			break
		}
		if len(arg) == 0 || arg[0] != '-' || arg == "-" {
			break
		}
		for j := 1; j < len(arg); j++ {
			option := arg[j]
			if strings.ContainsRune("46AaCfGgKkMNnqsTtVvXxYy", rune(option)) {
				continue
			}
			if !strings.ContainsRune("BbcDEeFIiJLlmOoPpQRSWw", rune(option)) {
				return "", "", false, fmt.Errorf("unsupported SSH option")
			}
			value := arg[j+1:]
			if value == "" {
				i++
				if i >= len(argv) {
					return "", "", false, fmt.Errorf("missing SSH option argument")
				}
				value = argv[i]
			}
			if value == "" {
				return "", "", false, fmt.Errorf("empty SSH option argument")
			}
			switch option {
			case 'S':
				control, found = value, true
			case 'F':
				config = value
			case 'o':
				name, val, ok := strings.Cut(value, "=")
				if !ok {
					parts := strings.Fields(value)
					if len(parts) == 2 {
						name, val = parts[0], parts[1]
					} else if strings.EqualFold(strings.TrimSpace(value), "ControlPath") {
						return "", "", false, fmt.Errorf("invalid ControlPath option")
					}
				}
				if strings.EqualFold(strings.TrimSpace(name), "ControlPath") && !found {
					control, found = strings.TrimSpace(val), true
				}
			}
			break
		}
	}
	if !found {
		return "", config, false, nil
	}
	control, err = expandParentControl(control, key)
	if err != nil {
		return "", "", false, err
	}
	return control, config, true, nil
}

// expandParentControl resolves only the tokens supplied by Kitty's socket template.
func expandParentControl(value, key string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] != '%' {
			out.WriteByte(value[i])
			continue
		}
		i++
		if i >= len(value) {
			return "", fmt.Errorf("unsupported ControlPath token")
		}
		switch value[i] {
		case '%':
			out.WriteByte('%')
		case 'C':
			out.WriteString(key)
		default:
			return "", fmt.Errorf("unsupported ControlPath token")
		}
	}
	path := out.String()
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot expand ControlPath home")
		}
		path = filepath.Join(home, path[2:])
	}
	if !filepath.IsAbs(path) || strings.ContainsAny(path, " \t\r\n\x00") || path == "none" {
		return "", fmt.Errorf("unsafe ControlPath")
	}
	return path, nil
}
