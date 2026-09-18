// Package bridges runs embedded tsnet reverse proxies for Aperture endpoints.
package bridges

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/tailscale/aperture-cli/internal/config"
	"github.com/tailscale/aperture-cli/internal/connection"
	"tailscale.com/client/local"
	"tailscale.com/health"
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
	nodes            map[string]*Machine
	// tailnets is the network each running node logged in to, keyed by bridge
	// ID. Read back by the TUI to label a bridge with the tailnet it reaches.
	tailnets map[string]string
	shutdown func() error

	newNode func(bridge config.Bridge, stateDir string, userLogf, debugLogf func(string, ...any)) tailnetNode
}

const (
	bridgePeerWaitWindow   = 5 * time.Second
	bridgePeerWaitInterval = 250 * time.Millisecond
)

// liveEvents points a node's long-lived reporting at whichever connection is
// using it now. Nodes and proxies outlive the connection that built them, and
// closures that captured that connection's sink went on writing to a channel
// nobody read, losing every later dial failure and proxy error.
//
// Nothing clears it when a connection ends: a finished sink discards what it is
// given, and a clear needs a lifecycle hook only the Attempt can own.
type liveEvents struct {
	mu sync.Mutex
	ev events
}

func (l *liveEvents) use(ev events) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ev = ev
}

// emit has the events signature, so callers keep note and notef.
func (l *liveEvents) emit(e connection.Event) {
	l.mu.Lock()
	ev := l.ev
	l.mu.Unlock()
	if ev != nil {
		ev(e)
	}
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
	WatchLogin(context.Context, events)
	Logout(context.Context) error
	Close() error
}

// events is where a bridge reports what it is doing. This package translates
// the tailnet's vocabulary into it and publishes nothing else, so no caller has
// to recover meaning by matching prose from inside a vendored package.
type events func(connection.Event)

// sink returns a usable events, so callers that want none can pass nil.
func sink(emit func(connection.Event)) events {
	return func(e connection.Event) {
		logEvent(e)
		if emit != nil {
			emit(e)
		}
	}
}

// logEvent copies a connection event into the run log. The connect screen dies
// with the process, and the run anyone wants to read back is the one that was
// killed halfway through. Notes are debug: under -debug they carry tsnet's
// backend logger, and a phase is worth reading without wading through that.
func logEvent(e connection.Event) {
	switch e.Kind {
	case connection.PhaseEntered:
		slog.Info("bridge phase", "phase", e.Phase)
	case connection.LoginRequired:
		slog.Info("bridge needs login")
	default:
		slog.Debug("bridge note", "text", redactDiagnostic(e.Text))
	}
}

// Backend diagnostics can repeat authorization capabilities. Keep the link in
// the interactive event only; even debug logs are routinely shared for support.
var diagnosticURL = regexp.MustCompile(`(?i)https?://\S+`)

func redactDiagnostic(text string) string {
	return diagnosticURL.ReplaceAllString(text, "[redacted URL]")
}

func (e events) note(text string)                 { e(connection.Note(text)) }
func (e events) notef(format string, args ...any) { e(connection.Notef(format, args...)) }
func (e events) enter(p connection.Phase)         { e(connection.Entered(p)) }
func (e events) login(link connection.LoginLink)  { e(connection.Login(link)) }

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

