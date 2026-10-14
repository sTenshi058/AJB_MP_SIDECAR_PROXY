package discovery

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"unicode/utf8"
)

const (
	queryPacketSize = 16
	maxServerName   = 128
)

var (
	querySignature    = [8]byte{0x0a, 0x01, 0x00, 0x00, 0x27, 0x0f, 'S', 'Q'}
	responseSignature = [8]byte{0x0a, 0x01, 0x00, 0x00, 0x27, 0x0f, 'S', 'R'}

	responseFixedPrefix = [...]byte{0, 0, 0, 0, 0, 0, 0, 0x13}
	responseFixedSuffix = [...]byte{
		0, 0, 0, 0, 1, 1, 0, 0, 1, 0, 1,
		1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	}
)

type Advertisement struct {
	Name     string
	Endpoint *net.UDPAddr
	session  [16]byte
}

func NewAdvertisement(name string, endpoint *net.UDPAddr) (Advertisement, error) {
	if err := validateAdvertisement(name, endpoint); err != nil {
		return Advertisement{}, err
	}

	advertisement := Advertisement{Name: name, Endpoint: cloneUDPAddr(endpoint)}
	if _, err := rand.Read(advertisement.session[:]); err != nil {
		return Advertisement{}, fmt.Errorf("generate discovery session ID: %w", err)
	}
	return advertisement, nil
}

func ParseQuery(packet []byte) ([8]byte, bool) {
	var token [8]byte
	if len(packet) != queryPacketSize || !equalBytes(packet[:len(querySignature)], querySignature[:]) {
		return token, false
	}
	copy(token[:], packet[len(querySignature):])
	return token, true
}

func (a Advertisement) Response(token [8]byte) ([]byte, error) {
	if err := validateAdvertisement(a.Name, a.Endpoint); err != nil {
		return nil, err
	}

	sessionID := strings.ToUpper(hex.EncodeToString(a.session[:]))
	payload := make([]byte, 0, 16+(4+len(a.Name)+1)*2+78)
	payload = append(payload, responseSignature[:]...)
	payload = append(payload, token[:]...)
	payload = appendString(payload, a.Name)
	payload = appendString(payload, a.Name)
	payload = append(payload, responseFixedPrefix[:]...)
	payload = binary.BigEndian.AppendUint32(payload, uint32(len(sessionID)))
	payload = append(payload, sessionID...)
	payload = append(payload, net.IPv4(127, 0, 0, 1).To4()...)
	payload = binary.BigEndian.AppendUint32(payload, uint32(a.Endpoint.Port))
	payload = binary.BigEndian.AppendUint32(payload, 20)
	payload = append(payload, responseFixedSuffix[:]...)
	return payload, nil
}

func validateAdvertisement(name string, endpoint *net.UDPAddr) error {
	if name == "" || len(name) > maxServerName || !utf8.ValidString(name) {
		return fmt.Errorf("discovery server name must contain 1 to %d UTF-8 bytes", maxServerName)
	}
	for _, char := range name {
		if char < 0x20 || char > 0x7e {
			return errors.New("discovery server name must contain printable ASCII characters only")
		}
	}
	if endpoint == nil {
		return errors.New("discovery endpoint cannot be nil")
	}
	if endpoint.IP == nil || !endpoint.IP.IsLoopback() || endpoint.IP.To4() == nil {
		return errors.New("discovery endpoint must use an IPv4 loopback address")
	}
	if endpoint.Port < 1 || endpoint.Port > 65535 {
		return errors.New("discovery endpoint port must be between 1 and 65535")
	}
	return nil
}

func appendString(destination []byte, value string) []byte {
	destination = binary.BigEndian.AppendUint32(destination, uint32(len(value)+1))
	destination = append(destination, value...)
	return append(destination, 0)
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
