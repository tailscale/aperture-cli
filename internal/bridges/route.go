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
	"time"

	"tailscale.com/ipn/ipnstate"
)

// Route reverse-proxies a loopback listener to one Endpoint over one
// Machine's node. LocalURL is the URL a client is told to use. A Route
// belongs to exactly one Machine and closes with it.
type Route struct {
	LocalURL string
	server   *http.Server
	listener net.Listener
}

// close shuts the listener and server. An already closed Route is not a
// failure, because Close and LeaveTailnet can both reach the same Route.
func (r *Route) close() error {
	var errs []error
	if err := r.server.Close(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		errs = append(errs, err)
	}
	if err := r.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		errs = append(errs, err)
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

// openRoute builds the reverse proxy for one target on the Machine's node.
// The proxy reports through the Machine's relay because the Route is cached
// and keeps serving long after the attempt that asked for it has gone. The
// caller holds the Machine's turn.
func (mc *Machine) openRoute(target *url.URL) (*Route, error) {
	node, ev := mc.node, events(mc.ev.emit)
	debug := mc.machines.debug
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
			mc.machines.peerWait,
			mc.machines.peerWaitInterval,
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
		// Clients send a placeholder bearer because their SDKs refuse to
		// build a request without one. Ingress auth at the Aperture is the
		// Machine's tailnet identity, so the header is never load-bearing,
		// and a gateway that treats it as authoritative rejects it.
		req.Header.Del("Authorization")
	}
	proxy.Transport = transport
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		ev.notef("Bridge proxy error: target=%s path=%s error=%T: %v", redactURL(target.String()), r.URL.Path, err, err)
		http.Error(w, "bridge proxy error: "+err.Error(), http.StatusBadGateway)
	}

	srv := &http.Server{Handler: proxy}
	go func() {
		_ = srv.Serve(ln)
	}()

	return &Route{
		LocalURL: "http://" + ln.Addr().String(),
		server:   srv,
		listener: ln,
	}, nil
}

type bridgeDialFunc func(context.Context, string, string) (net.Conn, error)

// dialViaNode dials address over the bridge's node. A hostname is resolved
// against the node's own peer map first and the IP found there is dialed.
//
// Handing the name to tsnet made a first connection hang for 30s. Until the
// netmap lands, tsnet's resolver falls through to the host resolver, and on a
// machine already on a tailnet that answers with a same-named node on the
// wrong one. A short alias gets this node's current tailnet suffix. A shared
// peer needs its full name.
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
		// A bare name is a peer alias and nothing else. Handed to tsnet it
		// would fall through to the host resolver, and on a machine already
		// on another tailnet that answers with that tailnet's node of the
		// same name. The wait above only delayed that.
		if !strings.Contains(host, ".") {
			return nil, attempts, fmt.Errorf("%s is not a node on this bridge's tailnet (%v)", host, err)
		}
		// A qualified name can be a subnet route or the tailnet's own DNS.
		// Only tsnet can resolve those, so fall through to it and say so,
		// because this path can leave the tailnet.
		ev.notef("Bridge target %s is not a node on this bridge's tailnet (%v); resolving it the usual way.", host, err)
		conn, derr := node.DialContext(ctx, network, address)
		return conn, attempts, derr
	}

	conn, err := node.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
	return conn, attempts, err
}

// waitForPeerAddr polls the node's status until host shows up as a peer. A
// node that just came up reports Running before its peer map arrives, so the
// first look usually misses.
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

// peerAddr finds host in the peer map. A short name is qualified with the
// current tailnet's MagicDNS suffix and matched only there. A shared-in peer
// can have the same first label but belongs to another tailnet, so reaching
// it requires its full name.
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