// WatchLogin reports what an interactive login is waiting on, until ctx is done.
//
// tsnet surfaces the link from a five second poll loop of its own, so a link
// landing just after a tick stays invisible for most of that window: one bridge
// was killed a few hundred milliseconds before its link would have printed. The
// IPN bus has it the moment the control plane answers.
func (n *tsnetNode) WatchLogin(ctx context.Context, ev events) {
	// A cancelled watch is how this returns on every connection that works,
	// so only a failure the caller did not ask for is worth a line.
	report := func(err error) {
		if err != nil && ctx.Err() == nil {
			// Logged as well as noted: a dead watch leaves the attempt on its
			// last phase forever, which on screen is indistinguishable from a
			// control plane that is simply slow.
			slog.Error("bridge login watch ended", "err", redactDiagnostic(err.Error()))
			ev.note("Could not watch the bridge's login state: " + err.Error())
		}
	}

	// LocalClient calls Start, so this blocks until the node is initialized,
	// the same bring-up Up is waiting on in parallel.
	lc, err := n.server.LocalClient()
	if err != nil {
		report(err)
		return
	}
	// InitialHealthState too: health changes reach every watcher regardless of
	// mask, but a login already broken before this watch started shows up only
	// in the initial one, which is the reused node case.
	watcher, err := lc.WatchIPNBus(ctx, ipn.NotifyInitialState|ipn.NotifyInitialHealthState)
	if err != nil {
		report(err)
		return
	}
	defer watcher.Close()
	report(reportLogin(watcher, ev))
}

// reportLogin translates an IPN bus watch into phases until the watch ends.
func reportLogin(watcher *local.IPNBusWatcher, ev events) error {
	reporter := loginReporter{ev: ev}
	for {
		notify, err := watcher.Next()
		if err != nil {
			return err
		}
		reporter.notify(&notify)
	}
}

// loginReporter turns IPN bus notifications into the phases a connection
// attempt reports, holding the last one because the bus repeats states.
//
// ipn.NeedsLogin covers two waits that look identical and are not: before a
// BrowseToURL the control plane has not answered and there is nothing to do,
// after it everything is waiting on the user. Reporting the backend state made
// a 29 second registration indistinguishable from someone who wandered off.
type loginReporter struct {
	ev    events
	phase connection.Phase
	// loginBroken is whether the login-state warning is up. Health state is
	// re-sent on every retry with a fresh request ID in the text, so reporting
	// on the text would add a line a second for as long as the failure lasts.
	loginBroken bool
}

func (r *loginReporter) enter(p connection.Phase) {
	// A re-notified NeedsLogin after the link is already on screen would walk
	// the attempt backwards through a wait the user has already left.
	if p <= r.phase {
		return
	}
	r.phase = p
	r.ev.enter(p)
}

func (r *loginReporter) notify(n *ipn.Notify) {
	if n == nil {
		return
	}
	if n.State != nil {
		// The raw state, not just the phase: NoState and NeedsLogin are one
		// phase on screen on purpose and the whole question in a log. NoState
		// means control has not answered the register yet.
		slog.Info("bridge ipn state", "state", n.State.String())
		switch *n.State {
		case ipn.NoState, ipn.NeedsLogin:
			// Both, and NoState is the one that matters: a bridge that never
			// logged in sits there for the whole of POST /machine/register, so
			// it is the wait and not a not-started-yet. Tailscale's own comment
			// reads "UIs should print Loading..." (ipnlocal/local.go).
			r.enter(connection.AwaitingLoginLink)
		case ipn.NeedsMachineAuth:
			// No phase of its own: we have never seen it, and inventing a wait
			// we cannot observe is worse than a line that says what to go and
			// do. Promote it if this turns out to be common.
			r.ev.note("This bridge is waiting to be approved in the tailnet's admin console.")
		case ipn.Starting:
			r.enter(connection.JoiningTailnet)
		case ipn.Running:
			r.enter(connection.FindingEndpoint)
		}
	}
	if n.BrowseToURL != nil {
		link, err := connection.ParseLoginLink(*n.BrowseToURL)
		if err != nil {
			// Record the rejection reason, never the authorization capability.
			slog.Error("unusable login link from the control plane", "err", err)
			// Not fatal to the login: tsnet keeps printing its own copy, and
			// the user can still finish by hand. Worth saying, because the
			// browser is not going to open.
			r.ev.note("Ignoring an unusable login link from the control plane: " + err.Error())
			return
		}
		r.enter(connection.AwaitingAuthorization)
		r.ev.login(link)
	}
	r.health(n.Health)
}

