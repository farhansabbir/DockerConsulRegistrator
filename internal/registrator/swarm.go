package registrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/farhansabbir/DockerConsulRegistrator/internal/runtime"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/safe"
)

// A Swarm service is registered when it carries consul=yes - on the service
// (docker service create --label) or on its containers (--container-label) -
// and its routing-mesh ports are registered too. Only managers can read a
// Swarm's services; workers cannot. So the registrators on managers write
// the services to be registered into Consul's KV store, and the registrators
// on workers read them from there:
//
//	docker-consul-registrator/swarm/<Swarm ID>/<service ID> = {"name", "ports"}
//
// Each node still registers only the tasks running on it, at its own address.

// KVPrefix is where the managers write the Swarm services to register.
const KVPrefix = "docker-consul-registrator/swarm/"

const maxKVValue = 64 << 10

var swarmID = regexp.MustCompile(`^[0-9a-z]{1,64}$`)

// swarmService is a Swarm service to register, as written to the KV store.
type swarmService struct {
	Name  string      `json:"name"`
	Ports []swarmPort `json:"ports,omitempty"` // its routing-mesh ports
}

type swarmPort struct {
	Proto     string `json:"proto"`
	Target    int    `json:"target"`
	Published int    `json:"published"`
}

// swarmServices returns the Swarm services to register, by ID: from the
// runtime on a manager - which writes them for the workers - and from the
// KV store on a worker. Outside a Swarm, none.
func (r *Registrator) swarmServices(ctx context.Context, host runtime.Host, seen map[string]bool) (map[string]swarmService, error) {
	if host.SwarmAddr == "" {
		return nil, nil
	}
	if !host.SwarmManager {
		return r.readSwarmServices(ctx, seen)
	}
	list, err := r.rt.SwarmServices(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]swarmService{}
	for _, s := range list {
		if s.Labels[Label] != "yes" && s.ContainerLabels[Label] != "yes" {
			if v, ok := s.Labels[Label]; ok {
				r.noteOnce(seen, Note{Key: "service/" + s.ID + "/label", Msg: "a Swarm service is not registered: its consul label is not yes",
					KV: []any{"swarm_service", safe.Short(s.Name, 80), "consul", safe.Short(v, 40)}})
			}
			continue
		}
		svc := swarmService{Name: s.Name}
		for _, p := range s.Ports {
			if p.Mode == "ingress" {
				svc.Ports = append(svc.Ports, swarmPort{Proto: p.Proto, Target: p.Target, Published: p.HostPort})
			}
		}
		sort.Slice(svc.Ports, func(i, j int) bool {
			a, b := svc.Ports[i], svc.Ports[j]
			if a.Published != b.Published {
				return a.Published < b.Published
			}
			return a.Proto < b.Proto
		})
		out[s.ID] = svc
	}
	if err := r.writeSwarmServices(ctx, host.SwarmCluster, out); err != nil {
		r.noteOnce(seen, Note{Key: "kv-write", Msg: "the Swarm's services could not be written for the workers' registrators", KV: []any{"err", err}})
	}
	return out, nil
}

// writeSwarmServices brings the KV store's copy of a Swarm's services in
// line: written when changed, removed when gone. Every manager writes the
// same, from the same Swarm.
func (r *Registrator) writeSwarmServices(ctx context.Context, cluster string, svcs map[string]swarmService) error {
	if !swarmID.MatchString(cluster) {
		return fmt.Errorf("the Swarm's ID %q is not one", safe.Short(cluster, 70))
	}
	prefix := KVPrefix + cluster + "/"
	have, _, err := r.consul.KVList(ctx, prefix, 0, 0)
	if err != nil {
		return err
	}
	existing := map[string]string{}
	for _, kv := range have {
		existing[kv.Key] = string(kv.Value)
	}
	var errs []error
	for _, id := range sortedIDs(svcs) {
		key := prefix + id
		b, _ := json.Marshal(svcs[id])
		if existing[key] == string(b) {
			continue
		}
		if r.cfg.Ignore {
			r.log.OK("would write a Swarm service for the workers (dry run)", "key", key)
			continue
		}
		if err := r.consul.KVPut(ctx, key, b); err != nil {
			errs = append(errs, err)
			continue
		}
		r.log.OK("wrote a Swarm service for the workers", "swarm_service", safe.Short(svcs[id].Name, 80), "key", key)
	}
	for key := range existing {
		if _, ok := svcs[strings.TrimPrefix(key, prefix)]; ok {
			continue
		}
		if r.cfg.Ignore {
			continue
		}
		if err := r.consul.KVDelete(ctx, key); err != nil {
			errs = append(errs, err)
			continue
		}
		r.log.OK("removed a Swarm service the workers no longer register", "key", key)
	}
	return errors.Join(errs...)
}

// readSwarmServices reads the Swarm services the managers wrote, checking
// each as what it is: input.
func (r *Registrator) readSwarmServices(ctx context.Context, seen map[string]bool) (map[string]swarmService, error) {
	kvs, _, err := r.consul.KVList(ctx, KVPrefix, 0, 0)
	if err != nil {
		return nil, err
	}
	out := map[string]swarmService{}
	for _, kv := range kvs {
		parts := strings.Split(strings.TrimPrefix(kv.Key, KVPrefix), "/")
		if len(parts) != 2 || !swarmID.MatchString(parts[0]) || !swarmID.MatchString(parts[1]) {
			continue
		}
		svc, ok := parseSwarmService(kv.Value)
		if !ok {
			r.noteOnce(seen, Note{Key: "kv/" + kv.Key, Msg: "a Swarm service in the KV store is not one; left out", KV: []any{"key", safe.Short(kv.Key, 120)}})
			continue
		}
		out[parts[1]] = svc
	}
	return out, nil
}

func parseSwarmService(b []byte) (swarmService, bool) {
	var s swarmService
	if len(b) > maxKVValue || json.Unmarshal(b, &s) != nil || s.Name == "" || len(s.Name) > 255 || len(s.Ports) > 1000 {
		return s, false
	}
	for _, p := range s.Ports {
		if p.Proto != "tcp" && p.Proto != "udp" && p.Proto != "sctp" ||
			p.Target < 1 || p.Target > 65535 || p.Published < 1 || p.Published > 65535 {
			return s, false
		}
	}
	return s, true
}

// meshPorts are the routing-mesh ports of the services, as runtime ports.
func meshPorts(svcs map[string]swarmService) map[string][]runtime.Port {
	out := map[string][]runtime.Port{}
	for id, s := range svcs {
		for _, p := range s.Ports {
			out[id] = append(out[id], runtime.Port{Proto: p.Proto, Target: p.Target, HostPort: p.Published, Mode: "ingress"})
		}
	}
	return out
}

// watchSwarmServices follows the KV store on a worker, and asks for a
// comparison when the managers change what is to be registered - a service
// labelled, published or removed.
func (r *Registrator) watchSwarmServices(ctx context.Context) {
	var index uint64
	for ctx.Err() == nil {
		if !r.swarmWorker.Load() {
			index = 0
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}
		_, next, err := r.consul.KVList(ctx, KVPrefix, max(index, 1), time.Minute)
		if err != nil {
			if ctx.Err() == nil {
				r.log.Debug("following the Swarm's services in the KV store failed; again soon", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}
		if index != 0 && next != index {
			r.log.Debug("the Swarm's services changed in the KV store")
			r.Trigger()
		}
		if next < index {
			next = 0 // the store's index went back: start over
		}
		index = next
	}
}
