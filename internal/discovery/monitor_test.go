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

func TestMonitorLogsIncomingDatagrams(t *testing.T) {
	output := &lockedBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	monitor, err := StartUDPMonitor(ctx, "127.0.0.1:0", output)
	if err != nil {
		t.Fatal(err)
	}
	defer monitor.Close()

	sender, err := net.DialUDP("udp", nil, monitor.LocalAddr())
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()

	payload := []byte{0x01, 0x02, 0xfe}
	if _, err := sender.Write(payload); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(time.Second)
	for !strings.Contains(output.String(), "0102fe") {
		select {
		case err := <-monitor.Done():
			t.Fatalf("monitor stopped before logging packet: %v", err)
		case <-deadline:
			t.Fatalf("monitor did not log packet; output: %q", output.String())
		case <-time.After(time.Millisecond):
		}
	}
	if !strings.Contains(output.String(), "3 bytes") {
		t.Fatalf("output does not report datagram size: %q", output.String())
	}
}

func TestMonitorStopsOnContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	output := &lockedBuffer{}
	monitor, err := StartUDPMonitor(ctx, "127.0.0.1:0", output)
	if err != nil {
		t.Fatal(err)
	}

	cancel()
	select {
	case err := <-monitor.Done():
		if err != nil {
			t.Fatalf("monitor stopped with error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("monitor did not stop after context cancellation")
	}
}
