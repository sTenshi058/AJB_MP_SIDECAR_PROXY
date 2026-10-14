package relaybridge

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
)

const maxUDPPacketSize = 65535

// Transport carries complete UDP datagrams between authenticated peers.
// Send may be called concurrently. Receive must return when ctx is canceled.
type Transport interface {
	Send(ctx context.Context, peerID string, packet []byte) error
	Receive(ctx context.Context) (peerID string, packet []byte, err error)
}

// ClientBridge exposes a loopback UDP endpoint for the game client and carries
// those datagrams to one remote peer through Transport.
type ClientBridge struct {
	conn      *net.UDPConn
	peerID    string
	transport Transport

	gameAddrMu sync.RWMutex
	gameAddr   *net.UDPAddr

	firstGamePacket sync.Once
	firstPeerPacket sync.Once
}

func NewClientBridge(listenAddr, peerID string, transport Transport) (*ClientBridge, error) {
	if peerID == "" {
		return nil, errors.New("peer ID cannot be empty")
	}
	if transport == nil {
		return nil, errors.New("transport cannot be nil")
	}

	addr, err := net.ResolveUDPAddr("udp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("resolve client listen address: %w", err)
	}
	if addr.IP == nil || !addr.IP.IsLoopback() {
		return nil, errors.New("client bridge must bind to a loopback address")
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen for local game packets: %w", err)
	}

	return &ClientBridge{conn: conn, peerID: peerID, transport: transport}, nil
}

func (b *ClientBridge) LocalAddr() *net.UDPAddr {
	addr, _ := b.conn.LocalAddr().(*net.UDPAddr)
	if addr == nil {
		return nil
	}
	return cloneUDPAddr(addr)
}

func (b *ClientBridge) Close() error {
	return b.conn.Close()
}

func (b *ClientBridge) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	errs := make(chan error, 2)
	var tasks sync.WaitGroup
	tasks.Add(2)

	go func() {
		defer tasks.Done()
		errs <- b.forwardGamePackets(runCtx)
	}()
	go func() {
		defer tasks.Done()
		errs <- b.forwardPeerPackets(runCtx)
	}()

	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-errs:
	}

	cancel()
	_ = b.conn.Close()
	tasks.Wait()

	if ctx.Err() != nil {
		return nil
	}
	return runErr
}

func (b *ClientBridge) forwardGamePackets(ctx context.Context) error {
	buffer := make([]byte, maxUDPPacketSize)
	for {
		n, addr, err := b.conn.ReadFromUDP(buffer)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("read local game packet: %w", err)
		}
		if !addr.IP.IsLoopback() {
			continue
		}

		b.gameAddrMu.Lock()
		previousGameAddr := cloneUDPAddr(b.gameAddr)
		b.gameAddr = cloneUDPAddr(addr)
		b.gameAddrMu.Unlock()
		if previousGameAddr != nil && !sameUDPAddr(addr, previousGameAddr) {
			log.Printf("[UDP 1170] game source changed from %s to %s; forwarding from the new socket", previousGameAddr, addr)
		}

		if err := b.transport.Send(ctx, b.peerID, clonePacket(buffer[:n])); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("send client packet to peer %q: %w", b.peerID, err)
		}
		b.firstGamePacket.Do(func() {
			log.Printf("[UDP 1170] forwarded first game packet (%d bytes) to EOS peer %s", n, b.peerID)
		})
	}
}

func (b *ClientBridge) forwardPeerPackets(ctx context.Context) error {
	for {
		peerID, packet, err := b.transport.Receive(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("receive packet from relay transport: %w", err)
		}
		if peerID != b.peerID {
			return fmt.Errorf("received packet from unexpected peer %q", peerID)
		}

		b.gameAddrMu.RLock()
		gameAddr := cloneUDPAddr(b.gameAddr)
		b.gameAddrMu.RUnlock()
		if gameAddr == nil {
			continue
		}
		if _, err := b.conn.WriteToUDP(packet, gameAddr); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("write relay packet to local game: %w", err)
		}
		b.firstPeerPacket.Do(func() {
			log.Printf("[UDP 1170] forwarded first EOS packet (%d bytes) to local game", len(packet))
		})
	}
}

// HostBridge gives each remote peer its own loopback UDP source port. The game
// therefore sees distinct clients and replies to the correct peer-specific port.
type HostBridge struct {
	gameAddr  *net.UDPAddr
	transport Transport

	mu     sync.Mutex
	peers  map[string]*net.UDPConn
	sendMu sync.Mutex

	firstPeerPacket sync.Once
	firstGamePacket sync.Once
}

