package egress

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"

	"golang.org/x/crypto/hkdf"
)

const (
	quicVersion1             = 0x00000001
	quicVersion2             = 0x6b3343cf
	maxQUICCrypto            = 64 << 10
	maxQUICBufferedDatagrams = 16
	maxQUICBufferedBytes     = 128 << 10
	maxQUICInspections       = 256
)

var (
	quicV1InitialSalt = []byte{0x38, 0x76, 0x2c, 0xf7, 0xf5, 0x59, 0x34, 0xb3, 0x4d, 0x17, 0x9a, 0xe6, 0xa4, 0xc8, 0x0c, 0xad, 0xcc, 0xbb, 0x7f, 0x0a}
	quicV2InitialSalt = []byte{0x0d, 0xed, 0xe3, 0xde, 0xf7, 0x00, 0xa6, 0xdb, 0x81, 0x93, 0x81, 0xbe, 0x6e, 0x26, 0x9d, 0xcb, 0xf9, 0xbd, 0x2e, 0xd9}
)

type quicCryptoFragment struct {
	offset uint64
	data   []byte
}

type quicInspection struct {
	largestPN     uint64
	havePN        bool
	crypto        []byte
	present       []bool
	buffered      [][]byte
	bufferedBytes int
	lastSeen      time.Time
}

func (q *quicInspection) add(packet []byte) (string, bool, error) {
	q.lastSeen = time.Now()
	if len(q.buffered) >= maxQUICBufferedDatagrams {
		return "", false, errors.New("QUIC ClientHello exceeded buffered datagram limit")
	}
	if len(packet) > maxQUICBufferedBytes-q.bufferedBytes {
		return "", false, errors.New("QUIC ClientHello exceeded buffered byte limit")
	}
	fragments, pn, err := decryptQUICInitial(packet, q.largestPN)
	if err != nil {
		return "", false, err
	}
	if !q.havePN || pn > q.largestPN {
		q.largestPN, q.havePN = pn, true
	}
	q.buffered = append(q.buffered, append([]byte(nil), packet...))
	q.bufferedBytes += len(packet)
	for _, fragment := range fragments {
		end := fragment.offset + uint64(len(fragment.data))
		if end > maxQUICCrypto {
			return "", false, errors.New("QUIC ClientHello exceeded crypto buffer limit")
		}
		if int(end) > len(q.crypto) {
			q.crypto = append(q.crypto, make([]byte, int(end)-len(q.crypto))...)
			q.present = append(q.present, make([]bool, int(end)-len(q.present))...)
		}
		copy(q.crypto[int(fragment.offset):int(end)], fragment.data)
		for i := int(fragment.offset); i < int(end); i++ {
			q.present[i] = true
		}
	}
	if len(q.crypto) < 4 || !allPresent(q.present[:4]) {
		return "", false, nil
	}
	if q.crypto[0] != 0x01 {
		return "", false, errors.New("QUIC CRYPTO stream does not start with ClientHello")
	}
	want := 4 + int(q.crypto[1])<<16 + int(q.crypto[2])<<8 + int(q.crypto[3])
	if want > maxQUICCrypto {
		return "", false, errors.New("QUIC ClientHello declared excessive length")
	}
	if len(q.crypto) < want || !allPresent(q.present[:want]) {
		return "", false, nil
	}
	host, ok := parseClientHelloHandshakeSNI(q.crypto[:want])
	if !ok {
		return "", false, errors.New("QUIC ClientHello has no valid SNI")
	}
	return normalizeHost(host), true, nil
}

func allPresent(p []bool) bool {
	for _, present := range p {
		if !present {
			return false
		}
	}
	return true
}

