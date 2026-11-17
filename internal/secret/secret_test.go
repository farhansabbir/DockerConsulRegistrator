package secret

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func env(m map[string]string) Lookup {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

const key = "pUqJrVyVRj5jsiYEkM/tFQYfWyJIv4s3XkvDwy7Cu5s=" // 32 bytes

func TestFrom(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "token")
	if err := os.WriteFile(file, []byte("  tok-from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		env      map[string]string
		flagFile string
		want     string
		err      string
	}{
		"none":           {env: map[string]string{}, want: ""},
		"value":          {env: map[string]string{"T": "tok"}, want: "tok"},
		"file":           {env: map[string]string{"T_FILE": file}, want: "tok-from-file"},
		"flag file":      {env: map[string]string{}, flagFile: file, want: "tok-from-file"},
		"both":           {env: map[string]string{"T": "tok", "T_FILE": file}, err: "both set"},
		"value and flag": {env: map[string]string{"T": "tok"}, flagFile: file, err: "both set"},
		"empty value":    {env: map[string]string{"T": ""}, want: ""},
		"bad value":      {env: map[string]string{"T": "two words"}, err: "T holds a space"},
		"missing file":   {env: map[string]string{"T_FILE": filepath.Join(dir, "nope")}, err: "T_FILE"},
		"directory":      {env: map[string]string{"T_FILE": dir}, err: "not a regular file"},
	} {
		got, err := From(env(c.env), "T", c.flagFile, Token)
		switch {
		case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)):
			t.Errorf("%s: err = %v, want %q", name, err, c.err)
		case c.err == "" && (err != nil || got != c.want):
			t.Errorf("%s: %q, %v; want %q", name, got, err, c.want)
		}
	}
}

func TestCheck(t *testing.T) {
	if err := Check(key, GossipKey); err != nil {
		t.Errorf("a gossip key: %v", err)
	}
	for _, bad := range []string{"", "short", "bm90LWEta2V5", strings.Repeat("A", 600)} {
		if Check(bad, GossipKey) == nil {
			t.Errorf("gossip key %q accepted", bad)
		}
	}
	if err := Check("6b6c0e4f-1b9a-4c1e-9d6f-2f0e5d3a7b11", Token); err != nil {
		t.Errorf("a token: %v", err)
	}
	if err := Check("s3cr3t\x1b[2J", Token); err == nil || strings.Contains(err.Error(), "s3cr3t") {
		t.Errorf("a token with an escape: %v (the error must not hold the value)", err)
	}
}
