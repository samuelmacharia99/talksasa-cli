package cli

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// stdin is one buffered reader for the whole run, so two reads in a
// command never lose what the first buffered.
func (a *App) stdin() *bufio.Reader {
	if a.in == nil {
		a.in = bufio.NewReader(a.Stdin)
	}
	return a.in
}

// confirm asks before something destructive. --yes answers it; without a
// terminal to ask on, the command refuses rather than guess.
func (a *App) confirm(question string) error {
	if a.flags.yes {
		return nil
	}
	if !a.StdinIsTerminal {
		return &usageError{msg: question + " Pass --yes to confirm when not running in a terminal."}
	}
	fmt.Fprintf(a.Stderr, "%s [y/N] ", question)
	answer, err := a.stdin().ReadString('\n')
	if err != nil && err != io.EOF {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return nil
	}
	return withExit(ExitFailed, fmt.Errorf("cancelled"))
}

// readSecret reads a secret: hidden from a terminal, or from piped input.
// From a pipe, whole takes everything (a value may span lines, such as a
// private key) less one trailing newline; otherwise the first line.
func (a *App) readSecret(prompt string, whole bool) (string, error) {
	if a.StdinIsTerminal && a.ReadPassword != nil {
		fmt.Fprint(a.Stderr, prompt)
		value, err := a.ReadPassword()
		fmt.Fprintln(a.Stderr)
		return value, err
	}
	if whole {
		raw, err := io.ReadAll(io.LimitReader(a.stdin(), 1<<20))
		if err != nil {
			return "", err
		}
		value := strings.TrimSuffix(string(raw), "\n")
		return strings.TrimSuffix(value, "\r"), nil
	}
	line, err := a.stdin().ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
