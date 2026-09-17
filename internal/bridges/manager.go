// Package bridges runs embedded tsnet reverse proxies for Aperture endpoints.
package bridges

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/tailscale/aperture-cli/internal/config"
	"tailscale.com/client/local"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tsnet"
)

// Manager owns active tsnet nodes and localhost reverse proxies.
type Manager struct {
	mu sync.Mutex

	debug bool
	// peerWait bounds how long a dial waits for the target to appear in the
	// node's peer map before giving up and resolving it the way tsnet would.
	peerWait         time.Duration
	peerWaitInterval time.Duration
	nodes            map[string]*nodeRuntime
	// tailnets is the network each running node logged in to, keyed by bridge
	// ID. Read back by the TUI to label a bridge with the tailnet it reaches.
	tailnets map[string]string

	newNode func(bridge config.Bridge, stateDir string, userLogf, debugLogf func(string, ...any)) tailnetNode
}

const (
	bridgePeerWaitWindow   = 5 * time.Second
	bridgePeerWaitInterval = 250 * time.Millisecond
)

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
	WatchLogin(context.Context, func(string))
	Logout(context.Context) error
	Close() error
}

// AuthLogPrefix labels the login link in a bridge's activation log. Callers
// parse it back out of the log stream to open a browser, so the text is part
// of this package's API rather than a message that can be reworded freely.
const AuthLogPrefix = "Authorize this bridge in your browser: "

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

// WatchLogin logs what an interactive login is waiting on, until ctx is done.
//
// tsnet surfaces the login link from a five second poll loop of its own, so a
// link that lands just after a tick stays invisible for most of that window.
// A bridge that took sixteen seconds to register showed the user nothing but
// "NeedsLogin" and got killed a few hundred milliseconds before the link would
// have been printed. The IPN bus has the link the moment the control plane
// answers, so watch that instead of waiting for tsnet to notice.
func (n *tsnetNode) WatchLogin(ctx context.Context, logf func(string)) {
	// A cancelled watch is how this returns on every connection that works,
	// so only a failure the caller did not ask for is worth a line.
	report := func(err error) {
		if err != nil && ctx.Err() == nil {
			logf("Could not watch the bridge's login state: " + err.Error())
		}
	}

	// LocalClient calls Start, so this blocks until the node is initialized,
	// the same bring-up Up is waiting on in parallel.
	lc, err := n.server.LocalClient()
	if err != nil {
		report(err)
		return
	}
	watcher, err := lc.WatchIPNBus(ctx, ipn.NotifyInitialState)
	if err != nil {
		report(err)
		return
	}
	defer watcher.Close()
	report(reportLogin(watcher, logf))
}

// reportLogin logs login progress from an IPN bus watch until it ends.
func reportLogin(watcher *local.IPNBusWatcher, logf func(string)) error {
	announced := false
	for {
		notify, err := watcher.Next()
		if err != nil {
			return err
		}
		if notify.State != nil && *notify.State == ipn.NeedsLogin && !announced {
			// Otherwise the wait for the control plane to answer is silent,
			// and the only thing on screen is tsnet's "NeedsLogin".
			announced = true
			logf("This bridge is not logged in to a tailnet yet. Waiting for a login link ...")
		}
		if notify.BrowseToURL != nil {
			logf(AuthLogPrefix + *notify.BrowseToURL)
		}
	}
}

// Logout drops the node's tailnet credentials. The node must be running: the
// login state lives behind its in-process LocalAPI, so logging out is how the
// node leaves the tailnet it is on rather than reusing it on the next start.
func (n *tsnetNode) Logout(ctx context.Context) error {
	lc, err := n.server.LocalClient()
	if err != nil {
		return err
	}
	return lc.Logout(ctx)
}

func (n *tsnetNode) Close() error {
	return n.server.Close()
}

