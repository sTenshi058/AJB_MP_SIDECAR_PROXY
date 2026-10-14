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

type Responder struct {
	conn          *net.UDPConn
	output        io.Writer
	advertisement Advertisement
	done          chan error
	once          sync.Once
}

func StartUDPResponder(ctx context.Context, address string, advertisement Advertisement, output io.Writer) (*Responder, error) {
	if output == nil {
		return nil, errors.New("discovery responder output cannot be nil")
	}
	if _, err := advertisement.Response([8]byte{}); err != nil {
		return nil, fmt.Errorf("validate discovery advertisement: %w", err)
	}
	addr, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		return nil, fmt.Errorf("resolve UDP discovery address: %w", err)
	}

	listenConfig := net.ListenConfig{Control: setDiscoverySocketOptions}
	packetConn, err := listenConfig.ListenPacket(ctx, "udp", addr.String())
	if err != nil {
		return nil, fmt.Errorf("listen for UDP discovery packets on %s: %w", addr, err)
	}
	conn, ok := packetConn.(*net.UDPConn)
	if !ok {
		_ = packetConn.Close()
		return nil, errors.New("UDP discovery listener returned an unexpected connection type")
	}

	responder := &Responder{
		conn:          conn,
		output:        output,
		advertisement: advertisement,
		done:          make(chan error, 1),
	}
	if _, err := fmt.Fprintf(output, "[UDP 14001] discovery responder listening on %s; advertising %s at 127.0.0.1:%d\n",
		conn.LocalAddr(), advertisement.Name, advertisement.Endpoint.Port); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("write UDP discovery status: %w", err)
	}
	go responder.run(ctx)
	return responder, nil
}

func (r *Responder) Done() <-chan error {
	return r.done
}

func (r *Responder) LocalAddr() *net.UDPAddr {
	addr, _ := r.conn.LocalAddr().(*net.UDPAddr)
	if addr == nil {
		return nil
	}
	return cloneUDPAddr(addr)
}

func (r *Responder) Close() error {
	var err error
	r.once.Do(func() {
		err = r.conn.Close()
	})
	return err
}

func (r *Responder) run(ctx context.Context) {
	defer close(r.done)
	defer r.Close()
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-ctx.Done():
			_ = r.Close()
		case <-finished:
		}
	}()

	buffer := make([]byte, 65535)
	for {
		n, addr, err := r.conn.ReadFromUDP(buffer)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				r.done <- nil
				return
			}
			r.done <- fmt.Errorf("read UDP discovery packet: %w", err)
			return
		}

		token, ok := ParseQuery(buffer[:n])
		if !ok {
			if err := r.logUnrecognized(addr, buffer[:n]); err != nil {
				r.done <- err
				return
			}
			continue
		}

		response, err := r.advertisement.Response(token)
		if err != nil {
			r.done <- fmt.Errorf("build UDP discovery response: %w", err)
			return
		}
		broadcastAddr := &net.UDPAddr{IP: net.IPv4bcast, Port: addr.Port}
		if _, err := r.conn.WriteToUDP(response, broadcastAddr); err != nil {
			if ctx.Err() != nil {
				r.done <- nil
				return
			}
			r.done <- fmt.Errorf("broadcast UDP discovery response for query from %s: %w", addr, err)
			return
		}
		if _, err := fmt.Fprintf(
			r.output,
			"[UDP 14001] valid SQ query from %s (%d bytes); broadcast SR response (%d bytes), advertised endpoint 127.0.0.1:%d\n",
			addr,
			n,
			len(response),
			r.advertisement.Endpoint.Port,
		); err != nil {
			r.done <- fmt.Errorf("write UDP discovery response log: %w", err)
			return
		}
	}
}

func (r *Responder) logUnrecognized(addr *net.UDPAddr, packet []byte) error {
	previewSize := len(packet)
	if previewSize > payloadPreviewSize {
		previewSize = payloadPreviewSize
	}
	if _, err := fmt.Fprintf(
		r.output,
		"[UDP 14001] received unrecognized datagram from %s (%d bytes, payload prefix hex=%s)\n",
		addr,
		len(packet),
		hex.EncodeToString(packet[:previewSize]),
	); err != nil {
		return fmt.Errorf("write UDP discovery packet log: %w", err)
	}
	return nil
}

func cloneUDPAddr(addr *net.UDPAddr) *net.UDPAddr {
	if addr == nil {
		return nil
	}
	return &net.UDPAddr{IP: append(net.IP(nil), addr.IP...), Port: addr.Port, Zone: addr.Zone}
}
