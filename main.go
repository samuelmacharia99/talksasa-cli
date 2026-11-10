// Command talksasa runs Talksasa apps from a terminal or CI.
package main

import (
	"os"

	"golang.org/x/term"

	"github.com/samuelmacharia99/talksasa-cli/internal/cli"
)

// version is set by the release build (-ldflags "-X main.version=...").
var version = "dev"

func main() {
	app := cli.NewApp(version)
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		app.StdinIsTerminal = true
		app.ReadPassword = func() (string, error) {
			b, err := term.ReadPassword(fd)
			return string(b), err
		}
	}
	os.Exit(app.Run(os.Args[1:]))
}
