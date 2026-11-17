package registrator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/farhansabbir/DockerConsulRegistrator/internal/config"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/consul"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/runtime"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/safe"
)

// What the registrator writes into each service it registers, so it knows
// its own and touches nothing else.
const (
	Owner     = "docker-consul-registrator"
	MetaOwner = "registered-by"
	MetaHash  = "registration-hash" // the registration without its check's status
)

// Label marks a container for registration: Label=yes.
const Label = "consul"

// Addrs are the host's addresses to register for ports published on all of
// its addresses: an IPv4 one, an IPv6 one, or both.
type Addrs struct {
	V4, V6 netip.Addr
}

// Options shape the registrations.
type Options struct {
	Addrs           Addrs
	Node            string // the host's name, recorded in each service's metadata
	Tags            []string
	Check           string
	Interval        time.Duration
	DeregisterAfter time.Duration
}

// Want is one registration the registrator wants in Consul.
type Want struct {
	Reg       consul.Registration
	Hash      string
	TTL       *TTL   // the status to keep its TTL check at, when it has one
	Container string // for the log
}

// TTL is a TTL check's wanted status.
type TTL struct {
	CheckID string
	Status  string // passing or critical
	Output  string
}

// Note is something worth saying once about a container, such as why it is
// not registered. Key says what it is about, so it is said once.
type Note struct {
	Key string
	Msg string
	KV  []any
}

// endpoint is one way into a container: a published port on one address.
type endpoint struct {
	proto   string
	target  int
	port    int
	addr    netip.Addr
	ingress string // the Swarm service, for routing-mesh ports
}

// Desired is the registrations wanted for containers - each carrying
// Label=yes - by ID: one per published port and address family, so DNS
// answers A, AAAA, SRV and PTR for each. ingress holds the routing-mesh
// ports of the Swarm services of the containers, by service ID.
func Desired(containers []runtime.Container, ingress map[string][]runtime.Port, o Options) (map[string]Want, []Note) {
	want := map[string]Want{}
	var notes []Note
	note := func(c runtime.Container, why, msg string, kv ...any) {
		notes = append(notes, Note{Key: c.ID + "/" + why, Msg: msg, KV: append([]any{"container", safe.Short(c.Name, 80)}, kv...)})
	}
	for _, c := range containers {
		if !c.Running {
			continue
		}
		base := serviceName(c)
		if base == "" {
			note(c, "name", "a container is not registered: no service name can be made from its name")
			continue
		}
		var eps []endpoint
		seen := map[string]bool{}
		add := func(e endpoint) {
			k := fmt.Sprint(e.proto, e.port, e.addr)
			if !seen[k] {
				seen[k] = true
				eps = append(eps, e)
			}
		}
		for _, p := range c.Ports {
			addr, why := hostAddr(p.HostIP, o.Addrs)
			if why != "" {
				note(c, "port"+strconv.Itoa(p.HostPort)+p.HostIP, "a published port is not registered: "+why, "port", fmt.Sprintf("%s:%d", p.HostIP, p.HostPort))
				continue
			}
			if !addr.IsValid() {
				continue // published on all IPv6 addresses, and the host has none to register
			}
			add(endpoint{proto: p.Proto, target: p.Target, port: p.HostPort, addr: addr})
		}
		if sid := c.Labels[runtime.LabelSwarmServiceID]; sid != "" {
			// The routing mesh answers on the host's IPv4 address only.
			for _, p := range ingress[sid] {
				if o.Addrs.V4.IsValid() {
					add(endpoint{proto: p.Proto, target: p.Target, port: p.HostPort, addr: o.Addrs.V4, ingress: sid})
				}
			}
		}
		if len(eps) == 0 {
			note(c, "noports", "a container labelled consul=yes publishes no port to register")
			continue
		}
		targets := map[int]bool{}
		for _, e := range eps {
			targets[e.target] = true
		}
		for _, e := range eps {
			name := base
			if len(targets) > 1 {
				name = suffixed(base, "-"+strconv.Itoa(e.target))
			}
			w := registration(c, e, name, o)
			if old, ok := want[w.Reg.ID]; ok {
				// Two tasks of one Swarm service share its mesh port: one
				// registration, healthy while either is.
				if old.TTL != nil && w.TTL != nil && rank(w.TTL.Status) > rank(old.TTL.Status) {
					old.TTL = w.TTL
				}
				want[w.Reg.ID] = old
				continue
			}
			want[w.Reg.ID] = w
		}
	}
	return want, notes
}

// serviceName is the container's service: its Swarm service, its Compose
// project and service, or its own name - as a DNS label.
func serviceName(c runtime.Container) string {
	l := c.Labels
	if s := l[runtime.LabelSwarmService]; s != "" {
		return safe.Label(s)
	}
	if s := l[runtime.LabelComposeService]; s != "" {
		if p := l[runtime.LabelComposeProject]; p != "" {
			return safe.Label(p + "-" + s)
		}
		return safe.Label(s)
	}
	return safe.Label(c.Name)
}

func suffixed(base, suffix string) string {
	if len(base)+len(suffix) > 63 {
		base = strings.TrimRight(base[:63-len(suffix)], "-")
	}
	return base + suffix
}

