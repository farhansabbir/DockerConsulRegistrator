// Package secret reads the secrets the registrator is given - ACL tokens and
// the gossip key - from the environment or from a file, and checks them.
// Secrets never come from the command line (any process can read another's
// arguments) and are never logged; errors name where a secret came from,
// never what it holds.
package secret

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strings"
)

// MaxFile is the largest secret file read.
const MaxFile = 4096

// Kind is what a secret is, and how it is checked.
type Kind int

// The kinds.
const (
	Token     Kind = iota // a Consul ACL token
	GossipKey             // a Consul gossip encryption key
)

// Lookup is how the environment is read: os.LookupEnv, or a test's.
type Lookup func(string) (string, bool)

// From reads a secret: from the environment variable name, or the file the
// variable name+"_FILE" (or flagFile, from the command line) names. ""
// means none was given. Both given is an error.
func From(env Lookup, name, flagFile string, kind Kind) (string, error) {
	value, hasValue := env(name)
	file, hasFile := env(name + "_FILE")
	if flagFile != "" {
		file, hasFile = flagFile, true
	}
	switch {
	case hasValue && value != "" && hasFile && file != "":
		return "", fmt.Errorf("%s and %s_FILE are both set: set one", name, name)
	case hasValue && value != "":
		if err := Check(value, kind); err != nil {
			return "", fmt.Errorf("%s %v", name, err)
		}
		return value, nil
	case hasFile && file != "":
		v, err := Read(file, kind)
		if err != nil {
			return "", fmt.Errorf("%s_FILE: %v", name, err)
		}
		return v, nil
	}
	return "", nil
}

// Read reads a secret from a regular file of at most MaxFile bytes, and
// checks it.
func Read(path string, kind Kind) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", path)
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxFile+1))
	if err != nil {
		return "", err
	}
	if len(b) > MaxFile {
		return "", fmt.Errorf("%s is larger than %d bytes", path, MaxFile)
	}
	v := strings.TrimSpace(string(b))
	if err := Check(v, kind); err != nil {
		return "", fmt.Errorf("%s: it %v", path, err)
	}
	return v, nil
}

// Check is what is wrong with a secret of kind, or nil. Its value is never
// in the error.
func Check(v string, kind Kind) error {
	if v == "" {
		return fmt.Errorf("is empty")
	}
	if len(v) > 512 {
		return fmt.Errorf("is longer than 512 characters")
	}
	for i := 0; i < len(v); i++ {
		if v[i] <= ' ' || v[i] > '~' {
			return fmt.Errorf("holds a space or a character no %s has", kind)
		}
	}
	if kind == GossipKey {
		b, err := base64.StdEncoding.DecodeString(v)
		if err != nil || (len(b) != 16 && len(b) != 24 && len(b) != 32) {
			return fmt.Errorf("is not a gossip key: base64 of 16, 24 or 32 bytes (consul keygen)")
		}
	}
	return nil
}

func (k Kind) String() string {
	if k == GossipKey {
		return "gossip key"
	}
	return "token"
}
