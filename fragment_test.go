package relaybridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"
	"time"
)

func TestFragmentingTransportRoundTripsEOSSizedPackets(t *testing.T) {
	inner := newTestTransport()
	sender, err := NewFragmentingTransport(inner)
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := NewFragmentingTransport(inner)
	if err != nil {
		t.Fatal(err)
	}

	packet := make([]byte, 65507)
	for i := range packet {
		packet[i] = byte(i * 31)
	}
	if err := sender.Send(context.Background(), "peer", packet); err != nil {
		t.Fatal(err)
	}
	expectedFragments := len(inner.outgoing)
	if expectedFragments != maxFragments {
		t.Fatalf("sent %d fragments; want %d", expectedFragments, maxFragments)
	}
	for index := 0; index < expectedFragments; index++ {
		sent := <-inner.outgoing
		if len(sent.data) > fragmentPacketSize {
			t.Fatalf("sent %d-byte EOS packet; maximum is %d", len(sent.data), fragmentPacketSize)
		}
		inner.incoming <- sent
	}
	peerID, got, err := receiver.Receive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if peerID != "peer" || !bytes.Equal(got, packet) {
		t.Fatal("reassembled packet did not match original datagram")
	}
}

func TestFragmentingTransportSupportsEmptyDatagram(t *testing.T) {
	inner := newTestTransport()
	sender, _ := NewFragmentingTransport(inner)
	receiver, _ := NewFragmentingTransport(inner)

	if err := sender.Send(context.Background(), "peer", nil); err != nil {
		t.Fatal(err)
	}
	inner.incoming <- <-inner.outgoing
	peerID, packet, err := receiver.Receive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if peerID != "peer" || len(packet) != 0 {
		t.Fatalf("received peer %q packet length %d; want empty packet from peer", peerID, len(packet))
	}
}

func TestFragmentingTransportRejectsMalformedFrames(t *testing.T) {
	inner := newTestTransport()
	receiver, _ := NewFragmentingTransport(inner)
	frame := make([]byte, fragmentHeaderSize)
	copy(frame, fragmentMagic[:])
	binary.BigEndian.PutUint32(frame[4:8], 1)
	binary.BigEndian.PutUint32(frame[8:12], 32)
	binary.BigEndian.PutUint16(frame[12:14], 0)
	binary.BigEndian.PutUint16(frame[14:16], 2)
	inner.incoming <- testPacket{peerID: "peer", data: frame}

	if _, _, err := receiver.Receive(context.Background()); err == nil {
		t.Fatal("expected inconsistent fragment count to be rejected")
	}
}

func TestFragmentingTransportExpiresIncompleteFrames(t *testing.T) {
	inner := newTestTransport()
	receiver, _ := NewFragmentingTransport(inner)
	total := fragmentDataSize + 1
	count := 2
	first := make([]byte, fragmentHeaderSize+fragmentDataSize)
	copy(first[:4], fragmentMagic[:])
	binary.BigEndian.PutUint32(first[4:8], 7)
	binary.BigEndian.PutUint32(first[8:12], uint32(total))
	binary.BigEndian.PutUint16(first[12:14], 0)
	binary.BigEndian.PutUint16(first[14:16], uint16(count))

	receiver.receiveMu.Lock()
	receiver.assemblies[assemblyKey{peerID: "expired", id: 6}] = &packetAssembly{total: 1, deadline: time.Now().Add(-time.Second)}
	receiver.usedBytes++
	receiver.receiveMu.Unlock()
	inner.incoming <- testPacket{peerID: "peer", data: first}

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, _, err := receiver.Receive(ctx); err == nil {
		t.Fatal("expected incomplete datagram receive to stop at context deadline")
	}
	receiver.receiveMu.Lock()
	defer receiver.receiveMu.Unlock()
	if _, exists := receiver.assemblies[assemblyKey{peerID: "expired", id: 6}]; exists {
		t.Fatal("expired assembly was not removed")
	}
}
