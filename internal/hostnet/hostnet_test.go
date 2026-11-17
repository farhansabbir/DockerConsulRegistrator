package hostnet

import (
	"strings"
	"testing"
)

func TestDefaultInterface(t *testing.T) {
	route := `Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT
docker0	000011AC	00000000	0001	0	0	0	0000FFFF	0	0	0
wlan0	00000000	FE01A8C0	0003	0	0	600	00000000	0	0	0
enp3s0	00000000	FE01A8C0	0003	0	0	100	00000000	0	0	0
down0	00000000	FE01A8C0	0002	0	0	1	00000000	0	0	0
enp3s0	0001A8C0	00000000	0001	0	0	100	00FFFFFF	0	0	0
`
	got, err := defaultInterface(strings.NewReader(route))
	if err != nil || got != "enp3s0" {
		t.Fatalf("defaultInterface = %q, %v", got, err)
	}
	if _, err := defaultInterface(strings.NewReader("Iface\tDestination\n")); err == nil {
		t.Fatal("no default route: no error")
	}
}

func TestStableIPv6(t *testing.T) {
	inet6 := strings.Join([]string{
		"fe80000000000000021132fffe241419 02 40 20 80 enp3s0", // link-local
		"2001056af9371800a1b2c3d4e5f60718 02 40 00 01 enp3s0", // temporary
		"2001056af937180000000000000000aa 02 40 00 20 enp3s0", // deprecated
		"fd00000000000000021132fffe241419 02 40 00 00 enp3s0", // unique local
		"2001056af9371800021132fffe241419 02 40 00 00 enp3s0", // SLAAC, stable
		"2001056af9371800000000000000beef 02 40 00 00 docker0",
		"00000000000000000000000000000001 01 80 10 80 lo",
	}, "\n")
	if got := stableIPv6(strings.NewReader(inet6), "enp3s0").String(); got != "2001:56a:f937:1800:211:32ff:fe24:1419" {
		t.Fatalf("stableIPv6 = %s", got)
	}
	onlyULA := "fd00000000000000021132fffe241419 02 40 00 00 enp3s0"
	if got := stableIPv6(strings.NewReader(onlyULA), "enp3s0").String(); got != "fd00::211:32ff:fe24:1419" {
		t.Fatalf("unique local only: %s", got)
	}
	if got := stableIPv6(strings.NewReader("garbage\n12 34"), "enp3s0"); got.IsValid() {
		t.Fatalf("garbage gave %s", got)
	}
}
