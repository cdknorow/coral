package lsp

import (
	"context"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

type Status string

const (
	StatusStarting    Status = "starting"
	StatusReady       Status = "ready"
	StatusUnavailable Status = "unavailable"
	StatusFailed      Status = "failed"
	StatusUnsupported Status = "unsupported"
)

type Instance struct {
	Server     *Server
	Workspace  string
	Definition ServerDefinition
}
type managed struct {
	instance     *Instance
	clients      int
	leases       map[string]string
	lastActivity time.Time
}
type Manager struct {
	mu                                           sync.Mutex
	servers                                      map[string]*managed
	startupTimeout, shutdownTimeout, idleTimeout time.Duration
	stop, stopped, closeDone                     chan struct{}
	closeOnce                                    sync.Once
	closed                                       bool
}

func NewManager() *Manager {
	m := &Manager{servers: map[string]*managed{}, startupTimeout: 10 * time.Second, shutdownTimeout: 3 * time.Second, idleTimeout: 5 * time.Minute, stop: make(chan struct{}), stopped: make(chan struct{}), closeDone: make(chan struct{})}
	go m.reap()
	return m
}
func key(workspace, id string) string { return workspace + "\x00" + id }

func (m *Manager) Availability(def ServerDefinition) Status {
	if _, err := exec.LookPath(def.Command); err != nil {
		return StatusUnavailable
	}
	return StatusStarting
}
func (m *Manager) Connect(ctx context.Context, def ServerDefinition, workspace, client string) (*Instance, Status, error) {
	canonical, err := Canonical(workspace)
	if err != nil {
		return nil, StatusFailed, err
	}
	k := key(canonical, def.ID)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, StatusFailed, fmt.Errorf("language server manager is closed")
	}
	if x := m.servers[k]; x != nil {
		if !x.instance.Server.Alive() {
			delete(m.servers, k)
		} else {
			x.clients++
			x.lastActivity = time.Now()
			instance := x.instance
			m.mu.Unlock()
			return instance, StatusReady, nil
		}
	}
	m.mu.Unlock()
	if _, err := exec.LookPath(def.Command); err != nil {
		return nil, StatusUnavailable, err
	}
	server, err := Start(ctx, def, canonical, m.startupTimeout)
	if err != nil {
		return nil, StatusFailed, err
	}
	instance := &Instance{Server: server, Workspace: canonical, Definition: def}
	m.mu.Lock()
	if existing := m.servers[k]; existing != nil {
		existing.clients++
		m.mu.Unlock()
		server.Shutdown(m.shutdownTimeout)
		return existing.instance, StatusReady, nil
	}
	m.servers[k] = &managed{instance: instance, clients: 1, leases: map[string]string{}, lastActivity: time.Now()}
	m.mu.Unlock()
	return instance, StatusReady, nil
}
func (m *Manager) Disconnect(instance *Instance, client string) {
	m.mu.Lock()
	x := m.servers[key(instance.Workspace, instance.Definition.ID)]
	if x == nil || x.instance != instance {
		m.mu.Unlock()
		return
	}
	if x.clients > 0 {
		x.clients--
	}
	var closed []string
	for identity, owner := range x.leases {
		if owner == client {
			delete(x.leases, identity)
			if uri, err := PathToURI(identity); err == nil {
				closed = append(closed, uri)
			}
		}
	}
	x.lastActivity = time.Now()
	m.mu.Unlock()
	for _, uri := range closed {
		_ = x.instance.Server.Notify("textDocument/didClose", map[string]any{"textDocument": map[string]any{"uri": uri}})
	}
}
func (m *Manager) Lease(instance *Instance, uri, client string) error {
	identity, err := documentIdentity(instance.Workspace, uri)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	x := m.servers[key(instance.Workspace, instance.Definition.ID)]
	if x == nil || x.instance != instance {
		return fmt.Errorf("server unavailable")
	}
	if owner := x.leases[identity]; owner != "" && owner != client {
		return fmt.Errorf("document is being edited by another client")
	}
	x.leases[identity] = client
	x.lastActivity = time.Now()
	return nil
}
func (m *Manager) Release(instance *Instance, uri, client string) {
	identity, err := documentIdentity(instance.Workspace, uri)
	if err != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	x := m.servers[key(instance.Workspace, instance.Definition.ID)]
	if x != nil && x.instance == instance && x.leases[identity] == client {
		delete(x.leases, identity)
	}
}
func (m *Manager) Owns(instance *Instance, uri, client string) bool {
	identity, err := documentIdentity(instance.Workspace, uri)
	if err != nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	x := m.servers[key(instance.Workspace, instance.Definition.ID)]
	return x != nil && x.instance == instance && x.leases[identity] == client
}

// documentIdentity is the sole lease key: one canonical on-disk file has one
// identity regardless of the accepted URI spelling used by a browser.
func documentIdentity(workspace, uri string) (string, error) {
	return URIToPath(workspace, uri)
}
func (m *Manager) reap() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	defer close(m.stopped)
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
		}
		var stale []*Server
		m.mu.Lock()
		now := time.Now()
		for k, x := range m.servers {
			if x.clients == 0 && len(x.leases) == 0 && now.Sub(x.lastActivity) >= m.idleTimeout {
				stale = append(stale, x.instance.Server)
				delete(m.servers, k)
			}
		}
		m.mu.Unlock()
		for _, s := range stale {
			s.Shutdown(m.shutdownTimeout)
		}
	}
}

func (m *Manager) Close(ctx context.Context) error {
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		instances := make([]*Server, 0, len(m.servers))
		for _, x := range m.servers {
			instances = append(instances, x.instance.Server)
		}
		m.servers = make(map[string]*managed)
		close(m.stop)
		m.mu.Unlock()
		go func() {
			defer close(m.closeDone)
			<-m.stopped
			for _, server := range instances {
				server.Shutdown(m.shutdownTimeout)
			}
		}()
	})
	select {
	case <-m.closeDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
