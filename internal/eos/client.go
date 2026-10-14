package eos

/*
#cgo CXXFLAGS: -I${SRCDIR}/../../../SDK/Include
#include <stdlib.h>
#include "native.h"
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"github.com/sTenshi058/AJB_MP_SIDECAR_PROXY/internal/config"
)

const maxEOSPacketSize = 1170

type Client struct {
	mu     sync.Mutex
	handle *C.ajb_eos
}

func New(cfg config.Config, clientSecret, cacheDirectory string) (*Client, error) {
	values := []*C.char{
		C.CString("AJB Sidecar"),
		C.CString(cfg.ProductID),
		C.CString(cfg.SandboxID),
		C.CString(cfg.DeploymentID),
		C.CString(cfg.ClientID),
		C.CString(clientSecret),
		C.CString(cacheDirectory),
	}
	defer func() {
		for _, value := range values {
			C.free(unsafe.Pointer(value))
		}
	}()

	var message [1024]C.char
	handle := C.ajb_eos_create(
		values[0], values[1], values[2], values[3], values[4], values[5], values[6],
		&message[0], C.uint32_t(len(message)),
	)
	if handle == nil {
		return nil, errors.New(C.GoString(&message[0]))
	}
	return &Client{handle: handle}, nil
}

func (c *Client) CreateLobby() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.handle == nil {
		return "", errors.New("EOS client is closed")
	}
	var lobbyID [128]C.char
	var message [1024]C.char
	if C.ajb_eos_create_lobby(c.handle, &lobbyID[0], C.uint32_t(len(lobbyID)), &message[0], C.uint32_t(len(message))) == 0 {
		return "", errors.New(C.GoString(&message[0]))
	}
	return C.GoString(&lobbyID[0]), nil
}

func (c *Client) JoinLobby(lobbyID string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.handle == nil {
		return "", errors.New("EOS client is closed")
	}
	id := C.CString(lobbyID)
	defer C.free(unsafe.Pointer(id))
	var ownerID [64]C.char
	var message [1024]C.char
	if C.ajb_eos_join_lobby(c.handle, id, &ownerID[0], C.uint32_t(len(ownerID)), &message[0], C.uint32_t(len(message))) == 0 {
		return "", errors.New(C.GoString(&message[0]))
	}
	return C.GoString(&ownerID[0]), nil
}

func (c *Client) Send(ctx context.Context, peerID string, packet []byte) error {
	if len(packet) > maxEOSPacketSize {
		return fmt.Errorf("EOS packet is %d bytes; maximum is %d", len(packet), maxEOSPacketSize)
	}
	if len(packet) == 0 {
		return errors.New("EOS transport does not send empty packets")
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.handle == nil {
		return errors.New("EOS client is closed")
	}
	id := C.CString(peerID)
	defer C.free(unsafe.Pointer(id))
	var message [1024]C.char
	result := C.ajb_eos_send(
		c.handle,
		id,
		unsafe.Pointer(&packet[0]),
		C.uint32_t(len(packet)),
		&message[0],
		C.uint32_t(len(message)),
	)
	runtime.KeepAlive(packet)
	if result == 0 {
		return errors.New(C.GoString(&message[0]))
	}
	return nil
}

func (c *Client) Receive(ctx context.Context) (string, []byte, error) {
	for {
		if err := ctx.Err(); err != nil {
			return "", nil, err
		}

		c.mu.Lock()
		if c.handle == nil {
			c.mu.Unlock()
			return "", nil, errors.New("EOS client is closed")
		}
		var peerID [64]C.char
		packet := make([]byte, maxEOSPacketSize)
		var packetSize C.uint32_t
		var message [1024]C.char
		result := C.ajb_eos_receive(
			c.handle,
			&peerID[0],
			C.uint32_t(len(peerID)),
			unsafe.Pointer(&packet[0]),
			C.uint32_t(len(packet)),
			&packetSize,
			50,
			&message[0],
			C.uint32_t(len(message)),
		)
		c.mu.Unlock()
		runtime.KeepAlive(packet)

		switch result {
		case 0:
			continue
		case 1:
			return C.GoString(&peerID[0]), packet[:int(packetSize)], nil
		default:
			return "", nil, errors.New(C.GoString(&message[0]))
		}
	}
}

func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.handle != nil {
		C.ajb_eos_close(c.handle)
		c.handle = nil
	}
}
