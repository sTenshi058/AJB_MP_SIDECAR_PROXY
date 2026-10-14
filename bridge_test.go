package relaybridge

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

type testTransport struct {
	outgoing chan testPacket
	incoming chan testPacket
}

type testPacket struct {
	peerID string
	data   []byte
}

func newTestTransport() *testTransport {
	return &testTransport{
		outgoing: make(chan testPacket, 256),
		incoming: make(chan testPacket, 256),
	}
}

func (t *testTransport) Send(ctx context.Context, peerID string, packet []byte) error {
	select {
	case t.outgoing <- testPacket{peerID: peerID, data: clonePacket(packet)}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *testTransport) Receive(ctx context.Context) (string, []byte, error) {
	select {
	case packet := <-t.incoming:
		return packet.peerID, clonePacket(packet.data), nil
	case <-ctx.Done():
		return "", nil, ctx.Err()
	}
}

func TestClientBridgeForwardsDatagramsBothWays(t *testing.T) {
	transport := newTestTransport()
	bridge, err := NewClientBridge("127.0.0.1:0", "host-peer", transport)
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()

	game, err := net.DialUDP("udp", nil, bridge.LocalAddr())
	if err != nil {
		t.Fatal(err)
	}
	defer game.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- bridge.Run(ctx) }()

	if _, err := game.Write([]byte("client packet")); err != nil {
		t.Fatal(err)
	}
	outbound := receiveTestPacket(t, transport.outgoing)
	if outbound.peerID != "host-peer" || string(outbound.data) != "client packet" {
		t.Fatalf("unexpected forwarded packet: %#v", outbound)
	}

	transport.incoming <- testPacket{peerID: "host-peer", data: []byte("host packet")}
	if got := readPacket(t, game); got != "host packet" {
		t.Fatalf("got %q; want host packet", got)
	}

	cancel()
	if err := receiveRunResult(t, done); err != nil {
		t.Fatalf("bridge returned error after cancellation: %v", err)
	}
}

func TestClientBridgeFollowsChangingGameSourcePort(t *testing.T) {
	transport := newTestTransport()
	bridge, err := NewClientBridge("127.0.0.1:0", "host-peer", transport)
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()

	firstGame, err := net.DialUDP("udp", nil, bridge.LocalAddr())
	if err != nil {
		t.Fatal(err)
	}
	defer firstGame.Close()
	secondGame, err := net.DialUDP("udp", nil, bridge.LocalAddr())
	if err != nil {
		t.Fatal(err)
	}
	defer secondGame.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- bridge.Run(ctx) }()

	if _, err := firstGame.Write([]byte("first socket")); err != nil {
		t.Fatal(err)
	}
	if packet := receiveTestPacket(t, transport.outgoing); string(packet.data) != "first socket" {
		t.Fatalf("first forwarded packet = %q, want first socket", packet.data)
	}

	if _, err := secondGame.Write([]byte("new socket")); err != nil {
		t.Fatal(err)
	}
	if packet := receiveTestPacket(t, transport.outgoing); string(packet.data) != "new socket" {
		t.Fatalf("second forwarded packet = %q, want new socket", packet.data)
	}

	transport.incoming <- testPacket{peerID: "host-peer", data: []byte("reply to active socket")}
	if got := readPacket(t, secondGame); got != "reply to active socket" {
		t.Fatalf("reply arrived at %q; want active second socket", got)
	}

	cancel()
	if err := receiveRunResult(t, done); err != nil {
		t.Fatalf("bridge returned error after cancellation: %v", err)
	}
}

func TestHostBridgeUsesPeerSpecificPorts(t *testing.T) {
	game, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer game.Close()

	transport := newTestTransport()
	bridge, err := NewHostBridge(game.LocalAddr().String(), transport)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- bridge.Run(ctx) }()

	transport.incoming <- testPacket{peerID: "client-a", data: []byte("to game a")}
	transport.incoming <- testPacket{peerID: "client-b", data: []byte("to game b")}
	toGameA := readPacketWithAddr(t, game)
	toGameB := readPacketWithAddr(t, game)
	if string(toGameA.data) != "to game a" || string(toGameB.data) != "to game b" {
		t.Fatalf("got %q and %q; want to game a and to game b", toGameA.data, toGameB.data)
	}
	if !toGameA.addr.IP.IsLoopback() || !toGameB.addr.IP.IsLoopback() {
		t.Fatalf("game packet sources are not loopback: %v and %v", toGameA.addr, toGameB.addr)
	}
	if toGameA.addr.Port == toGameB.addr.Port {
		t.Fatalf("peers share a local UDP source port: %v", toGameA.addr)
	}

	for _, target := range []struct {
		peerID string
		addr   *net.UDPAddr
		reply  string
	}{
		{peerID: "client-a", addr: toGameA.addr, reply: "game reply a"},
		{peerID: "client-b", addr: toGameB.addr, reply: "game reply b"},
	} {
		if _, err := game.WriteToUDP([]byte(target.reply), target.addr); err != nil {
			t.Fatal(err)
		}
	}

	outboundA := receiveTestPacket(t, transport.outgoing)
	outboundB := receiveTestPacket(t, transport.outgoing)
	gotReplies := map[string]string{outboundA.peerID: string(outboundA.data), outboundB.peerID: string(outboundB.data)}
	if gotReplies["client-a"] != "game reply a" || gotReplies["client-b"] != "game reply b" {
		t.Fatalf("unexpected forwarded replies: %#v", gotReplies)
	}

	cancel()
	if err := receiveRunResult(t, done); err != nil {
		t.Fatalf("bridge returned error after cancellation: %v", err)
	}
}

func TestClientBridgeRequiresLoopbackBind(t *testing.T) {
	_, err := NewClientBridge("0.0.0.0:1170", "peer", newTestTransport())
	if err == nil {
		t.Fatal("expected non-loopback bind to be rejected")
	}
}

func TestClientBridgeRejectsUnexpectedPeer(t *testing.T) {
	transport := newTestTransport()
	bridge, err := NewClientBridge("127.0.0.1:0", "expected", transport)
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()

	game, err := net.DialUDP("udp", nil, bridge.LocalAddr())
	if err != nil {
		t.Fatal(err)
	}
	defer game.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- bridge.Run(ctx) }()

	if _, err := game.Write([]byte("first packet")); err != nil {
		t.Fatal(err)
	}
	_ = receiveTestPacket(t, transport.outgoing)
	transport.incoming <- testPacket{peerID: "unexpected", data: []byte("injected")}

	err = receiveRunResult(t, done)
	if err == nil || !strings.Contains(err.Error(), "unexpected peer") {
		t.Fatalf("got error %v; want unexpected peer error", err)
	}
}

func receiveTestPacket(t *testing.T, packets <-chan testPacket) testPacket {
	t.Helper()
	select {
	case packet := <-packets:
		return packet
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for forwarded packet")
		return testPacket{}
	}
}

func readPacket(t *testing.T, conn *net.UDPConn) string {
	t.Helper()
	packet := readPacketWithAddr(t, conn)
	return string(packet.data)
}

func readPacketWithAddr(t *testing.T, conn *net.UDPConn) testDatagram {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buffer := make([]byte, maxUDPPacketSize)
	n, addr, err := conn.ReadFromUDP(buffer)
	if err != nil {
		t.Fatalf("read UDP packet: %v", err)
	}
	return testDatagram{data: clonePacket(buffer[:n]), addr: addr}
}

type testDatagram struct {
	data []byte
	addr *net.UDPAddr
}

func receiveRunResult(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for bridge to stop")
		return errors.New("timeout")
	}
}
