// Package bridges runs embedded tsnet reverse proxies for Aperture endpoints.
package bridges

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
	"time"

	"github.com/tailscale/aperture-cli/internal/config"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tsnet"
)

// Manager owns active tsnet nodes and localhost reverse proxies.
type Manager struct {
	mu sync.Mutex

	debug bool
	nodes map[string]*nodeRuntime

	newNode func(bridge config.Bridge, stateDir string, userLogf, debugLogf func(string, ...any)) tailnetNode
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
	Up(context.Context) (*ipnstate.Status, error)
	Status(context.Context) (*ipnstate.Status, error)
	DialContext(context.Context, string, string) (net.Conn, error)
	Close() error
}

type tsnetNode struct {
	server *tsnet.Server
}

func (n *tsnetNode) Up(ctx context.Context) (*ipnstate.Status, error) {
	return n.server.Up(ctx)
}

func (n *tsnetNode) Status(ctx context.Context) (*ipnstate.Status, error) {
	lc, err := n.server.LocalClient()
	if err != nil {
		return nil, err
	}
	return lc.Status(ctx)
}

func (n *tsnetNode) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return n.server.Dial(ctx, network, address)
}

func (n *tsnetNode) Close() error {
	return n.server.Close()
}

// NewManager returns a bridge manager. When debug is true, verbose tsnet
// backend logs are also emitted to the supplied activation log sink.
func NewManager(debug bool) *Manager {
	m := &Manager{
		debug: debug,
		nodes: make(map[string]*nodeRuntime),
	}
	m.newNode = func(bridge config.Bridge, stateDir string, userLogf, debugLogf func(string, ...any)) tailnetNode {
		s := &tsnet.Server{
			Dir:      stateDir,
			Hostname: "aperture-cli-" + bridge.ID,
			UserLogf: userLogf,
		}
		if debug {
			s.Logf = debugLogf
		}
		return &tsnetNode{server: s}
	}
	return m
}

