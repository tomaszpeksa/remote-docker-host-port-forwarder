package ssh

import (
	"errors"
	"strings"
)

// QuoteShellArg encodes one literal POSIX-shell operand. Callers crossing two
// shell layers must quote the operand and the enclosing script independently.
func QuoteShellArg(value string) (string, error) {
	if strings.ContainsRune(value, '\x00') {
		return "", errors.New("shell operand contains NUL")
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'", nil
}