// health reports a login that is failing rather than merely slow. A register
// answered with a 502 leaves the node in NeedsLogin sending no BrowseToURL, so
// the attempt sits on "Waiting for a login link" while tsnet retries behind a
// backoff; the error is not a vizerror, so it never reaches Notify.ErrMessage.
//
// login-state only. The other warnables describe a node that is up and
// imperfect, and would bury the one line that is this attempt's business.
func (r *loginReporter) health(state *health.State) {
	if state == nil {
		return
	}
	warning, broken := state.Warnings[health.LoginStateWarnable.Code]
	if broken == r.loginBroken {
		return
	}
	r.loginBroken = broken
	if !broken {
		slog.Info("bridge login recovered")
		return
	}
	slog.Error("bridge login is failing", "text", redactDiagnostic(warning.Text))
	r.ev.note("The tailnet will not log this bridge in: " + warning.Text)
}

// Logout initializes the LocalAPI, but does not wait for authorization. A
// bridge whose old identity cannot log in must still be able to leave it.
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
		nodes:            make(map[string]*Machine),
		tailnets:         make(map[string]string),
	}
	m.newNode = func(bridge config.Bridge, stateDir string, userLogf, debugLogf func(string, ...any)) tailnetNode {
		s := &tsnet.Server{
			Dir:      stateDir,
			Hostname: MachineName(bridge.ID),
			UserLogf: userLogf,
		}
		if debug {
			s.Logf = debugLogf
		}
		return &tsnetNode{server: s}
	}
	m.shutdown = sync.OnceValue(m.close)
	return m
}

// Activate starts or reuses a bridge reverse proxy for remoteURL and returns
// the localhost URL clients should use.
func (m *Manager) Activate(ctx context.Context, bridge config.Bridge, remoteURL string, emit func(connection.Event)) (string, error) {
	if m == nil {
		return "", fmt.Errorf("bridge manager is not configured")
	}
	if err := validateBridgeID(bridge.ID); err != nil {
		return "", err
	}
	ev := sink(emit)
	target, err := parseTarget(remoteURL)
	if err != nil {
		return "", err
	}

	ctx, rt, err := m.acquire(ctx, bridge.ID)
	if err != nil {
		return "", err
	}
	defer m.release(rt)
	status, err := m.runningNode(ctx, bridge, rt, ev)
	if err != nil {
		return "", err
	}
	// Here rather than in runningNode, which returns immediately for a node
	// already up: a reused bridge would otherwise report nothing while the
	// first dial waits for the target to appear in its peer map.
	ev.enter(connection.FindingEndpoint)
	if m.debug {
		// Up deliberately returns status without peers. Full status lets debug
		// output tell a DNS problem from a target absent from this node's
		// netmap; on reuse too, since the endpoint may have changed.
		if fullStatus, err := rt.node.Status(ctx); err != nil {
			ev.note("Could not read bridge network status: " + err.Error())
		} else {
			status = fullStatus
		}
		logBridgeStatus(ev, status, target)
	}

	if err := ctx.Err(); err != nil {
		return "", err
	}
	key := target.String()
	if proxy := rt.proxies[key]; proxy != nil {
		return proxy.localURL, nil
	}

	proxy, err := m.startProxy(rt, target)
	if err != nil {
		return "", err
	}
	rt.proxies[key] = proxy
	ev.note("Listening on " + proxy.localURL)
	return proxy.localURL, nil
}

// initNode constructs a node without waiting for login. The Machine's turn is
// held by the caller; only Activate follows initialization with Up.
func (m *Manager) initNode(bridge config.Bridge, rt *Machine, ev events) error {
	rt.ev.use(ev)
	if rt.node != nil {
		return nil
	}
	stateDir, err := config.BridgeStateDir(bridge.ID)
	if err != nil {
		return err
	}
	// Both of tsnet's loggers are diagnostics now: everything the attempt waits
	// on comes off the IPN bus, and UserLogf is mostly printAuthURLLoop
	// reprinting a link the footer already shows. A no-op rather than nil,
	// because tsnet falls back to log.Printf, which writes over the TUI.
	logNotes := func(format string, args ...any) {
		if m.debug {
			events(rt.ev.emit).notef(format, args...)
		}
	}
	userLogf, debugLogf := logNotes, logNotes
	rt.node = m.newNode(bridge, stateDir, userLogf, debugLogf)
	if rt.node == nil {
		return fmt.Errorf("bridge node is not configured")
	}
	return nil
}