// decryptQUICInitial authenticates one client Initial packet and returns its
// CRYPTO fragments. It supports the standardized QUIC v1 and v2 salts. Other
// versions fail closed because their Initial key schedule is unknown here.
func decryptQUICInitial(packet []byte, largestPN uint64) ([]quicCryptoFragment, uint64, error) {
	if len(packet) < 7 || packet[0]&0xc0 != 0xc0 {
		return nil, 0, errors.New("not a QUIC long-header packet")
	}
	version := binary.BigEndian.Uint32(packet[1:5])
	var salt []byte
	var initialType byte
	labelPrefix := "quic"
	switch version {
	case quicVersion1:
		salt, initialType = quicV1InitialSalt, 0
	case quicVersion2:
		salt, initialType = quicV2InitialSalt, 1
		labelPrefix = "quicv2"
	default:
		return nil, 0, fmt.Errorf("unsupported QUIC version 0x%08x", version)
	}
	if (packet[0]>>4)&0x03 != initialType {
		return nil, 0, errors.New("QUIC long header is not an Initial")
	}

	p := 5
	dcidLen := int(packet[p])
	p++
	if dcidLen > 20 || len(packet) < p+dcidLen+1 {
		return nil, 0, errors.New("invalid QUIC destination connection ID")
	}
	dcid := packet[p : p+dcidLen]
	p += dcidLen
	scidLen := int(packet[p])
	p++
	if scidLen > 20 || len(packet) < p+scidLen {
		return nil, 0, errors.New("invalid QUIC source connection ID")
	}
	p += scidLen
	tokenLen, n, ok := readQUICVarint(packet[p:])
	if !ok || tokenLen > uint64(len(packet)) {
		return nil, 0, errors.New("invalid QUIC Initial token length")
	}
	p += n
	if tokenLen > uint64(len(packet)-p) {
		return nil, 0, errors.New("truncated QUIC Initial token")
	}
	p += int(tokenLen)
	protectedLen, n, ok := readQUICVarint(packet[p:])
	if !ok {
		return nil, 0, errors.New("invalid QUIC Initial payload length")
	}
	p += n
	pnOffset := p
	if protectedLen < 17 || protectedLen > uint64(len(packet)-pnOffset) || len(packet) < pnOffset+4+aes.BlockSize {
		return nil, 0, errors.New("truncated QUIC Initial payload")
	}

	key, iv, hp, err := quicInitialKeys(salt, dcid, labelPrefix)
	if err != nil {
		return nil, 0, err
	}
	hpBlock, err := aes.NewCipher(hp)
	if err != nil {
		return nil, 0, err
	}
	mask := make([]byte, aes.BlockSize)
	hpBlock.Encrypt(mask, packet[pnOffset+4:pnOffset+4+aes.BlockSize])
	first := packet[0] ^ (mask[0] & 0x0f)
	pnLen := int(first&0x03) + 1
	if protectedLen < uint64(pnLen+16) {
		return nil, 0, errors.New("invalid QUIC Initial packet number length")
	}
	header := append([]byte(nil), packet[:pnOffset+pnLen]...)
	header[0] = first
	var truncatedPN uint64
	for i := 0; i < pnLen; i++ {
		b := packet[pnOffset+i] ^ mask[i+1]
		header[pnOffset+i] = b
		truncatedPN = truncatedPN<<8 | uint64(b)
	}
	pn := decodeQUICPacketNumber(largestPN, truncatedPN, uint(pnLen*8))

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, 0, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, 0, err
	}
	nonce := append([]byte(nil), iv...)
	for i, v := len(nonce)-1, pn; i >= 0 && v != 0; i, v = i-1, v>>8 {
		nonce[i] ^= byte(v)
	}
	ciphertext := packet[pnOffset+pnLen : pnOffset+int(protectedLen)]
	plain, err := aead.Open(nil, nonce, ciphertext, header)
	if err != nil {
		return nil, 0, errors.New("QUIC Initial authentication failed")
	}
	fragments, err := parseQUICInitialFrames(plain)
	if err != nil {
		return nil, 0, err
	}
	return fragments, pn, nil
}