// hostAddr is the address to register for a port published on hostIP: the
// host's own for all addresses, else hostIP itself. why says why a port is
// not registered at all.
func hostAddr(hostIP string, a Addrs) (addr netip.Addr, why string) {
	switch hostIP {
	case "", "0.0.0.0":
		if !a.V4.IsValid() {
			return addr, "the host has no IPv4 address to register"
		}
		return a.V4, ""
	case "::":
		return a.V6, ""
	}
	ip, err := netip.ParseAddr(hostIP)
	if err != nil || ip.Zone() != "" {
		return addr, "its host address is not one"
	}
	ip = ip.Unmap()
	switch {
	case ip.IsLoopback():
		return addr, "it is published on a loopback address, which no other host can reach"
	case ip.IsLinkLocalUnicast(), ip.IsMulticast(), ip.IsUnspecified():
		return addr, "its host address is not one other hosts can reach"
	}
	return ip, ""
}

func registration(c runtime.Container, e endpoint, name string, o Options) Want {
	fam := "ipv4"
	if e.addr.Is6() {
		fam = "ipv6"
	}
	var id string
	meta := map[string]string{
		MetaOwner:        Owner,
		"docker-node":    safe.Meta(o.Node),
		"container-port": fmt.Sprintf("%d/%s", e.target, e.proto),
	}
	if e.ingress != "" {
		id = fmt.Sprintf("dcr-swarm-%s-%d-%s-%s", e.ingress, e.port, e.proto, fam)
		meta["swarm-service"] = safe.Meta(c.Labels[runtime.LabelSwarmService])
		meta["published"] = "ingress"
	} else {
		id = fmt.Sprintf("dcr-%s-%d-%s-%s", short(c.ID), e.port, e.proto, fam)
		meta["container-id"] = safe.Meta(c.ID)
		meta["container-name"] = safe.Meta(c.Name)
		if s := c.Labels[runtime.LabelSwarmService]; s != "" {
			meta["swarm-service"] = safe.Meta(s)
		}
	}
	tags := append([]string{}, o.Tags...)
	if !contains(tags, e.proto) {
		tags = append(tags, e.proto) // what `_name._udp` SRV queries match
	}
	reg := consul.Registration{
		ID:      id,
		Name:    name,
		Tags:    tags,
		Address: e.addr.String(),
		Port:    e.port,
		Meta:    meta,
	}
	w := Want{Container: c.Name}
	hostPort := net.JoinHostPort(e.addr.String(), strconv.Itoa(e.port))
	deregister := ""
	if o.DeregisterAfter > 0 {
		deregister = o.DeregisterAfter.String()
	}
	switch {
	case o.Check == config.CheckNone:
	case o.Check == config.CheckTTL || (o.Check == config.CheckAuto && c.Health != nil):
		status, output := health(c)
		reg.Check = &consul.Check{
			Name:                           "Container health",
			Notes:                          "kept up to date by " + Owner + " from the container runtime",
			TTL:                            (3 * o.Interval).String(),
			Status:                         status,
			DeregisterCriticalServiceAfter: deregister,
		}
		w.TTL = &TTL{CheckID: consul.CheckID(id), Status: status, Output: output}
	case e.proto == "tcp":
		timeout := 2 * time.Second
		if o.Interval < timeout {
			timeout = o.Interval
		}
		reg.Check = &consul.Check{
			Name:                           "TCP " + hostPort,
			TCP:                            hostPort,
			Interval:                       o.Interval.String(),
			Timeout:                        timeout.String(),
			DeregisterCriticalServiceAfter: deregister,
		}
	}
	w.Reg = reg
	w.Hash = hash(reg)
	w.Reg.Meta[MetaHash] = w.Hash
	return w
}

// health is a container's TTL status: its health check's, or passing while
// it runs when it has none.
func health(c runtime.Container) (status, output string) {
	h := c.Health
	if h == nil {
		return "passing", "running"
	}
	output = "health: " + h.Status
	if h.Status != "starting" {
		output += fmt.Sprintf(" (exit %d) %s", h.ExitCode, safe.Short(strings.TrimSpace(h.Output), 200))
	}
	if h.Status == "healthy" {
		return "passing", strings.TrimSpace(output)
	}
	return "critical", strings.TrimSpace(output) // unhealthy, or not healthy yet
}

func rank(status string) int {
	switch status {
	case "passing":
		return 2
	case "warning":
		return 1
	}
	return 0
}

// hash is the registration without its check's status, which changes
// through TTL updates, not by registering again.
func hash(r consul.Registration) string {
	if r.Check != nil {
		c := *r.Check
		c.Status = ""
		r.Check = &c
	}
	b, _ := json.Marshal(r)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// short is a container ID fit for a service ID: the first 12 characters of
// Docker's and Podman's hex IDs, a hash of containerd's, which are names.
func short(id string) string {
	if len(id) >= 12 && strings.Trim(id, "0123456789abcdef") == "" {
		return id[:12]
	}
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:6])
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// sortedIDs returns the keys of want in order, so logs read the same way
// every time.
func sortedIDs[V any](m map[string]V) []string {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