// runningNode waits for an uncached node to become usable while holding its
// Machine's turn. A failed Up finishes cleanup before another attempt enters.
func (m *Manager) runningNode(ctx context.Context, bridge config.Bridge, rt *Machine, ev events) (*ipnstate.Status, error) {
	if rt.node != nil {
		rt.ev.use(ev)
		return nil, nil
	}
	if err := m.initNode(bridge, rt, ev); err != nil {
		return nil, err
	}

	ev.enter(connection.StartingMachine)

	// Up blocks until the node is Running, which for a bridge that has never
	// logged in means blocking until the user visits a link nothing has shown
	// them yet. The watch runs alongside it and ends with it.
	watchCtx, stopWatch := context.WithCancel(ctx)
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		rt.node.WatchLogin(watchCtx, ev)
	}()

	// Timed because this is the wait every "it just sat there" report is
	// about, and the number is the difference between a slow control plane and
	// a login link the user never saw.
	start := time.Now()
	status, err := rt.node.Up(ctx)
	stopWatch()
	<-watchDone
	if err != nil {
		slog.Error("bridge node did not come up", "bridge", bridge.ID, "after", time.Since(start), "err", redactDiagnostic(err.Error()))
		return nil, errors.Join(err, rt.close())
	}
	slog.Info("bridge node up", "bridge", bridge.ID, "after", time.Since(start))

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
	return status, nil
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
// node, so the next Activate asks for a new login.
//
// Logout only needs an initialized LocalAPI. Waiting for Running first would
// demand authorization of an expired or unapproved identity just to leave it.
func (m *Manager) SwitchTailnet(ctx context.Context, bridge config.Bridge, emit func(connection.Event)) error {
	if m == nil {
		return fmt.Errorf("bridge manager is not configured")
	}
	if err := validateBridgeID(bridge.ID); err != nil {
		return err
	}
	ev := sink(emit)
	ctx, rt, err := m.acquire(ctx, bridge.ID)
	if err != nil {
		return err
	}
	defer m.release(rt)
	if err := m.initNode(bridge, rt, ev); err != nil {
		return err
	}

	ev.note("Logging bridge " + bridge.Name + " out of its tailnet ...")
	logoutErr := rt.node.Logout(ctx)

	closeErr := rt.close()
	m.forget(bridge.ID)

	if err := errors.Join(logoutErr, closeErr); err != nil {
		return err
	}
	ev.note("Bridge logged out. Log in to the tailnet you want next.")
	return nil
}

// Close shuts down all active reverse proxies and tsnet nodes. Concurrent and
// subsequent callers wait for the same cleanup and receive the same result.
func (m *Manager) Close() error {
	if m == nil || m.shutdown == nil {
		return nil
	}
	return m.shutdown()
}

