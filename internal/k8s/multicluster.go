package k8s

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"golang.org/x/sync/errgroup"
)

// Registry holds connected clusters and the session's active selection. Each
// cluster handle owns its own clients, rate limiter, and credentials, so a
// failure or throttle in one is isolated from the others.
type Registry struct {
	mu       sync.RWMutex
	clusters map[string]*Cluster
	active   string
}

// NewRegistry creates an empty cluster registry.
func NewRegistry() *Registry {
	return &Registry{clusters: map[string]*Cluster{}}
}

// Add connects and registers a cluster. The first cluster added becomes active.
func (r *Registry) Add(name, kubeContext string, inCluster bool) (*Cluster, error) {
	c, err := Connect(name, kubeContext, inCluster)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clusters[c.Name] = c
	if r.active == "" {
		r.active = c.Name
	}
	return c, nil
}

// Active returns the currently selected cluster.
func (r *Registry) Active() (*Cluster, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.clusters[r.active]
	if !ok {
		return nil, fmt.Errorf("no active cluster")
	}
	return c, nil
}

// ActiveName returns the name of the currently selected cluster (or "").
func (r *Registry) ActiveName() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.active
}

// Switch changes the active cluster (e.g. the "/cluster prod" command).
func (r *Registry) Switch(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.clusters[name]; !ok {
		return fmt.Errorf("cluster %q not registered", name)
	}
	r.active = name
	return nil
}

// Get returns a specific cluster by name.
func (r *Registry) Get(name string) (*Cluster, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.clusters[name]
	if !ok {
		return nil, fmt.Errorf("cluster %q not registered", name)
	}
	return c, nil
}

// Names lists registered clusters in stable order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.clusters))
	for n := range r.clusters {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// FanOut runs fn against every named cluster concurrently and collects results.
// Used by the audit engine to scan a fleet; per-cluster RBAC is respected
// independently because each call uses that cluster's own handle.
func (r *Registry) FanOut(ctx context.Context, names []string, fn func(context.Context, *Cluster) error) error {
	if len(names) == 0 {
		names = r.Names()
	}
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(8) // cap concurrency to protect API servers
	for _, name := range names {
		c, err := r.Get(name)
		if err != nil {
			return err
		}
		g.Go(func() error { return fn(ctx, c) })
	}
	return g.Wait()
}
