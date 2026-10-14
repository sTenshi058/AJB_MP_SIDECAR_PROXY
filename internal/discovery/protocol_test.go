package discovery

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"net"
	"strings"
	"testing"
)

func TestParseQueryValidatesSignatureAndLength(t *testing.T) {
	packet := append([]byte(nil), querySignature[:]...)
	packet = append(packet, []byte("query123")...)

	token, ok := ParseQuery(packet)
	if !ok || string(token[:]) != "query123" {
		t.Fatalf("ParseQuery(%x) = (%q, %v)", packet, token, ok)
	}

	for _, malformed := range [][]byte{
		packet[:len(packet)-1],
		append(append([]byte(nil), packet...), 0),
		append([]byte{0}, packet[1:]...),
	} {
		if _, ok := ParseQuery(malformed); ok {
			t.Fatalf("ParseQuery(%x) accepted malformed query", malformed)
		}
	}
}

func TestResponseMatchesCapturedPacketShape(t *testing.T) {
	name := strings.Repeat("N", 48)
	advertisement := Advertisement{
		Name:     name,
		Endpoint: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1170},
	}
	token := [8]byte{'q', 'u', 'e', 'r', 'y', '1', '2', '3'}
	response, err := advertisement.Response(token)
	if err != nil {
		t.Fatal(err)
	}
	if len(response) != 200 {
		t.Fatalf("response length = %d, want captured 200-byte shape", len(response))
	}
	if !bytes.Equal(response[:8], responseSignature[:]) {
		t.Fatalf("response signature = %x, want %x", response[:8], responseSignature)
	}
	if !bytes.Equal(response[8:16], token[:]) {
		t.Fatalf("response token = %q, want %q", response[8:16], token)
	}

	offset := 16
	for i := 0; i < 2; i++ {
		length := int(binary.BigEndian.Uint32(response[offset : offset+4]))
		offset += 4
		if length != len(name)+1 || string(response[offset:offset+length-1]) != name || response[offset+length-1] != 0 {
			t.Fatalf("serialized server name %d is invalid", i+1)
		}
		offset += length
	}

	if !bytes.Equal(response[offset:offset+len(responseFixedPrefix)], responseFixedPrefix[:]) {
		t.Fatalf("fixed response prefix = %x, want %x", response[offset:offset+8], responseFixedPrefix)
	}
	offset += len(responseFixedPrefix)
	sessionLength := int(binary.BigEndian.Uint32(response[offset : offset+4]))
	offset += 4
	if sessionLength != 32 {
		t.Fatalf("session ID length = %d, want 32", sessionLength)
	}
	if _, err := hex.DecodeString(string(response[offset : offset+sessionLength])); err != nil {
		t.Fatalf("session ID is not hexadecimal: %v", err)
	}
	offset += sessionLength
	if !bytes.Equal(response[offset:offset+4], net.IPv4(127, 0, 0, 1).To4()) {
		t.Fatalf("advertised IP = %x, want loopback", response[offset:offset+4])
	}
	offset += 4
	if port := binary.BigEndian.Uint32(response[offset : offset+4]); port != 1170 {
		t.Fatalf("advertised port = %d, want 1170", port)
	}
	offset += 4
	if settingLength := binary.BigEndian.Uint32(response[offset : offset+4]); settingLength != 20 {
		t.Fatalf("trailing settings length = %d, want 20", settingLength)
	}
	offset += 4
	if !bytes.Equal(response[offset:], responseFixedSuffix[:]) {
		t.Fatalf("fixed response suffix = %x, want %x", response[offset:], responseFixedSuffix)
	}
}

func TestNewAdvertisementRejectsInvalidNameAndEndpoint(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		endpoint *net.UDPAddr
	}{
		{name: ""},
		{name: "has\nnewline", endpoint: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1170}},
		{name: strings.Repeat("N", maxServerName+1), endpoint: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1170}},
		{name: "valid", endpoint: &net.UDPAddr{IP: net.IPv4(192, 168, 1, 10), Port: 1170}},
		{name: "valid", endpoint: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}},
	} {
		if _, err := NewAdvertisement(testCase.name, testCase.endpoint); err == nil {
			t.Fatalf("NewAdvertisement(%q, %v) unexpectedly succeeded", testCase.name, testCase.endpoint)
		}
	}
}