func (m *Manager) close() error {
	m.mu.Lock()
	nodes := m.nodes
	m.nodes = nil
	for _, rt := range nodes {
		if rt.cancel != nil {
			rt.cancel()
		}
	}
	m.mu.Unlock()

	var errs []error
	for _, rt := range nodes {
		rt.turn <- struct{}{}
		errs = append(errs, rt.close())
		<-rt.turn
	}
	m.mu.Lock()
	clear(m.tailnets)
	m.mu.Unlock()
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

// startProxy builds the reverse proxy for one target on rt's node. It reports
// through rt because the proxy is cached and will still be serving long after
// the connection that asked for it has gone.
func (m *Manager) startProxy(rt *Machine, target *url.URL) (*proxyRuntime, error) {
	node, ev := rt.node, events(rt.ev.emit)
	debug := m.debug
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		start := time.Now()
		if debug {
			ev.notef("Bridge dialing network=%s address=%s", network, address)
		}
		conn, attempts, err := dialViaNode(
			ctx,
			node,
			network,
			address,
			ev,
			m.peerWait,
			m.peerWaitInterval,
		)
		elapsed := time.Since(start).Round(time.Millisecond)
		if err != nil {
			ev.notef("Bridge dial failed: network=%s address=%s attempts=%d elapsed=%s error=%T: %v", network, address, attempts, elapsed, err, err)
			return nil, err
		}
		if debug {
			ev.notef("Bridge dial connected: address=%s remote=%s attempts=%d elapsed=%s", address, conn.RemoteAddr(), attempts, elapsed)
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
		ev.notef("Bridge proxy error: target=%s path=%s error=%T: %v", target.Redacted(), r.URL.Path, err, err)
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
// Handing the name to tsnet is what made a first connection hang for 30s: until
// the netmap lands its resolver falls through to the host resolver, which on a
// machine already on a tailnet answers with a same-named node on the wrong one.
// Short aliases use this node's current tailnet suffix; a shared peer requires
// its full name.
func dialViaNode(
	ctx context.Context,
	node tailnetNode,
	network, address string,
	ev events,
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
		// own DNS can serve it. Those resolve only the way tsnet resolves, so
		// fall through and say so, since this path can leave the tailnet.
		ev.notef("Bridge target %s is not a node on this bridge's tailnet (%v); resolving it the usual way.", host, err)
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

// peerAddr resolves short names only within the current tailnet's MagicDNS
// suffix. A shared-in peer can have the same first label but belongs to another
// tailnet; reaching it requires its explicit full name.
func peerAddr(status *ipnstate.Status, host string) (netip.Addr, bool) {
	if status == nil {
		return netip.Addr{}, false
	}
	want := strings.ToLower(strings.TrimSuffix(host, "."))
	if !strings.Contains(want, ".") {
		if status.CurrentTailnet == nil {
			return netip.Addr{}, false
		}
		suffix := strings.ToLower(strings.TrimSuffix(status.CurrentTailnet.MagicDNSSuffix, "."))
		if suffix == "" || want == "" {
			return netip.Addr{}, false
		}
		want += "." + suffix
	}
	for _, peer := range status.Peer {
		if peer == nil || strings.ToLower(strings.TrimSuffix(peer.DNSName, ".")) != want {
			continue
		}
		if ip, ok := preferIPv4(peer.TailscaleIPs); ok {
			return ip, true
		}
	}
	return netip.Addr{}, false
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

func logBridgeStatus(ev events, status *ipnstate.Status, target *url.URL) {
	if status == nil {
		ev.note("Bridge network status is unavailable.")
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
	ev.notef(
		"Bridge network: state=%s tailnet=%q dns_suffix=%q magic_dns=%t self=%q ips=%v peers=%d",
		status.BackendState, tailnetName, dnsSuffix, magicDNS, selfDNS, status.TailscaleIPs, len(status.Peer),
	)
	if len(status.Health) > 0 {
		ev.note("Bridge health: " + strings.Join(status.Health, "; "))
	}

	host := strings.ToLower(strings.TrimSuffix(target.Hostname(), "."))
	expectedFQDN := host
	if !strings.Contains(host, ".") && dnsSuffix != "" {
		expectedFQDN += "." + strings.ToLower(strings.TrimSuffix(dnsSuffix, "."))
	}
	for _, peer := range status.Peer {
		peerDNS := strings.ToLower(strings.TrimSuffix(peer.DNSName, "."))
		if peerDNS == host || peerDNS == expectedFQDN {
			ev.notef("Bridge target is visible: requested=%q peer=%q ips=%v", host, peer.DNSName, peer.TailscaleIPs)
			return
		}
	}
	ev.notef(
		"Bridge target is not present among visible peers: requested=%q expected_fqdn=%q peers=%d; check the selected tailnet and grants/ACLs",
		host, expectedFQDN, len(status.Peer),
	)
}
