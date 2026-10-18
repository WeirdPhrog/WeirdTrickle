package dnsMITMProxy

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestReusedUDPSocketIgnoresUnrelatedResponses(t *testing.T) {
	for _, reuseID := range []bool{false, true} {
		t.Run(fmt.Sprintf("reuseID=%t", reuseID), func(t *testing.T) {
			testReusedUDPSocketIgnoresUnrelatedResponses(t, reuseID)
		})
	}
}

func testReusedUDPSocketIgnoresUnrelatedResponses(t *testing.T, reuseID bool) {
	t.Helper()
	server, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	defer func() { server.Close(); <-done }()
	go func() {
		defer close(done)
		buf := make([]byte, dns.MaxMsgSize)
		var previous []byte
		var firstClient string
		for i := 0; i < 2; i++ {
			server.SetReadDeadline(time.Now().Add(2 * time.Second))
			n, client, err := server.ReadFrom(buf)
			if err != nil {
				t.Error(err)
				return
			}
			if i == 0 {
				firstClient = client.String()
			} else if client.String() != firstClient {
				t.Error("UDP socket was not reused")
			}
			var query dns.Msg
			if err := query.Unpack(buf[:n]); err != nil {
				t.Error(err)
				return
			}
			if i == 1 {
				// A duplicate old reply, a short packet, and an echoed query.
				for _, invalid := range [][]byte{previous, {0, 1}, buf[:n]} {
					if _, err := server.WriteTo(invalid, client); err != nil {
						t.Error(err)
						return
					}
				}
			}
			reply := new(dns.Msg).SetReply(&query)
			previous, err = reply.Pack()
			if err != nil {
				t.Error(err)
				return
			}
			if _, err := server.WriteTo(previous, client); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	proxy := NewDNSMITMProxy(server.LocalAddr().String(), 1, 1, time.Second)
	defer proxy.Close()
	hooks := 0
	proxy.ResponseHook = func(_ net.Addr, query, reply dns.Msg, _ string) (*dns.Msg, error) {
		hooks++
		if query.Id != reply.Id || query.Question[0] != reply.Question[0] {
			t.Error("an unrelated response reached the routing hook")
		}
		return nil, nil
	}
	for i, id := range []uint16{123, 456} {
		query := new(dns.Msg).SetQuestion(fmt.Sprintf("query%d.example.com.", i), dns.TypeA)
		if reuseID {
			id = 123
		}
		query.Id = id
		data, _ := query.Pack()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		response, err := proxy.processReq(ctx, nil, data, "udp")
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		var reply dns.Msg
		if err := reply.Unpack(response); err != nil || reply.Id != id || reply.Question[0] != query.Question[0] {
			t.Fatalf("wrong response: id=%d, error=%v", reply.Id, err)
		}
	}
	if hooks != 2 {
		t.Fatalf("response hook called %d times, want 2", hooks)
	}
}

func TestTCPUnexpectedIDDiscardsConnection(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	listener.(*net.TCPListener).SetDeadline(time.Now().Add(2 * time.Second))
	closed := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			closed <- err
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(2 * time.Second))
		prefix := make([]byte, 2)
		if _, err := io.ReadFull(conn, prefix); err != nil {
			closed <- err
			return
		}
		data := make([]byte, binary.BigEndian.Uint16(prefix))
		if _, err := io.ReadFull(conn, data); err != nil {
			closed <- err
			return
		}
		var query dns.Msg
		if err := query.Unpack(data); err != nil {
			closed <- err
			return
		}
		reply := new(dns.Msg).SetReply(&query)
		reply.Id++
		data, _ = reply.Pack()
		binary.BigEndian.PutUint16(prefix, uint16(len(data)))
		if _, err := conn.Write(append(prefix, data...)); err != nil {
			closed <- err
			return
		}
		_, err = conn.Read(prefix)
		closed <- err
	}()
	proxy := NewDNSMITMProxy(listener.Addr().String(), 1, 1, time.Second)
	defer proxy.Close()
	query, _ := new(dns.Msg).SetQuestion("example.com.", dns.TypeA).Pack()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if response, err := proxy.requestUpstreamDNS(ctx, query, "tcp"); err == nil || response != nil || !strings.Contains(err.Error(), "unexpected DNS response") {
		t.Fatalf("expected rejection of the response ID, got %v: %v", response, err)
	}
	if err := <-closed; err != io.EOF {
		t.Fatalf("invalid connection was not closed: %v", err)
	}
	if len(proxy.tcpConnPool.pool) != 0 {
		t.Fatal("invalid connection was returned to the pool")
	}
}

