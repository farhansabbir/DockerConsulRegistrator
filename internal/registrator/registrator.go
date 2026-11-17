// Package registrator keeps the Consul agent's services in line with the
// runtime's containers: for every running container carrying the label
// (consul=yes by default), one
// service per published port and address family; nothing for anything else.
// It compares on every change the runtime reports (a burst of changes is one
// comparison), and on a timer in case a change was missed. It only ever
// touches the services it registered - those carrying its metadata.
package registrator

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/farhansabbir/DockerConsulRegistrator/internal/config"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/consul"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/logx"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/runtime"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/safe"
)

// settle is how long a burst of changes may take before one comparison.
const settle = 250 * time.Millisecond

// Consul is the part of the agent's API the registrator uses.
type Consul interface {
	Self(ctx context.Context) (consul.Self, error)
	Services(ctx context.Context) (map[string]consul.Service, error)
	Register(ctx context.Context, r consul.Registration) error
	Deregister(ctx context.Context, id string) error
	UpdateTTL(ctx context.Context, checkID, status, output string) error
	KVList(ctx context.Context, prefix string, index uint64, wait time.Duration) ([]consul.KVPair, uint64, error)
	KVPut(ctx context.Context, key string, value []byte) error
	KVDelete(ctx context.Context, key string) error
}

// Registrator keeps one agent in line with one runtime.
type Registrator struct {
	cfg    config.Config
	rt     runtime.Runtime
	consul Consul
	log    *logx.Logger
	// IPv6Beside is the host's IPv6 address on the interface that holds an
	// IPv4 one; tests replace it.
	IPv6Beside func(netip.Addr) netip.Addr
	// Domain is the DNS domain the agent serves, for the names in the log.
	Domain string

	trigger     chan struct{}
	swarmWorker atomic.Bool // this node is a Swarm worker: follow the KV store

	// Owned by Run's goroutine.
	ttls    map[string]TTL
	pushed  map[string]string // check ID -> status and output last sent
	ttlErr  map[string]string
	noted   map[string]bool
	lastErr string
	errAt   time.Time
}

// New returns a registrator; Run starts it.
func New(cfg config.Config, rt runtime.Runtime, c Consul, log *logx.Logger) *Registrator {
	return &Registrator{
		cfg: cfg, rt: rt, consul: c, log: log,
		IPv6Beside: func(netip.Addr) netip.Addr { return netip.Addr{} },
		Domain:     "consul",
		trigger:    make(chan struct{}, 1),
		ttls:       map[string]TTL{},
		pushed:     map[string]string{},
		ttlErr:     map[string]string{},
		noted:      map[string]bool{},
	}
}

// Trigger asks for a comparison soon.
func (r *Registrator) Trigger() {
	select {
	case r.trigger <- struct{}{}:
	default:
	}
}

// Run keeps Consul in line until ctx is done.
func (r *Registrator) Run(ctx context.Context) error {
	go r.watch(ctx)
	go r.watchSwarmServices(ctx)
	resync := time.NewTicker(r.cfg.Resync)
	defer resync.Stop()
	refresh := time.NewTicker(r.cfg.CheckInterval)
	defer refresh.Stop()
	var settled, retry <-chan time.Time
	r.Trigger()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-r.trigger:
			if settled == nil {
				settled = time.After(settle)
			}
		case <-settled:
			settled = nil
			if err := r.Reconcile(ctx); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				r.failed(err)
				retry = time.After(5 * time.Second)
			} else {
				r.recovered()
			}
		case <-retry:
			retry = nil
			r.Trigger()
		case <-resync.C:
			r.Trigger()
		case <-refresh.C:
			r.pushTTLs(ctx, true)
		}
	}
}

