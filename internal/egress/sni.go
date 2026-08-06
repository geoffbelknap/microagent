package egress

import "encoding/binary"

// parseClientHelloSNI extracts the SNI server_name from a TLS ClientHello
// record (including the 5-byte record header). Returns ("", false) when the
// bytes are not a ClientHello or carry no SNI. Never panics.
func parseClientHelloSNI(b []byte) (string, bool) {
	if len(b) < 5 || b[0] != 0x16 { // 0x16 = handshake record
		return "", false
	}
	rec := b[5:]
	recLen := int(binary.BigEndian.Uint16(b[3:5]))
	if recLen > len(rec) {
		return "", false
	}
	rec = rec[:recLen]
	return parseClientHelloHandshakeSNI(rec)
}

// parseClientHelloHandshakeSNI parses a TLS handshake message without a TLS
// record header. QUIC CRYPTO frames carry this form directly.
func parseClientHelloHandshakeSNI(rec []byte) (string, bool) {
	if len(rec) < 4 || rec[0] != 0x01 { // 0x01 = ClientHello
		return "", false
	}
	handshakeLen := int(rec[1])<<16 | int(rec[2])<<8 | int(rec[3])
	if handshakeLen > len(rec)-4 {
		return "", false
	}
	rec = rec[:4+handshakeLen]
	p := rec[4:]
	if len(p) < 34 { // client_version(2) + random(32)
		return "", false
	}
	p = p[34:]
	p, ok := skipVec8(p) // session_id
	if !ok {
		return "", false
	}
	p, ok = skipVec16(p) // cipher_suites
	if !ok {
		return "", false
	}
	p, ok = skipVec8(p) // compression_methods
	if !ok {
		return "", false
	}
	if len(p) < 2 { // extensions length
		return "", false
	}
	extLen := int(binary.BigEndian.Uint16(p[:2]))
	p = p[2:]
	if extLen > len(p) {
		return "", false
	}
	p = p[:extLen]
	for len(p) >= 4 {
		extType := binary.BigEndian.Uint16(p[:2])
		l := int(binary.BigEndian.Uint16(p[2:4]))
		p = p[4:]
		if len(p) < l {
			return "", false
		}
		ext := p[:l]
		p = p[l:]
		if extType != 0x0000 { // server_name
			continue
		}
		if len(ext) < 2 {
			return "", false
		}
		listLen := int(binary.BigEndian.Uint16(ext[:2]))
		if listLen != len(ext)-2 {
			return "", false
		}
		sn := ext[2:]
		for len(sn) >= 3 {
			nameType := sn[0]
			nlen := int(binary.BigEndian.Uint16(sn[1:3]))
			sn = sn[3:]
			if len(sn) < nlen {
				return "", false
			}
			name := sn[:nlen]
			sn = sn[nlen:]
			if nameType == 0 { // host_name
				return string(name), true
			}
		}
	}
	return "", false
}

func skipVec8(p []byte) ([]byte, bool) {
	if len(p) < 1 {
		return nil, false
	}
	n := int(p[0])
	p = p[1:]
	if len(p) < n {
		return nil, false
	}
	return p[n:], true
}

func skipVec16(p []byte) ([]byte, bool) {
	if len(p) < 2 {
		return nil, false
	}
	n := int(binary.BigEndian.Uint16(p[:2]))
	p = p[2:]
	if len(p) < n {
		return nil, false
	}
	return p[n:], true
}
