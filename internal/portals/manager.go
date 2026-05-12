// Package portals runs embedded tsnet reverse proxies for Aperture endpoints.
package portals

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"

	"github.com/tailscale/aperture-cli/internal/config"
	"tailscale.com/tsnet"
)

// Manager owns active tsnet nodes and localhost reverse proxies.
type Manager struct {
	mu sync.Mutex

	debug bool
	nodes map[string]*nodeRuntime

	newNode func(portal config.Portal, stateDir string, userLogf, debugLogf func(string, ...any)) tailnetNode
}

type nodeRuntime struct {
	node    tailnetNode
	proxies map[string]*proxyRuntime
}

type proxyRuntime struct {
	localURL string
	server   *http.Server
	listener net.Listener
}

type tailnetNode interface {
	Up(context.Context) error
	DialContext(context.Context, string, string) (net.Conn, error)
	Close() error
}

type tsnetNode struct {
	server *tsnet.Server
}

func (n *tsnetNode) Up(ctx context.Context) error {
	_, err := n.server.Up(ctx)
	return err
}

func (n *tsnetNode) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return n.server.Dial(ctx, network, address)
}

func (n *tsnetNode) Close() error {
	return n.server.Close()
}

// NewManager returns a portal manager. When debug is true, verbose tsnet
// backend logs are also emitted to the supplied activation log sink.
func NewManager(debug bool) *Manager {
	m := &Manager{
		debug: debug,
		nodes: make(map[string]*nodeRuntime),
	}
	m.newNode = func(portal config.Portal, stateDir string, userLogf, debugLogf func(string, ...any)) tailnetNode {
		s := &tsnet.Server{
			Dir:      stateDir,
			Hostname: portal.ID,
			UserLogf: userLogf,
		}
		if debug {
			s.Logf = debugLogf
		}
		return &tsnetNode{server: s}
	}
	return m
}

// Activate starts or reuses a portal reverse proxy for remoteURL and returns
// the localhost URL clients should use.
func (m *Manager) Activate(ctx context.Context, portal config.Portal, remoteURL string, logf func(string)) (string, error) {
	if m == nil {
		return "", fmt.Errorf("portal manager is not configured")
	}
	if logf == nil {
		logf = func(string) {}
	}
	target, err := parseTarget(remoteURL)
	if err != nil {
		return "", err
	}

	m.mu.Lock()
	rt := m.nodes[portal.ID]
	needUp := false
	if rt == nil {
		stateDir, err := config.PortalStateDir(portal.ID)
		if err != nil {
			m.mu.Unlock()
			return "", err
		}
		userLogf := func(format string, args ...any) {
			logf(fmt.Sprintf(format, args...))
		}
		debugLogf := func(format string, args ...any) {
			if m.debug {
				logf(fmt.Sprintf(format, args...))
			}
		}
		node := m.newNode(portal, stateDir, userLogf, debugLogf)
		rt = &nodeRuntime{
			node:    node,
			proxies: make(map[string]*proxyRuntime),
		}
		m.nodes[portal.ID] = rt
		needUp = true
	}
	m.mu.Unlock()

	if needUp {
		logf("Starting portal " + portal.Name + " (" + portal.ID + ")")
		if err := rt.node.Up(ctx); err != nil {
			m.mu.Lock()
			if m.nodes[portal.ID] == rt {
				delete(m.nodes, portal.ID)
			}
			m.mu.Unlock()
			return "", err
		}
		logf("Portal connected.")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.nodes[portal.ID] != rt {
		return "", fmt.Errorf("portal stopped before activation completed")
	}
	key := target.String()
	if proxy := rt.proxies[key]; proxy != nil {
		return proxy.localURL, nil
	}

	proxy, err := startProxy(rt.node, target)
	if err != nil {
		return "", err
	}
	rt.proxies[key] = proxy
	logf("Listening on " + proxy.localURL)
	return proxy.localURL, nil
}

// Close shuts down all active reverse proxies and tsnet nodes.
func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []error
	for id, rt := range m.nodes {
		for key, proxy := range rt.proxies {
			if err := proxy.server.Close(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errs = append(errs, err)
			}
			if err := proxy.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				errs = append(errs, err)
			}
			delete(rt.proxies, key)
		}
		if err := rt.node.Close(); err != nil {
			errs = append(errs, err)
		}
		delete(m.nodes, id)
	}
	return errors.Join(errs...)
}

func parseTarget(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("endpoint URL is empty")
	}
	target, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if target.Scheme == "" || target.Host == "" {
		return nil, fmt.Errorf("endpoint URL must include scheme and host")
	}
	return target, nil
}

func startProxy(node tailnetNode, target *url.URL) (*proxyRuntime, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = node.DialContext

	proxy := httputil.NewSingleHostReverseProxy(target)
	director := proxy.Director
	proxy.Director = func(req *http.Request) {
		director(req)
		req.Host = target.Host
	}
	proxy.Transport = transport

	srv := &http.Server{Handler: proxy}
	go func() {
		_ = srv.Serve(ln)
	}()

	return &proxyRuntime{
		localURL: "http://" + ln.Addr().String(),
		server:   srv,
		listener: ln,
	}, nil
}