// watch follows the runtime's changes, and follows them again when the
// stream ends - a runtime restarting ends it - comparing everything anew
// each time, since changes may have been missed.
func (r *Registrator) watch(ctx context.Context) {
	backoff := time.Second
	for {
		err := r.rt.Watch(ctx, []string{r.cfg.LabelKey, runtime.LabelSwarmServiceID}, func() {
			backoff = time.Second
			r.log.OK("following the runtime's container events", "runtime", r.rt.Kind())
			r.Trigger()
		}, func(what string) {
			r.log.Debug("a container changed", "event", what)
			r.Trigger()
		})
		if ctx.Err() != nil {
			return
		}
		r.log.Warn("the runtime's event stream ended; following it again", "err", err, "in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, 30*time.Second)
		r.Trigger()
	}
}

func (r *Registrator) failed(err error) {
	msg := err.Error()
	if msg != r.lastErr || time.Since(r.errAt) > time.Minute {
		r.log.Warn("could not bring Consul in line with the containers; trying again", "err", err)
		r.lastErr, r.errAt = msg, time.Now()
	}
}

func (r *Registrator) recovered() {
	if r.lastErr != "" {
		r.log.OK("Consul is in line with the containers again")
		r.lastErr = ""
	}
}

// Reconcile compares the containers with the agent's services once, and
// registers and deregisters what differs.
func (r *Registrator) Reconcile(ctx context.Context) error {
	self, err := r.consul.Self(ctx)
	if err != nil {
		return err
	}
	addrs, err := r.addrs(self)
	if err != nil {
		return err
	}
	host, err := r.rt.Host(ctx)
	if err != nil {
		return err
	}
	r.swarmWorker.Store(host.SwarmAddr != "" && !host.SwarmManager)
	seen := map[string]bool{}
	svcs, err := r.swarmServices(ctx, host, seen)
	if err != nil {
		return err
	}
	list, err := r.rt.Containers(ctx, r.cfg.LabelKey, runtime.LabelSwarmServiceID)
	if err != nil {
		return err
	}
	var labelled []runtime.Container
	for _, c := range list {
		sid := c.Labels[runtime.LabelSwarmServiceID]
		_, serviceLabelled := svcs[sid]
		v, has := c.Labels[r.cfg.LabelKey]
		switch {
		case v == r.cfg.LabelValue || (sid != "" && serviceLabelled):
			labelled = append(labelled, c)
			if sid != "" && !serviceLabelled && len(c.Ports) == 0 && host.SwarmAddr != "" && !host.SwarmManager {
				r.noteOnce(seen, Note{Key: "mesh/" + sid, Msg: "a Swarm task's routing-mesh ports are not known here: the registrator on a manager writes them to Consul - is one running?",
					KV: []any{"swarm_service", safe.Short(c.Labels[runtime.LabelSwarmService], 80)}})
			}
		case has:
			r.noteOnce(seen, Note{Key: c.ID + "/label", Msg: "a container is not registered: its label's value is not the one to register",
				KV: []any{"container", safe.Short(c.Name, 80), "label", r.cfg.LabelKey + "=" + safe.Short(v, 40), "registers", r.cfg.LabelKey + "=" + r.cfg.LabelValue}})
		}
	}
	want, notes := Desired(labelled, meshPorts(svcs), Options{
		Addrs: addrs, Node: host.Name, Tags: r.cfg.Tags, Check: r.cfg.Check,
		Interval: r.cfg.CheckInterval, DeregisterAfter: r.cfg.DeregisterAfter,
	})
	for _, n := range notes {
		r.noteOnce(seen, n)
	}
	r.noted = seen // a problem that went away and comes back is said again

	have, err := r.consul.Services(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, id := range sortedIDs(want) {
		w := want[id]
		if h, ok := have[id]; ok && h.Meta[MetaOwner] == Owner && h.Meta[MetaHash] == w.Hash {
			continue
		}
		kv := []any{"service", w.Reg.Name, "dns", r.dnsName(w.Reg), "address", net.JoinHostPort(w.Reg.Address, strconv.Itoa(w.Reg.Port)),
			"container", safe.Short(w.Container, 80), "check", checkKind(w.Reg.Check), "id", id}
		if r.cfg.Ignore {
			r.log.OK("would register (dry run)", kv...)
			continue
		}
		if err := r.consul.Register(ctx, w.Reg); err != nil {
			errs = append(errs, fmt.Errorf("registering %s: %w", id, err))
			continue
		}
		r.log.OK("registered", kv...)
		if w.TTL != nil {
			delete(r.pushed, w.TTL.CheckID) // send the output too
		}
	}
	for _, id := range sortedIDs(have) {
		s := have[id]
		if s.Meta[MetaOwner] != Owner {
			continue
		}
		if _, ok := want[id]; ok {
			continue
		}
		kv := []any{"service", s.Service, "address", net.JoinHostPort(s.Address, strconv.Itoa(s.Port)), "id", id}
		if r.cfg.Ignore {
			r.log.OK("would deregister (dry run)", kv...)
			continue
		}
		if err := r.consul.Deregister(ctx, id); err != nil && !errors.Is(err, consul.ErrNotFound) {
			errs = append(errs, fmt.Errorf("deregistering %s: %w", id, err))
			continue
		}
		r.log.OK("deregistered", kv...)
	}

	ttls := map[string]TTL{}
	for _, w := range want {
		if w.TTL != nil {
			ttls[w.TTL.CheckID] = *w.TTL
		}
	}
	r.ttls = ttls
	r.pushTTLs(ctx, false)
	return errors.Join(errs...)
}

// pushTTLs sends the TTL checks' statuses: the changed ones, or all of them
// (they expire unless sent within their TTL).
func (r *Registrator) pushTTLs(ctx context.Context, all bool) {
	for id := range r.pushed {
		if _, ok := r.ttls[id]; !ok {
			delete(r.pushed, id)
			delete(r.ttlErr, id)
		}
	}
	if r.cfg.Ignore {
		return
	}
	for _, id := range sortedIDs(r.ttls) {
		t := r.ttls[id]
		key := t.Status + "\x00" + t.Output
		prev, sent := r.pushed[id]
		if !all && sent && prev == key {
			continue
		}
		if err := r.consul.UpdateTTL(ctx, id, t.Status, t.Output); err != nil {
			if errors.Is(err, consul.ErrNotFound) {
				r.Trigger() // the agent lost the service: register it again
			}
			if r.ttlErr[id] != err.Error() {
				r.log.Warn("a health check update failed", "check", id, "err", err)
				r.ttlErr[id] = err.Error()
			}
			continue
		}
		delete(r.ttlErr, id)
		if !sent || strings.SplitN(prev, "\x00", 2)[0] != t.Status {
			r.log.OK("health", "check", id, "status", t.Status, "output", t.Output)
		}
		r.pushed[id] = key
	}
}

func (r *Registrator) noteOnce(seen map[string]bool, n Note) {
	seen[n.Key] = true
	if !r.noted[n.Key] {
		r.log.Warn(n.Msg, n.KV...)
	}
}

// addrs are the host addresses to register: --address, else the agent's
// own address and the IPv6 address beside it.
func (r *Registrator) addrs(self consul.Self) (Addrs, error) {
	var a Addrs
	if len(r.cfg.Addresses) > 0 {
		for _, s := range r.cfg.Addresses {
			ip := netip.MustParseAddr(s) // checked by config.Validate
			if ip.Is4() {
				a.V4 = ip
			} else {
				a.V6 = ip
			}
		}
		return a, nil
	}
	ip, err := netip.ParseAddr(self.Member.Addr)
	if err != nil || ip.Zone() != "" || ip.IsUnspecified() {
		return a, fmt.Errorf("the Consul agent advertises %q, which is not an address to register; give --address", safe.Short(self.Member.Addr, 60))
	}
	ip = ip.Unmap()
	if ip.Is4() {
		a.V4 = ip
		a.V6 = r.IPv6Beside(ip)
	} else {
		a.V6 = ip
	}
	return a, nil
}

func (r *Registrator) dnsName(reg consul.Registration) string {
	return reg.Name + ".service." + r.Domain
}

func checkKind(c *consul.Check) string {
	switch {
	case c == nil:
		return "none"
	case c.TTL != "":
		return "ttl"
	case c.TCP != "":
		return "tcp"
	}
	return "other"
}