func TestUnexpectedUDPResponseStillTimesOut(t *testing.T) {
	server, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	defer func() { server.Close(); <-done }()
	go func() {
		defer close(done)
		buf := make([]byte, dns.MaxMsgSize)
		server.SetReadDeadline(time.Now().Add(time.Second))
		_, client, err := server.ReadFrom(buf)
		if err == nil {
			server.WriteTo([]byte{0, 1}, client)
		}
	}()
	proxy := NewDNSMITMProxy(server.LocalAddr().String(), 1, 1, 50*time.Millisecond)
	defer proxy.Close()
	query, _ := new(dns.Msg).SetQuestion("example.com.", dns.TypeA).Pack()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	response, err := proxy.requestUpstreamDNS(ctx, query, "udp")
	var networkErr net.Error
	if response != nil || !errors.As(err, &networkErr) || !networkErr.Timeout() {
		t.Fatalf("expected a timeout while discarding the malformed packet, got %v: %v", response, err)
	}
}

func TestDNSResponseQuestionMatching(t *testing.T) {
	query := new(dns.Msg).SetQuestion("Example.COM.", dns.TypeA)
	request, _ := query.Pack()
	for _, tc := range []struct {
		name   string
		change func(*dns.Msg)
		match  bool
	}{
		{"unchanged", func(*dns.Msg) {}, true},
		{"case insensitive", func(m *dns.Msg) { m.Question[0].Name = "eXaMpLe.com." }, true},
		{"different name", func(m *dns.Msg) { m.Question[0].Name = "other.example." }, false},
		{"different type", func(m *dns.Msg) { m.Question[0].Qtype = dns.TypeAAAA }, false},
		{"different class", func(m *dns.Msg) { m.Question[0].Qclass = dns.ClassCHAOS }, false},
		{"different opcode", func(m *dns.Msg) { m.Opcode = dns.OpcodeStatus }, false},
		{"missing question", func(m *dns.Msg) { m.Question = nil }, false},
		{"failure with matching question", func(m *dns.Msg) { m.Rcode = dns.RcodeServerFailure }, true},
		{"FORMERR without question", func(m *dns.Msg) { m.Question = nil; m.Rcode = dns.RcodeFormatError }, true},
		{"NOTIMP without question", func(m *dns.Msg) { m.Question = nil; m.Rcode = dns.RcodeNotImplemented }, true},
		{"REFUSED without question", func(m *dns.Msg) { m.Question = nil; m.Rcode = dns.RcodeRefused; m.SetEdns0(1232, false) }, true},
		{"failure with wrong question", func(m *dns.Msg) { m.Rcode = dns.RcodeServerFailure; m.Question[0].Name = "other.example." }, false},
		{"questionless error with answer", func(m *dns.Msg) {
			m.Question = nil
			m.Rcode = dns.RcodeRefused
			m.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: "example.com.", Rrtype: dns.TypeA, Class: dns.ClassINET}, A: net.IPv4(192, 0, 2, 1)}}
		}, false},
		{"questionless error with additional address", func(m *dns.Msg) {
			m.Question = nil
			m.Rcode = dns.RcodeRefused
			m.Extra = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: "example.com.", Rrtype: dns.TypeA, Class: dns.ClassINET}, A: net.IPv4(192, 0, 2, 1)}}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reply := new(dns.Msg).SetReply(query)
			tc.change(reply)
			response, err := reply.Pack()
			if err != nil {
				t.Fatal(err)
			}
			if actual := matchesDNSResponse(request, response); actual != tc.match {
				t.Fatalf("matchesDNSResponse = %t, want %t", actual, tc.match)
			}
		})
	}
	for _, malformed := range [][]byte{nil, request[:11], append(append([]byte(nil), request[:12]...), 0xc0, 12)} {
		if len(malformed) >= 3 {
			malformed[2] |= 0x80
		}
		if matchesDNSResponse(request, malformed) {
			t.Fatal("accepted a malformed question")
		}
	}
}
