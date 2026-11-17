// Package hostnet finds the host's addresses the registrator registers and
// the bundled agent binds to: the IPv4 address of the default route's
// interface, and the IPv6 address beside an IPv4 one. The registrator runs on
// the host's network (a container with --network host), so the host's
// interfaces are its own.
package hostnet

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"sort"
	"strconv"
	"strings"
)

// DefaultIPv4 is the first IPv4 address of the interface the default route
// goes through.
func DefaultIPv4() (netip.Addr, error) {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return netip.Addr{}, err
	}
	defer func() { _ = f.Close() }()
	ifname, err := defaultInterface(f)
	if err != nil {
		return netip.Addr{}, err
	}
	ifc, err := net.InterfaceByName(ifname)
	if err != nil {
		return netip.Addr{}, err
	}
	addrs, err := ifc.Addrs()
	if err != nil {
		return netip.Addr{}, err
	}
	for _, a := range addrs {
		if p, err := netip.ParsePrefix(a.String()); err == nil && p.Addr().Is4() && p.Addr().IsGlobalUnicast() {
			return p.Addr(), nil
		}
	}
	return netip.Addr{}, fmt.Errorf("the default route's interface %s has no IPv4 address", ifname)
}

// defaultInterface reads /proc/net/route for the default route's interface:
// destination and mask 0, up (flag 1), lowest metric.
func defaultInterface(r io.Reader) (string, error) {
	sc := bufio.NewScanner(r)
	best, metric := "", -1
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 8 || f[0] == "Iface" {
			continue
		}
		flags, err := strconv.ParseUint(f[3], 16, 32)
		if err != nil || f[1] != "00000000" || f[7] != "00000000" || flags&1 == 0 {
			continue
		}
		m, err := strconv.Atoi(f[6])
		if err != nil {
			continue
		}
		if metric < 0 || m < metric {
			best, metric = f[0], m
		}
	}
	if best == "" {
		return "", errors.New("no default IPv4 route")
	}
	return best, nil
}

// IPv6Beside is the host's IPv6 address to register on the interface that
// holds v4: global, stable (not a temporary privacy address, not deprecated,
// tentative or failed), a global unicast one before a unique local one. It is
// the zero Addr when there is none.
func IPv6Beside(v4 netip.Addr) netip.Addr {
	ifname := interfaceOf(v4)
	if ifname == "" {
		return netip.Addr{}
	}
	f, err := os.Open("/proc/net/if_inet6")
	if err != nil {
		return netip.Addr{}
	}
	defer func() { _ = f.Close() }()
	return stableIPv6(f, ifname)
}

func interfaceOf(v4 netip.Addr) string {
	ifcs, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, ifc := range ifcs {
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if p, err := netip.ParsePrefix(a.String()); err == nil && p.Addr() == v4 {
				return ifc.Name
			}
		}
	}
	return ""
}

// The address flags of /proc/net/if_inet6 that rule an address out.
const (
	flagTemporary  = 0x01
	flagDADFailed  = 0x08
	flagDeprecated = 0x20
	flagTentative  = 0x40
	flagPermanent  = 0x80 // configured, not autoconfigured
)

// stableIPv6 reads /proc/net/if_inet6 lines - address, index, prefix length,
// scope, flags, interface - for ifname's address to register.
func stableIPv6(r io.Reader, ifname string) netip.Addr {
	type cand struct {
		a         netip.Addr
		permanent bool
	}
	var cs []cand
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 6 || f[5] != ifname || f[3] != "00" || len(f[0]) != 32 {
			continue
		}
		flags, err := strconv.ParseUint(f[4], 16, 32)
		if err != nil || flags&(flagTemporary|flagDADFailed|flagDeprecated|flagTentative) != 0 {
			continue
		}
		var b [16]byte
		ok := true
		for i := range b {
			v, err := strconv.ParseUint(f[0][2*i:2*i+2], 16, 8)
			if err != nil {
				ok = false
				break
			}
			b[i] = byte(v)
		}
		a := netip.AddrFrom16(b)
		if !ok || !a.IsGlobalUnicast() || a.Is4In6() {
			continue
		}
		cs = append(cs, cand{a, flags&flagPermanent != 0})
	}
	if len(cs) == 0 {
		return netip.Addr{}
	}
	sort.Slice(cs, func(i, j int) bool {
		gi, gj := !cs[i].a.IsPrivate(), !cs[j].a.IsPrivate() // global unicast before unique local
		if gi != gj {
			return gi
		}
		if cs[i].permanent != cs[j].permanent {
			return cs[i].permanent
		}
		return cs[i].a.Less(cs[j].a)
	})
	return cs[0].a
}
