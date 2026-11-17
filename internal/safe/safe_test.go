package safe

import (
	"strings"
	"testing"
)

func TestArg(t *testing.T) {
	for _, ok := range []string{"", "unix:///var/run/docker.sock", "web,prod", "na\u00efve"} {
		if err := Arg(ok); err != nil {
			t.Errorf("Arg(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"a\nb", "x\x1b[31m", "\u202eevil", "a\u2028b", string([]byte{0xff}), strings.Repeat("a", MaxArg+1)} {
		if Arg(bad) == nil {
			t.Errorf("Arg(%q) = nil, want an error", bad)
		}
	}
}

func TestText(t *testing.T) {
	for in, want := range map[string]string{
		"plain":                   "plain",
		"two\nlines":              "two?lines",
		"\x1b[2Jcleared":          "?[2Jcleared",
		"rtl\u202eoverride":       "rtl?override",
		string([]byte{'a', 0xff}): "a?",
	} {
		if got := Text(in); got != want {
			t.Errorf("Text(%q) = %q, want %q", in, got, want)
		}
	}
	if got := Short("abcdef", 3); got != "abc..." {
		t.Errorf("Short = %q", got)
	}
}

func TestLabel(t *testing.T) {
	for in, want := range map[string]string{
		"web":                          "web",
		"MyStack_Web":                  "mystack-web",
		"/proj-web-1":                  "proj-web-1",
		"a..b__c":                      "a-b-c",
		"-x-":                          "x",
		"___":                          "",
		"\u00fcn\u00efcode":            "n-code",
		strings.Repeat("a", 70):        strings.Repeat("a", 63),
		strings.Repeat("a", 62) + "_b": strings.Repeat("a", 62),
	} {
		if got := Label(in); got != want {
			t.Errorf("Label(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTagOK(t *testing.T) {
	for _, ok := range []string{"prod", "env=prod", "traefik.enable=true", "v1.2"} {
		if !TagOK(ok) {
			t.Errorf("TagOK(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", "a b", "a,b", "tab\t", "\u00e9", strings.Repeat("a", 256)} {
		if TagOK(bad) {
			t.Errorf("TagOK(%q) = true", bad)
		}
	}
}

func TestMeta(t *testing.T) {
	if got := Meta(strings.Repeat("\u00e9", 300)); len(got) > 512 {
		t.Errorf("Meta kept %d bytes", len(got))
	}
	if got := Meta("a\nb"); got != "a?b" {
		t.Errorf("Meta = %q", got)
	}
}
