package discovery

import (
	"bytes"
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(data)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func TestResponderAnswersValidQuery(t *testing.T) {
	output := &lockedBuffer{}
	advertisement, err := NewAdvertisement("AJB Sidecar Relay", &net.UDPAddr{
		IP: net.IPv4(127, 0, 0, 1), Port: 1170,
	})
	if err != nil {
		t.Fatal(err)
	}
	responder, err := StartUDPResponder(context.Background(), "0.0.0.0:0", advertisement, output)
	if err != nil {
		t.Fatal(err)
	}
	defer responder.Close()

	target := responder.LocalAddr()
	target.IP = net.IPv4(127, 0, 0, 1)
	sender, err := net.DialUDP("udp", nil, target)
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()

	query := append([]byte(nil), querySignature[:]...)
	query = append(query, []byte("token123")...)
	if _, err := sender.Write(query); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(time.Second)
	for !strings.Contains(output.String(), "broadcast SR response") {
		select {
		case err := <-responder.Done():
			t.Fatalf("responder stopped before logging the reply: %v", err)
		case <-deadline:
			t.Fatalf("responder did not log its reply; output: %q", output.String())
		case <-time.After(time.Millisecond):
		}
	}
}

func TestResponderLogsButDoesNotAnswerMalformedQuery(t *testing.T) {
	output := &lockedBuffer{}
	advertisement, err := NewAdvertisement("AJB Sidecar Relay", &net.UDPAddr{
		IP: net.IPv4(127, 0, 0, 1), Port: 1170,
	})
	if err != nil {
		t.Fatal(err)
	}
	responder, err := StartUDPResponder(context.Background(), "0.0.0.0:0", advertisement, output)
	if err != nil {
		t.Fatal(err)
	}
	defer responder.Close()

	target := responder.LocalAddr()
	target.IP = net.IPv4(127, 0, 0, 1)
	sender, err := net.DialUDP("udp", nil, target)
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	if _, err := sender.Write([]byte{0, 1, 2}); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(time.Second)
	for !strings.Contains(output.String(), "unrecognized datagram") {
		select {
		case err := <-responder.Done():
			t.Fatalf("responder stopped before logging malformed packet: %v", err)
		case <-deadline:
			t.Fatalf("responder did not log malformed packet; output: %q", output.String())
		case <-time.After(time.Millisecond):
		}
	}

}

func TestResponderStopsOnContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	output := &lockedBuffer{}
	advertisement, err := NewAdvertisement("AJB Sidecar Relay", &net.UDPAddr{
		IP: net.IPv4(127, 0, 0, 1), Port: 1170,
	})
	if err != nil {
		t.Fatal(err)
	}
	responder, err := StartUDPResponder(ctx, "0.0.0.0:0", advertisement, output)
	if err != nil {
		t.Fatal(err)
	}

	cancel()
	select {
	case err := <-responder.Done():
		if err != nil {
			t.Fatalf("responder stopped with error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("responder did not stop after context cancellation")
	}
}
