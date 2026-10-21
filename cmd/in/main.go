// Command in connects remote browser requests to the laptop over existing SSH.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

// version is supplied by release builds.
var version = "dev"

// main keeps diagnostics free of request data and cancels owned work on signals.
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	args := os.Args[1:]
	if filepath.Base(os.Args[0]) == "browser" {
		args = append([]string{"browse"}, args...)
	}
	if err := run(ctx, args); err != nil {
		if len(args) > 0 && args[0] == "attach" {
			saveDiagnostic(err.Error())
		}
		fmt.Fprintln(os.Stderr, "in:", err)
		os.Exit(1)
	}
}

// run dispatches public management commands and the private SSH/browser entry points.
func run(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		fmt.Print("in — remote login, in your browser.\n\nSetup once:\n  ./in setup remote\n  ./in setup laptop HOST\n\nManage:\n  ./in status\n  ./in stop\n  ./in uninstall\n  ./in --version\n\nThen use your normal ssh, mosh and CLI commands.\n")
		return nil
	}
	switch args[0] {
	case "--version", "version":
		if len(args) != 1 {
			return fmt.Errorf("version takes no arguments")
		}
		fmt.Println(version)
		return nil
	case "setup":
		return setup(ctx, args[1:])
	case "status":
		if len(args) != 1 {
			return fmt.Errorf("status takes no arguments")
		}
		return status()
	case "stop":
		if len(args) != 1 {
			return fmt.Errorf("stop takes no arguments")
		}
		return stop()
	case "uninstall":
		if len(args) != 1 {
			return fmt.Errorf("uninstall takes no arguments")
		}
		return uninstall()
	case "browse":
		if len(args) != 2 {
			return fmt.Errorf("browser hook expects one URL")
		}
		return browse(ctx, args[1])
	case "connect":
		return connect(args[1:])
	case "attach":
		return attach(ctx, args[1:])
	case "relay":
		if len(args) != 1 {
			return fmt.Errorf("relay takes no arguments")
		}
		return remoteRelay(ctx)
	case "pipe":
		if len(args) != 4 {
			return fmt.Errorf("invalid callback endpoint")
		}
		return remotePipe(ctx, args[1], args[2], args[3])
	default:
		return fmt.Errorf("unknown command; run ./in --help")
	}
}
