// Command in connects remote CLI browser logins to a local browser.
// This scaffold exposes metadata only; the login bridge is not implemented yet.
package main

import (
	"fmt"
	"os"
)

// version is set by release builds; source builds report dev.
var version = "dev"

// main serves metadata commands and rejects unsupported operations.
func main() {
	args := os.Args[1:]
	if len(args) == 1 && (args[0] == "--version" || args[0] == "version") {
		fmt.Println(version)
		return
	}
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "help")) {
		fmt.Println("in — remote CLI login, in your local browser.")
		fmt.Println("\nUsage: in [--help | --version]")
		fmt.Println("\nDevelopment scaffold: the login bridge is not implemented yet.")
		return
	}
	fmt.Fprintln(os.Stderr, "in: unsupported command; run in --help")
	os.Exit(2)
}