// Activate starts or reuses a bridge reverse proxy for remoteURL and returns
// the localhost URL clients should use.
func (m *Manager) Activate(ctx context.Context, bridge config.Bridge, remoteURL string, logf func(string)) (string, error) {
	if m == nil {
		return "", fmt.Errorf("bridge manager is not configured")
	}
	if err := validateBridgeID(bridge.ID); err != nil {
		return "", err
	}
	if logf == nil {
		logf = func(string) {}
	}
	target, err := parseTarget(remoteURL)
	if err != nil {
		return "", err
	}

	m.mu.Lock()
	rt := m.nodes[bridge.ID]
	needUp := false
	if rt == nil {
		stateDir, err := config.BridgeStateDir(bridge.ID)
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
		node := m.newNode(bridge, stateDir, userLogf, debugLogf)
		rt = &nodeRuntime{
			node:    node,
			proxies: make(map[string]*proxyRuntime),
		}
		m.nodes[bridge.ID] = rt
		needUp = true
	}
	m.mu.Unlock()

	var status *ipnstate.Status
	if needUp {
		logf("Starting bridge " + bridge.Name + " (" + bridge.ID + ")")
		var err error
		status, err = rt.node.Up(ctx)
		if err != nil {
			m.mu.Lock()
			if m.nodes[bridge.ID] == rt {
				delete(m.nodes, bridge.ID)
			}
			m.mu.Unlock()
			return "", errors.Join(err, rt.node.Close())
		}
		logf("Bridge connected.")
	}
	if m.debug {
		// Up deliberately returns status without peers. Ask the in-process
		// LocalAPI for full status so debug output can distinguish a DNS
		// problem from a target that is absent from this node's netmap. Do
		// this on reuse too, since the selected endpoint might have changed.
		if fullStatus, err := rt.node.Status(ctx); err != nil {
			logf("Could not read bridge network status: " + err.Error())
		} else {
			status = fullStatus
		}
		logBridgeStatus(logf, status, target)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.nodes[bridge.ID] != rt {
		return "", fmt.Errorf("bridge stopped before activation completed")
	}
	key := target.String()
	if proxy := rt.proxies[key]; proxy != nil {
		return proxy.localURL, nil
	}

	proxy, err := startProxy(rt.node, target, logf, m.debug)
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

// validateBridgeID rejects IDs that don't match the system-generated
// "bridge-<hex>" format, so a hand-edited config can't inject arbitrary
// content into the tailnet hostname.
func validateBridgeID(id string) error {
	suffix, ok := strings.CutPrefix(id, "bridge-")
	if !ok || suffix == "" {
		return fmt.Errorf("invalid bridge ID %q", id)
	}
	if len(suffix) > 64 {
		return fmt.Errorf("invalid bridge ID %q", id)
	}
	for _, r := range suffix {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return fmt.Errorf("invalid bridge ID %q", id)
		}
	}
	return nil
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

func startProxy(node tailnetNode, target *url.URL, logf func(string), debug bool) (*proxyRuntime, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		start := time.Now()
		if debug {
			logf(fmt.Sprintf("Bridge dialing network=%s address=%s", network, address))
		}
		conn, err := node.DialContext(ctx, network, address)
		elapsed := time.Since(start).Round(time.Millisecond)
		if err != nil {
			logf(fmt.Sprintf("Bridge dial failed: network=%s address=%s elapsed=%s error=%T: %v", network, address, elapsed, err, err))
			return nil, err
		}
		if debug {
			logf(fmt.Sprintf("Bridge dial connected: address=%s remote=%s elapsed=%s", address, conn.RemoteAddr(), elapsed))
		}
		return conn, nil
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	director := proxy.Director
	proxy.Director = func(req *http.Request) {
		director(req)
		req.Host = target.Host
	}
	proxy.Transport = transport
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		logf(fmt.Sprintf("Bridge proxy error: target=%s path=%s error=%T: %v", target.Redacted(), r.URL.Path, err, err))
		http.Error(w, "bridge proxy error: "+err.Error(), http.StatusBadGateway)
	}

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

func logBridgeStatus(logf func(string), status *ipnstate.Status, target *url.URL) {
	if status == nil {
		logf("Bridge network status is unavailable.")
		return
	}

	var tailnetName, dnsSuffix string
	var magicDNS bool
	if status.CurrentTailnet != nil {
		tailnetName = status.CurrentTailnet.Name
		dnsSuffix = status.CurrentTailnet.MagicDNSSuffix
		magicDNS = status.CurrentTailnet.MagicDNSEnabled
	}
	var selfDNS string
	if status.Self != nil {
		selfDNS = status.Self.DNSName
	}
	logf(fmt.Sprintf(
		"Bridge network: state=%s tailnet=%q dns_suffix=%q magic_dns=%t self=%q ips=%v peers=%d",
		status.BackendState, tailnetName, dnsSuffix, magicDNS, selfDNS, status.TailscaleIPs, len(status.Peer),
	))
	if len(status.Health) > 0 {
		logf("Bridge health: " + strings.Join(status.Health, "; "))
	}

	host := strings.ToLower(strings.TrimSuffix(target.Hostname(), "."))
	expectedFQDN := host
	if !strings.Contains(host, ".") && dnsSuffix != "" {
		expectedFQDN += "." + strings.ToLower(strings.TrimSuffix(dnsSuffix, "."))
	}
	for _, peer := range status.Peer {
		peerDNS := strings.ToLower(strings.TrimSuffix(peer.DNSName, "."))
		if peerDNS == host || peerDNS == expectedFQDN {
			logf(fmt.Sprintf("Bridge target is visible: requested=%q peer=%q ips=%v", host, peer.DNSName, peer.TailscaleIPs))
			return
		}
	}
	logf(fmt.Sprintf(
		"Bridge target is not present among visible peers: requested=%q expected_fqdn=%q peers=%d; check the selected tailnet and grants/ACLs",
		host, expectedFQDN, len(status.Peer),
	))
}
