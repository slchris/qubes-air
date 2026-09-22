package orchestrator

import "fmt"

// ValidQubeName reports whether name is safe to pass to a provider API or an
// external command.
//
// This is the orchestrator's security boundary: qube names flow from HTTP
// requests into provider calls and (for snippet upload) an SSH command. We
// allow only a conservative character set (alphanumerics plus - _ .) — the same
// philosophy as qrexec.validQrexecArg — so a name can never smuggle an address
// separator, a shell metacharacter, or an extra flag. The first character must
// be alphanumeric so a leading '-' is never parsed as an option.
//
// Rejected examples: "a;rm -rf /", "$(whoami)", "a b", "--var=x", "a\"b",
// "a[0]", "".
func ValidQubeName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	first := name[0]
	if !isAlnum(first) {
		return false
	}
	for i := 0; i < len(name); i++ {
		if !isValidNameChar(name[i]) {
			return false
		}
	}
	return true
}

func isAlnum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// isValidNameChar reports whether c is allowed anywhere in a qube name.
func isValidNameChar(c byte) bool {
	return isAlnum(c) || c == '-' || c == '_' || c == '.'
}

// ErrInvalidQubeName is returned when a qube name fails ValidQubeName.
type ErrInvalidQubeName struct {
	Name string
}

func (e *ErrInvalidQubeName) Error() string {
	return fmt.Sprintf("invalid qube name %q: only alphanumerics, '-', '_' and '.' allowed (must start alphanumeric, max 64 chars)", e.Name)
}
