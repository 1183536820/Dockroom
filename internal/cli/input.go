package cli

import (
	"bufio"
	"fmt"
	"golang.org/x/term"
	"os"
	"strings"
)

func Secret(prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprint(os.Stderr, prompt)
		b, e := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		return string(b), e
	}
	v, e := bufio.NewReaderSize(os.Stdin, 2048).ReadSlice('\n')
	if e != nil {
		return "", e
	}
	return strings.TrimRight(string(v), "\r\n"), nil
}
