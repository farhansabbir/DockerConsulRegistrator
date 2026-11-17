package logx

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func TestLine(t *testing.T) {
	at := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	got := Line(at, "registrator", OK, "registered", "service", "web", "address", "192.168.1.93:8080", "err", errors.New("a b"), "empty", "", "odd")
	want := `2026-11-16T12:00:00.000Z [registrator] OK registered service=web address=192.168.1.93:8080 err="a b" empty="" odd=!MISSING`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestOutsideTextCannotEndALine(t *testing.T) {
	var b bytes.Buffer
	l := New(&b, "m")
	l.Warn("container\nname", "name", "evil\n2026-01-01T00:00:00.000Z [registrator] OK forged", "esc", "\x1b[2J")
	if n := bytes.Count(b.Bytes(), []byte("\n")); n != 1 {
		t.Fatalf("one event wrote %d lines: %q", n, b.String())
	}
	if bytes.ContainsRune(b.Bytes(), 0x1b) {
		t.Fatalf("an escape reached the log: %q", b.String())
	}
}

func TestDebugOnlyWhenOn(t *testing.T) {
	var b bytes.Buffer
	l := New(&b, "m")
	l.Debug("hidden")
	if b.Len() != 0 {
		t.Fatal("DEBUG written while off")
	}
	l.Module("other").SetDebug(true)
	l.Debug("shown")
	if !bytes.Contains(b.Bytes(), []byte("DEBUG shown")) {
		t.Fatalf("DEBUG not written while on: %q", b.String())
	}
}