// NewManager returns a bridge manager. When debug is true, verbose tsnet
// backend logs are also emitted to the supplied activation log sink.
func NewManager(debug bool) *Manager {
	m := &Manager{
		debug:            debug,
		peerWait:         bridgePeerWaitWindow,
		peerWaitInterval: bridgePeerWaitInterval,
		nodes:            make(map[string]*nodeRuntime),
		tailnets:         make(map[string]string),
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

	rt, status, err := m.runningNode(ctx, bridge, logf)
	if err != nil {
		return "", err
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

	proxy, err := m.startProxy(rt.node, target, logf)
	if err != nil {
		return "", err
	}
	rt.proxies[key] = proxy
	logf("Listening on " + proxy.localURL)
	return proxy.localURL, nil
}

// runningNode returns the bridge's node, starting it if this is the first use.
// status is the login status Up reported, and is nil for a node that was
// already running. Callers hold no lock.
func (m *Manager) runningNode(ctx context.Context, bridge config.Bridge, logf func(string)) (*nodeRuntime, *ipnstate.Status, error) {
	m.mu.Lock()
	rt := m.nodes[bridge.ID]
	if rt != nil {
		m.mu.Unlock()
		return rt, nil, nil
	}
	stateDir, err := config.BridgeStateDir(bridge.ID)
	if err != nil {
		m.mu.Unlock()
		return nil, nil, err
	}
	userLogf := func(format string, args ...any) {
		logf(fmt.Sprintf(format, args...))
	}
	debugLogf := func(format string, args ...any) {
		if m.debug {
			logf(fmt.Sprintf(format, args...))
		}
	}
	rt = &nodeRuntime{
		node:    m.newNode(bridge, stateDir, userLogf, debugLogf),
		proxies: make(map[string]*proxyRuntime),
	}
	m.nodes[bridge.ID] = rt
	m.mu.Unlock()

	logf("Starting bridge " + bridge.Name + " (" + bridge.ID + ")")

	// Up blocks until the node is Running, which for a bridge that has never
	// logged in means blocking until the user visits a link nothing has shown
	// them yet. The watch runs alongside it and ends with it.
	watchCtx, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	go rt.node.WatchLogin(watchCtx, logf)

	status, err := rt.node.Up(ctx)
	if err != nil {
		m.mu.Lock()
		if m.nodes[bridge.ID] == rt {
			delete(m.nodes, bridge.ID)
		}
		m.mu.Unlock()
		return nil, nil, errors.Join(err, rt.node.Close())
	}
	logf("Bridge connected.")

	// Up returns the login status, so the tailnet this bridge reaches costs no
	// extra call. The connection picker names it on rows the user has not
	// connected to yet.
	if status != nil && status.CurrentTailnet != nil && status.CurrentTailnet.Name != "" {
		m.mu.Lock()
		if m.tailnets == nil {
			m.tailnets = make(map[string]string)
		}
		m.tailnets[bridge.ID] = status.CurrentTailnet.Name
		m.mu.Unlock()
	}
	return rt, status, nil
}

// Tailnet returns the network the bridge's node logged in to during this
// session, or "" when it has not been started or reported one.
func (m *Manager) Tailnet(bridgeID string) string {
	if m == nil {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tailnets[bridgeID]
}

// SwitchTailnet logs the bridge out of the tailnet it is on and discards its
// node, so the next Activate starts a fresh one and asks for a new login.
//
// The node has to be running to be logged out: its credentials live behind the
// in-process LocalAPI, and closing the node without logging out would reuse
// them on the next start. A node that was never started this session is
// therefore brought up on the old tailnet first, which is also what leaves the
// device removed from it rather than orphaned.
func (m *Manager) SwitchTailnet(ctx context.Context, bridge config.Bridge, logf func(string)) error {
	if m == nil {
		return fmt.Errorf("bridge manager is not configured")
	}
	if err := validateBridgeID(bridge.ID); err != nil {
		return err
	}
	if logf == nil {
		logf = func(string) {}
	}
	rt, _, err := m.runningNode(ctx, bridge, logf)
	if err != nil {
		return err
	}

	logf("Logging bridge " + bridge.Name + " out of its tailnet ...")
	logoutErr := rt.node.Logout(ctx)

	// Under the lock, as in Close: an Activate that took rt before the delete
	// may still be adding a proxy to it.
	m.mu.Lock()
	if m.nodes[bridge.ID] == rt {
		delete(m.nodes, bridge.ID)
	}
	delete(m.tailnets, bridge.ID)
	errs := []error{logoutErr}
	for key, proxy := range rt.proxies {
		errs = append(errs, closeProxy(proxy))
		delete(rt.proxies, key)
	}
	errs = append(errs, rt.node.Close())
	m.mu.Unlock()

	if err := errors.Join(errs...); err != nil {
		return err
	}
	logf("Bridge logged out. Log in to the tailnet you want next.")
	return nil
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
			errs = append(errs, closeProxy(proxy))
			delete(rt.proxies, key)
		}
		if err := rt.node.Close(); err != nil {
			errs = append(errs, err)
		}
		delete(m.nodes, id)
		delete(m.tailnets, id)
	}
	return errors.Join(errs...)
}

// closeProxy shuts down one localhost reverse proxy. An already-closed server
// or listener is not a failure: Close and SwitchTailnet can both reach the
// same proxy.
func closeProxy(proxy *proxyRuntime) error {
	var errs []error
	if err := proxy.server.Close(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		errs = append(errs, err)
	}
	if err := proxy.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		errs = append(errs, err)
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

func (m *Manager) startProxy(node tailnetNode, target *url.URL, logf func(string)) (*proxyRuntime, error) {
	debug := m.debug
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
		conn, attempts, err := dialViaNode(
			ctx,
			node,
			network,
			address,
			logf,
			m.peerWait,
			m.peerWaitInterval,
		)
		elapsed := time.Since(start).Round(time.Millisecond)
		if err != nil {
			logf(fmt.Sprintf("Bridge dial failed: network=%s address=%s attempts=%d elapsed=%s error=%T: %v", network, address, attempts, elapsed, err, err))
			return nil, err
		}
		if debug {
			logf(fmt.Sprintf("Bridge dial connected: address=%s remote=%s attempts=%d elapsed=%s", address, conn.RemoteAddr(), attempts, elapsed))
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

type bridgeDialFunc func(context.Context, string, string) (net.Conn, error)

// dialViaNode dials address over the bridge's node, resolving a name against
// the node's own peer map first and dialing the IP it finds.
//
// Handing the name straight to tsnet is what made a first connection hang for
// 30s: until the node's netmap lands, tsnet's resolver falls through to the
// host resolver, and on a machine that is itself on a tailnet that answers
// with a same-named node on the *host's* tailnet. tsnet then sees an address
// it has no route for and system-dials it, so the bridge either blackholes
// until the fetch times out or, worse, proxies to the wrong tailnet's node.
// Resolving through the node cannot leave the bridge's tailnet, and waiting
// for the peer to appear is the same wait the old DNS retry was aiming at.
func dialViaNode(
	ctx context.Context,
	node tailnetNode,
	network, address string,
	logf func(string),
	peerWaitWindow, peerWaitInterval time.Duration,
) (net.Conn, int, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, 0, err
	}
	if _, err := netip.ParseAddr(host); err == nil {
		conn, err := node.DialContext(ctx, network, address)
		return conn, 1, err
	}

	ip, attempts, err := waitForPeerAddr(ctx, node, host, peerWaitWindow, peerWaitInterval)
	if err != nil {
		if ctx.Err() != nil {
			return nil, attempts, err
		}
		// Not every target is a tailnet node: a subnet router or the tailnet's
		// own DNS can serve it. Those only resolve the way tsnet resolves, so
		// fall through and say so, since this is the path that can leave the
		// tailnet.
		logf(fmt.Sprintf("Bridge target %s is not a node on this bridge's tailnet (%v); resolving it the usual way.", host, err))
		conn, derr := node.DialContext(ctx, network, address)
		return conn, attempts, derr
	}

	conn, err := node.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
	return conn, attempts, err
}

// waitForPeerAddr polls the node's status until host shows up as a peer. A node
// that just came up reports Running before its peer map arrives, so the first
// look usually misses.
func waitForPeerAddr(
	ctx context.Context,
	node tailnetNode,
	host string,
	window, interval time.Duration,
) (netip.Addr, int, error) {
	deadline := time.Now().Add(window)
	attempts := 0
	for {
		status, err := node.Status(ctx)
		attempts++
		if err == nil {
			if ip, ok := peerAddr(status, host); ok {
				return ip, attempts, nil
			}
			err = errors.New("not in this node's peer map")
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return netip.Addr{}, attempts, ctxErr
		}

		remaining := time.Until(deadline)
		if remaining <= 0 || interval <= 0 {
			return netip.Addr{}, attempts, err
		}
		if interval > remaining {
			interval = remaining
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return netip.Addr{}, attempts, ctx.Err()
		case <-timer.C:
		}
	}
}

// peerAddr returns the tailnet address the node has for host, matching either a
// peer's full MagicDNS name or its first label, the short form endpoint URLs
// usually carry.
func peerAddr(status *ipnstate.Status, host string) (netip.Addr, bool) {
	if status == nil {
		return netip.Addr{}, false
	}
	want := strings.ToLower(strings.TrimSuffix(host, "."))
	for _, peer := range status.Peer {
		if peer == nil || !magicDNSNameMatches(peer.DNSName, want) {
			continue
		}
		if ip, ok := preferIPv4(peer.TailscaleIPs); ok {
			return ip, true
		}
	}
	return netip.Addr{}, false
}

func magicDNSNameMatches(dnsName, host string) bool {
	name := strings.ToLower(strings.TrimSuffix(dnsName, "."))
	if name == "" {
		return false
	}
	if name == host {
		return true
	}
	label, _, _ := strings.Cut(name, ".")
	return label == host
}

func preferIPv4(addrs []netip.Addr) (netip.Addr, bool) {
	var fallback netip.Addr
	for _, addr := range addrs {
		if addr.Is4() {
			return addr, true
		}
		if !fallback.IsValid() {
			fallback = addr
		}
	}
	return fallback, fallback.IsValid()
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
