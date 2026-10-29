package main

import (
	"net"
	"reflect"
	"testing"
	"time"
)

// fakeResolver answers every A query with 192.0.2.1 - just enough DNS for Go's
// resolver to accept the reply. With answer=false it swallows queries instead,
// like a resolver blocked on the path.
func fakeResolver(t *testing.T, answer bool) string {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })

	go func() {
		buf := make([]byte, 512)
		for {
			n, from, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			if !answer || n < 12 {
				continue
			}
			// The question runs from byte 12 to the end of its name plus QTYPE/QCLASS.
			end := 12
			for end < n && buf[end] != 0 {
				end += int(buf[end]) + 1
			}
			end += 5
			if end > n {
				continue
			}
			resp := append([]byte{}, buf[:2]...)                    // query ID
			resp = append(resp, 0x81, 0x80, 0, 1, 0, 1, 0, 0, 0, 0) // response, 1 question, 1 answer
			resp = append(resp, buf[12:end]...)
			resp = append(resp, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 192, 0, 2, 1)
			_, _ = conn.WriteTo(resp, from)
		}
	}()
	return conn.LocalAddr().String()
}

func TestProbeDNS(t *testing.T) {
	defer func(old time.Duration) { dnsProbeTimeout = old }(dnsProbeTimeout)
	dnsProbeTimeout = 500 * time.Millisecond

	if err := probeDNS(fakeResolver(t, true)); err != nil {
		t.Fatalf("expected an answering resolver to pass, got %v", err)
	}
	if err := probeDNS(fakeResolver(t, false)); err == nil {
		t.Fatal("expected a silent resolver to fail")
	}
}

func TestDNSServersSkipsSearchDomains(t *testing.T) {
	got := dnsServers(" 1.1.1.1, 1.0.0.1 ,corp.example, 2606:4700:4700::1111")
	want := []string{"1.1.1.1", "1.0.0.1", "2606:4700:4700::1111"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dnsServers = %v, want %v", got, want)
	}
}
