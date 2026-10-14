package discovery

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
)

const payloadPreviewSize = 32

type Monitor struct {
	conn   *net.UDPConn
	output io.Writer
	done   chan error
	once   sync.Once
}

func StartUDPMonitor(ctx context.Context, address string, output io.Writer) (*Monitor, error) {
	if output == nil {
		return nil, errors.New("discovery monitor output cannot be nil")
	}
	addr, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		return nil, fmt.Errorf("resolve UDP discovery address: %w", err)
	}

	listenConfig := net.ListenConfig{Control: setReuseAddress}
	packetConn, err := listenConfig.ListenPacket(ctx, "udp", addr.String())
	if err != nil {
		return nil, fmt.Errorf("listen for UDP discovery packets on %s: %w", addr, err)
	}
	conn, ok := packetConn.(*net.UDPConn)
	if !ok {
		_ = packetConn.Close()
		return nil, errors.New("UDP discovery listener returned an unexpected connection type")
	}

	monitor := &Monitor{
		conn:   conn,
		output: output,
		done:   make(chan error, 1),
	}
	if _, err := fmt.Fprintf(output, "[UDP 14001] monitoring discovery traffic on %s\n", conn.LocalAddr()); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("write UDP discovery status: %w", err)
	}
	go monitor.run(ctx)
	return monitor, nil
}

func (m *Monitor) Done() <-chan error {
	return m.done
}

func (m *Monitor) LocalAddr() *net.UDPAddr {
	addr, _ := m.conn.LocalAddr().(*net.UDPAddr)
	if addr == nil {
		return nil
	}
	return cloneUDPAddr(addr)
}

func (m *Monitor) Close() error {
	var err error
	m.once.Do(func() {
		err = m.conn.Close()
	})
	return err
}

func (m *Monitor) run(ctx context.Context) {
	defer close(m.done)
	defer m.Close()
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-ctx.Done():
			_ = m.Close()
		case <-finished:
		}
	}()

	buffer := make([]byte, 65535)
	for {
		n, addr, err := m.conn.ReadFromUDP(buffer)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				m.done <- nil
				return
			}
			m.done <- fmt.Errorf("read UDP discovery packet: %w", err)
			return
		}

		previewSize := n
		if previewSize > payloadPreviewSize {
			previewSize = payloadPreviewSize
		}
		if _, err := fmt.Fprintf(
			m.output,
			"[UDP 14001] received datagram from %s (%d bytes, payload prefix hex=%s)\n",
			addr,
			n,
			hex.EncodeToString(buffer[:previewSize]),
		); err != nil {
			m.done <- fmt.Errorf("write UDP discovery packet log: %w", err)
			return
		}
	}
}

func cloneUDPAddr(addr *net.UDPAddr) *net.UDPAddr {
	if addr == nil {
		return nil
	}
	return &net.UDPAddr{IP: append(net.IP(nil), addr.IP...), Port: addr.Port, Zone: addr.Zone}
}
