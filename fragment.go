package relaybridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	fragmentHeaderSize = 16
	fragmentPacketSize = 1170
	fragmentDataSize   = fragmentPacketSize - fragmentHeaderSize
	maxDatagramSize    = 65507
	maxFragments       = (maxDatagramSize + fragmentDataSize - 1) / fragmentDataSize
	maxAssemblies      = 128
	maxAssemblyBytes   = 4 << 20
	assemblyLifetime   = 10 * time.Second
)

var fragmentMagic = [4]byte{'A', 'J', 'B', '1'}

// FragmentingTransport keeps EOS P2P datagrams within EOS_P2P_MAX_PACKET_SIZE
// and reassembles each original UDP datagram at the receiving side.
type FragmentingTransport struct {
	inner Transport

	sendMu sync.Mutex
	nextID uint32

	receiveMu  sync.Mutex
	assemblies map[assemblyKey]*packetAssembly
	usedBytes  int
}

type assemblyKey struct {
	peerID string
	id     uint32
}

type packetAssembly struct {
	total    int
	pieces   [][]byte
	received int
	deadline time.Time
}

func NewFragmentingTransport(inner Transport) (*FragmentingTransport, error) {
	if inner == nil {
		return nil, errors.New("inner transport cannot be nil")
	}
	return &FragmentingTransport{
		inner:      inner,
		assemblies: make(map[assemblyKey]*packetAssembly),
	}, nil
}

func (t *FragmentingTransport) Send(ctx context.Context, peerID string, packet []byte) error {
	if len(packet) > maxDatagramSize {
		return fmt.Errorf("UDP datagram is %d bytes; maximum supported size is %d", len(packet), maxDatagramSize)
	}
	count := (len(packet) + fragmentDataSize - 1) / fragmentDataSize
	if count == 0 {
		count = 1
	}
	if count > maxFragments {
		return fmt.Errorf("UDP datagram requires %d fragments; maximum is %d", count, maxFragments)
	}

	t.sendMu.Lock()
	defer t.sendMu.Unlock()
	t.nextID++
	if t.nextID == 0 {
		t.nextID++
	}
	id := t.nextID

	for index := 0; index < count; index++ {
		start := index * fragmentDataSize
		end := start + fragmentDataSize
		if end > len(packet) {
			end = len(packet)
		}
		frame := make([]byte, fragmentHeaderSize+end-start)
		copy(frame[:4], fragmentMagic[:])
		binary.BigEndian.PutUint32(frame[4:8], id)
		binary.BigEndian.PutUint32(frame[8:12], uint32(len(packet)))
		binary.BigEndian.PutUint16(frame[12:14], uint16(index))
		binary.BigEndian.PutUint16(frame[14:16], uint16(count))
		copy(frame[fragmentHeaderSize:], packet[start:end])
		if err := t.inner.Send(ctx, peerID, frame); err != nil {
			return fmt.Errorf("send fragment %d/%d to peer %q: %w", index+1, count, peerID, err)
		}
	}
	return nil
}

func (t *FragmentingTransport) Receive(ctx context.Context) (string, []byte, error) {
	for {
		peerID, frame, err := t.inner.Receive(ctx)
		if err != nil {
			return "", nil, err
		}
		packet, complete, err := t.acceptFragment(peerID, frame)
		if err != nil {
			return "", nil, err
		}
		if complete {
			return peerID, packet, nil
		}
	}
}

func (t *FragmentingTransport) acceptFragment(peerID string, frame []byte) ([]byte, bool, error) {
	if len(frame) < fragmentHeaderSize || string(frame[:4]) != string(fragmentMagic[:]) {
		return nil, false, errors.New("received invalid relay fragment header")
	}
	id := binary.BigEndian.Uint32(frame[4:8])
	total := int(binary.BigEndian.Uint32(frame[8:12]))
	index := int(binary.BigEndian.Uint16(frame[12:14]))
	count := int(binary.BigEndian.Uint16(frame[14:16]))
	if id == 0 || total > maxDatagramSize || count < 1 || count > maxFragments || index >= count {
		return nil, false, errors.New("received relay fragment with invalid metadata")
	}
	expectedCount := (total + fragmentDataSize - 1) / fragmentDataSize
	if expectedCount == 0 {
		expectedCount = 1
	}
	if count != expectedCount {
		return nil, false, errors.New("received relay fragment with inconsistent fragment count")
	}
	data := frame[fragmentHeaderSize:]
	expectedSize := fragmentDataSize
	if index == count-1 {
		expectedSize = total - index*fragmentDataSize
	}
	if len(data) != expectedSize {
		return nil, false, errors.New("received relay fragment with inconsistent payload size")
	}

	t.receiveMu.Lock()
	defer t.receiveMu.Unlock()
	now := time.Now()
	t.expireAssemblies(now)

	key := assemblyKey{peerID: peerID, id: id}
	assembly := t.assemblies[key]
	if assembly == nil {
		if len(t.assemblies) >= maxAssemblies || t.usedBytes+total > maxAssemblyBytes {
			return nil, false, errors.New("relay fragment reassembly capacity exceeded")
		}
		assembly = &packetAssembly{
			total:    total,
			pieces:   make([][]byte, count),
			deadline: now.Add(assemblyLifetime),
		}
		t.assemblies[key] = assembly
		t.usedBytes += total
	} else if assembly.total != total || len(assembly.pieces) != count {
		t.removeAssembly(key, assembly)
		return nil, false, errors.New("received inconsistent fragments for one relay datagram")
	}

	if assembly.pieces[index] != nil {
		return nil, false, nil
	}
	assembly.pieces[index] = make([]byte, len(data))
	copy(assembly.pieces[index], data)
	assembly.received += len(data)
	for _, piece := range assembly.pieces {
		if piece == nil {
			return nil, false, nil
		}
	}

	packet := make([]byte, 0, assembly.total)
	for _, piece := range assembly.pieces {
		packet = append(packet, piece...)
	}
	t.removeAssembly(key, assembly)
	return packet, true, nil
}

func (t *FragmentingTransport) expireAssemblies(now time.Time) {
	for key, assembly := range t.assemblies {
		if !now.Before(assembly.deadline) {
			t.removeAssembly(key, assembly)
		}
	}
}

func (t *FragmentingTransport) removeAssembly(key assemblyKey, assembly *packetAssembly) {
	delete(t.assemblies, key)
	t.usedBytes -= assembly.total
}
