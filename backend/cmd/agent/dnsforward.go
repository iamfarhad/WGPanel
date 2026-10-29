package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"
)

const (
	dnsTCPIdleTimeout = 10 * time.Second // a TCP DNS exchange is a few small messages
	dnsMaxInFlight    = 256              // bounds goroutines under a query flood
)

// dnsUpstreamTimeout bounds each upstream attempt - below the ~5s clients wait per
// query, so a slow but working upstream still answers. A var so tests can shorten it.
var dnsUpstreamTimeout = 4 * time.Second

// dnsForwarder answers DNS for this node's clients on the node's own tunnel address
// (the subnet's .1, which panel-generated configs point DNS at when client_dns is
// "node") and relays every query, unparsed, to the resolvers this server itself uses
// (its resolv.conf - inside the node container that's Docker's embedded DNS, which in
// turn uses the host's). Clients then resolve exactly where their traffic egresses:
// wherever the server can resolve names, so can they - no public resolver that a
// datacenter or corporate network might block. Bound to the tunnel address only, so
// it's reachable from the tunnel and nowhere else.
type dnsForwarder struct {
	upstreams []string // host:port
	udp       net.PacketConn
	tcp       net.Listener
	logger    *slog.Logger
	inFlight  chan struct{}
}

// startDNSForwarder starts the forwarder on iface's IPv4 address. An error means
// clients can't be pointed at this node for DNS; the agent then reports no forwarder
// and the panel keeps such nodes' clients on its public default.
func startDNSForwarder(iface, resolvConf string, logger *slog.Logger) (*dnsForwarder, error) {
	listenIP, err := interfaceIPv4(iface)
	if err != nil {
		return nil, err
	}
	upstreams, err := resolvConfNameservers(resolvConf)
	if err != nil {
		return nil, err
	}
	// Never relay to ourselves - a resolv.conf naming the tunnel address would loop.
	upstreams = without(upstreams, net.JoinHostPort(listenIP, "53"))
	if len(upstreams) == 0 {
		return nil, fmt.Errorf("no usable nameserver in %s", resolvConf)
	}

	return listenDNSForwarder(net.JoinHostPort(listenIP, "53"), upstreams, logger)
}

// listenDNSForwarder serves UDP and TCP on the same address (TCP takes UDP's port,
// so a ":0" address works too).
func listenDNSForwarder(addr string, upstreams []string, logger *slog.Logger) (*dnsForwarder, error) {
	udp, err := net.ListenPacket("udp", addr)
	if err != nil {
		return nil, err
	}
	tcp, err := net.Listen("tcp", udp.LocalAddr().String())
	if err != nil {
		udp.Close()
		return nil, err
	}

	f := &dnsForwarder{upstreams: upstreams, udp: udp, tcp: tcp, logger: logger, inFlight: make(chan struct{}, dnsMaxInFlight)}
	go f.serveUDP()
	go f.serveTCP()
	logger.Info("dns_forwarder_listening", "addr", udp.LocalAddr().String(), "upstreams", upstreams)
	return f, nil
}

func (f *dnsForwarder) Close() {
	f.udp.Close()
	f.tcp.Close()
}

func (f *dnsForwarder) serveUDP() {
	buf := make([]byte, 65535)
	for {
		n, client, err := f.udp.ReadFrom(buf)
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				f.logger.Warn("dns_forwarder_udp_stopped", "error", err)
			}
			return
		}
		select {
		case f.inFlight <- struct{}{}:
		default:
			continue // saturated: drop, the client retries like with any lossy resolver
		}
		query := append([]byte(nil), buf[:n]...)
		go func() {
			defer func() { <-f.inFlight }()
			if reply, err := f.exchangeUDP(query); err == nil {
				_, _ = f.udp.WriteTo(reply, client)
			}
		}()
	}
}

// exchangeUDP tries each upstream in resolv.conf order until one answers.
func (f *dnsForwarder) exchangeUDP(query []byte) ([]byte, error) {
	var lastErr error
	for _, upstream := range f.upstreams {
		reply, err := exchangeUDPOnce(upstream, query)
		if err == nil {
			return reply, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func exchangeUDPOnce(upstream string, query []byte) ([]byte, error) {
	conn, err := net.DialTimeout("udp", upstream, dnsUpstreamTimeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(dnsUpstreamTimeout)); err != nil {
		return nil, err
	}
	if _, err := conn.Write(query); err != nil {
		return nil, err
	}
	reply := make([]byte, 65535)
	n, err := conn.Read(reply)
	if err != nil {
		return nil, err
	}
	return reply[:n], nil
}

func (f *dnsForwarder) serveTCP() {
	for {
		client, err := f.tcp.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				f.logger.Warn("dns_forwarder_tcp_stopped", "error", err)
			}
			return
		}
		select {
		case f.inFlight <- struct{}{}:
		default:
			client.Close()
			continue
		}
		go func() {
			defer func() { <-f.inFlight }()
			f.relayTCP(client)
		}()
	}
}

// relayTCP pipes one client connection to the first upstream that accepts; DNS over
// TCP is length-prefixed on both sides, so the bytes pass through unchanged.
func (f *dnsForwarder) relayTCP(client net.Conn) {
	defer client.Close()
	var upstream net.Conn
	for _, addr := range f.upstreams {
		conn, err := net.DialTimeout("tcp", addr, dnsUpstreamTimeout)
		if err == nil {
			upstream = conn
			break
		}
	}
	if upstream == nil {
		return
	}
	defer upstream.Close()

	deadline := time.Now().Add(dnsTCPIdleTimeout)
	_ = client.SetDeadline(deadline)
	_ = upstream.SetDeadline(deadline)
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(upstream, client)
		close(done)
	}()
	_, _ = io.Copy(client, upstream)
	<-done
}

// resolvConfNameservers returns the "nameserver" entries of a resolv.conf as
// host:port, in file order.
func resolvConfNameservers(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var servers []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[0] == "nameserver" {
			// Zone-scoped IPv6 ("fe80::1%eth0") is valid in resolv.conf and for Dial.
			host, _, _ := strings.Cut(fields[1], "%")
			if net.ParseIP(host) != nil {
				servers = append(servers, net.JoinHostPort(fields[1], "53"))
			}
		}
	}
	return servers, scanner.Err()
}

func interfaceIPv4(name string) (string, error) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return "", err
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return "", err
	}
	for _, a := range addrs {
		if ipNet, ok := a.(*net.IPNet); ok && ipNet.IP.To4() != nil {
			return ipNet.IP.String(), nil
		}
	}
	return "", fmt.Errorf("interface %s has no IPv4 address", name)
}

func without(list []string, drop string) []string {
	out := list[:0:0]
	for _, v := range list {
		if v != drop {
			out = append(out, v)
		}
	}
	return out
}