func NewHostBridge(gameAddr string, transport Transport) (*HostBridge, error) {
	if transport == nil {
		return nil, errors.New("transport cannot be nil")
	}
	addr, err := net.ResolveUDPAddr("udp", gameAddr)
	if err != nil {
		return nil, fmt.Errorf("resolve host game address: %w", err)
	}
	if addr.IP == nil || !addr.IP.IsLoopback() {
		return nil, errors.New("host game address must be loopback")
	}

	return &HostBridge{
		gameAddr:  addr,
		transport: transport,
		peers:     make(map[string]*net.UDPConn),
	}, nil
}

func (b *HostBridge) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	errs := make(chan error, 1)
	var tasks sync.WaitGroup

	tasks.Add(1)
	go func() {
		defer tasks.Done()
		if err := b.receivePeerPackets(runCtx, &tasks, errs); err != nil {
			reportError(errs, err)
		}
	}()

	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-errs:
	}

	cancel()
	b.closePeers()
	tasks.Wait()
	if ctx.Err() != nil {
		return nil
	}
	return runErr
}

func (b *HostBridge) receivePeerPackets(ctx context.Context, tasks *sync.WaitGroup, errs chan<- error) error {
	for {
		peerID, packet, err := b.transport.Receive(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("receive packet from relay transport: %w", err)
		}
		if peerID == "" {
			return errors.New("received packet with empty peer ID")
		}

		conn, err := b.peerConn(peerID, ctx, tasks, errs)
		if err != nil {
			return err
		}
		if _, err := conn.WriteToUDP(packet, b.gameAddr); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("write peer %q packet to local game: %w", peerID, err)
		}
		b.firstPeerPacket.Do(func() {
			log.Printf("[UDP 1170] forwarded first EOS packet (%d bytes) from peer %s to local game", len(packet), peerID)
		})
	}
}

func (b *HostBridge) peerConn(peerID string, ctx context.Context, tasks *sync.WaitGroup, errs chan<- error) (*net.UDPConn, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if conn := b.peers[peerID]; conn != nil {
		return conn, nil
	}
	network := "udp4"
	localAddr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}
	if b.gameAddr.IP.To4() == nil {
		network = "udp6"
		localAddr = &net.UDPAddr{IP: net.IPv6loopback}
	}
	conn, err := net.ListenUDP(network, localAddr)
	if err != nil {
		return nil, fmt.Errorf("create local UDP endpoint for peer %q: %w", peerID, err)
	}

	b.peers[peerID] = conn
	tasks.Add(1)
	go func() {
		defer tasks.Done()
		if err := b.forwardGameReplies(ctx, peerID, conn); err != nil {
			reportError(errs, err)
		}
	}()
	return conn, nil
}

func (b *HostBridge) forwardGameReplies(ctx context.Context, peerID string, conn *net.UDPConn) error {
	buffer := make([]byte, maxUDPPacketSize)
	for {
		n, addr, err := conn.ReadFromUDP(buffer)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("read game reply for peer %q: %w", peerID, err)
		}
		if !addr.IP.IsLoopback() || !sameUDPAddr(addr, b.gameAddr) {
			continue
		}
		b.sendMu.Lock()
		err = b.transport.Send(ctx, peerID, clonePacket(buffer[:n]))
		b.sendMu.Unlock()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("send game reply to peer %q: %w", peerID, err)
		}
		b.firstGamePacket.Do(func() {
			log.Printf("[UDP 1170] forwarded first local-game packet (%d bytes) to EOS peer %s", n, peerID)
		})
	}
}

func (b *HostBridge) closePeers() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for peerID, conn := range b.peers {
		_ = conn.Close()
		delete(b.peers, peerID)
	}
}

func reportError(errs chan<- error, err error) {
	if err == nil {
		return
	}
	select {
	case errs <- err:
	default:
	}
}

func clonePacket(packet []byte) []byte {
	return append([]byte(nil), packet...)
}

func cloneUDPAddr(addr *net.UDPAddr) *net.UDPAddr {
	if addr == nil {
		return nil
	}
	return &net.UDPAddr{IP: append(net.IP(nil), addr.IP...), Port: addr.Port, Zone: addr.Zone}
}

func sameUDPAddr(a, b *net.UDPAddr) bool {
	return a != nil && b != nil && a.Port == b.Port && a.Zone == b.Zone && a.IP.Equal(b.IP)
}