func quicInitialKeys(salt, dcid []byte, labelPrefix string) (key, iv, hp []byte, err error) {
	initial := hkdf.Extract(sha256.New, dcid, salt)
	client, err := quicHKDFExpandLabel(initial, "client in", 32)
	if err != nil {
		return nil, nil, nil, err
	}
	key, err = quicHKDFExpandLabel(client, labelPrefix+" key", 16)
	if err != nil {
		return nil, nil, nil, err
	}
	iv, err = quicHKDFExpandLabel(client, labelPrefix+" iv", 12)
	if err != nil {
		return nil, nil, nil, err
	}
	hp, err = quicHKDFExpandLabel(client, labelPrefix+" hp", 16)
	return key, iv, hp, err
}

func quicHKDFExpandLabel(secret []byte, label string, length int) ([]byte, error) {
	fullLabel := "tls13 " + label
	info := make([]byte, 0, 2+1+len(fullLabel)+1)
	info = binary.BigEndian.AppendUint16(info, uint16(length))
	info = append(info, byte(len(fullLabel)))
	info = append(info, fullLabel...)
	info = append(info, 0) // empty context
	out := make([]byte, length)
	_, err := io.ReadFull(hkdf.Expand(sha256.New, secret, info), out)
	return out, err
}

func readQUICVarint(p []byte) (uint64, int, bool) {
	if len(p) == 0 {
		return 0, 0, false
	}
	n := 1 << (p[0] >> 6)
	if len(p) < n {
		return 0, 0, false
	}
	v := uint64(p[0] & 0x3f)
	for i := 1; i < n; i++ {
		v = v<<8 | uint64(p[i])
	}
	return v, n, true
}

func decodeQUICPacketNumber(largest, truncated uint64, bits uint) uint64 {
	window := uint64(1) << bits
	half := window / 2
	mask := window - 1
	expected := largest + 1
	candidate := (expected &^ mask) | truncated
	if candidate+half <= expected && candidate <= ^uint64(0)-window {
		return candidate + window
	}
	if candidate > expected+half && candidate >= window {
		return candidate - window
	}
	return candidate
}

func parseQUICInitialFrames(p []byte) ([]quicCryptoFragment, error) {
	var out []quicCryptoFragment
	for len(p) > 0 {
		frameType, n, ok := readQUICVarint(p)
		if !ok {
			return nil, errors.New("invalid QUIC Initial frame type")
		}
		p = p[n:]
		switch frameType {
		case 0x00: // PADDING
			continue
		case 0x01: // PING
			continue
		case 0x02, 0x03: // ACK, ACK_ECN
			var ranges uint64
			for i := 0; i < 4; i++ {
				v, m, valid := readQUICVarint(p)
				if !valid {
					return nil, errors.New("truncated QUIC ACK frame")
				}
				p = p[m:]
				if i == 2 {
					ranges = v
				}
			}
			for ; ranges > 0; ranges-- {
				for j := 0; j < 2; j++ {
					_, m, valid := readQUICVarint(p)
					if !valid {
						return nil, errors.New("truncated QUIC ACK range")
					}
					p = p[m:]
				}
			}
			if frameType == 0x03 {
				for i := 0; i < 3; i++ {
					_, m, valid := readQUICVarint(p)
					if !valid {
						return nil, errors.New("truncated QUIC ACK ECN counts")
					}
					p = p[m:]
				}
			}
		case 0x06: // CRYPTO
			offset, m, valid := readQUICVarint(p)
			if !valid {
				return nil, errors.New("truncated QUIC CRYPTO offset")
			}
			p = p[m:]
			length, m, valid := readQUICVarint(p)
			if !valid || length > uint64(len(p)-m) {
				return nil, errors.New("truncated QUIC CRYPTO data")
			}
			p = p[m:]
			data := append([]byte(nil), p[:int(length)]...)
			p = p[int(length):]
			out = append(out, quicCryptoFragment{offset: offset, data: data})
		default:
			return nil, fmt.Errorf("unsupported frame 0x%x in QUIC Initial", frameType)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("QUIC Initial has no CRYPTO frame")
	}
	return out, nil
}
