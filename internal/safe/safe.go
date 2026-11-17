// Package safe keeps what the registrator is given from doing more than be
// read. Flags, Docker's names, labels and ports, and Consul's answers are
// checked where they come in; this package checks command-line arguments and
// cleans text where it goes out - to a log line, a terminal or Consul.
package safe

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxArg is the longest command-line argument the registrator takes.
const MaxArg = 4096

// Arg is why a command-line argument is refused, or nil: one that is not
// UTF-8, holds a control character (a newline, a terminal's escape) or a
// character that reorders text, or is longer than MaxArg.
func Arg(a string) error {
	switch {
	case len(a) > MaxArg:
		return fmt.Errorf("is longer than %d bytes", MaxArg)
	case !utf8.ValidString(a):
		return fmt.Errorf("is not UTF-8")
	}
	for _, r := range a {
		if unsafeRune(r) {
			return fmt.Errorf("holds the control character %U", r)
		}
	}
	return nil
}

// unsafeRune is a character that changes what a terminal shows or how a line
// ends: control characters, the line and paragraph separators, and the
// characters that reorder text.
func unsafeRune(r rune) bool {
	switch {
	case unicode.IsControl(r):
		return true
	case r == '\u2028' || r == '\u2029':
		return true
	case r >= '\u202a' && r <= '\u202e', r >= '\u2066' && r <= '\u2069', r == '\u200e', r == '\u200f', r == '\u061c':
		return true
	case r == utf8.RuneError:
		return true
	}
	return false
}

// Text is s fit for a log line or a terminal: each unsafe character, and each
// byte that is not UTF-8, a "?".
func Text(s string) string {
	if strings.IndexFunc(s, unsafeRune) < 0 && utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unsafeRune(r) {
			r = '?'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Short is Text cut at n characters, with "..." where it was cut.
func Short(s string, n int) string {
	s = Text(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "..."
}

// Label is s as a DNS label - what Consul serves over DNS for service and
// node names: lowercase letters, digits and hyphens, 63 at most, no hyphen
// first or last. Every other character becomes a hyphen, runs of hyphens one.
// It is "" when nothing of s is left.
func Label(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if len(out) > 63 {
		out = strings.TrimRight(out[:63], "-")
	}
	return out
}

// HostnameOK is whether s is a host name: dot-separated labels of letters,
// digits and hyphens, 1 to 63 each and no hyphen first or last, 253 at most.
func HostnameOK(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	for _, l := range strings.Split(s, ".") {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for i := 0; i < len(l); i++ {
			c := l[i]
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	return true
}

// TagOK is whether s can be a Consul tag given on the command line: 1 to 255
// bytes of printable ASCII, no space and no comma (commas separate tags).
func TagOK(s string) bool {
	if s == "" || len(s) > 255 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; c <= ' ' || c > '~' || c == ',' {
			return false
		}
	}
	return true
}

// Meta is s fit for a Consul metadata value: Text, at most 512 bytes.
func Meta(s string) string {
	s = Text(s)
	for len(s) > 512 {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return s
}
