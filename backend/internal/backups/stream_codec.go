package backups

import (
	"bytes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
)

const (
	streamFormat = "sub-pool-pg-v2"
	streamHeader = "SUB-POOL-PG-BACKUP/2\n"
	fullScope    = "full"
	lightScope   = "light"

	frameSize        = 1 << 20
	framePrefixSize  = 1 + 8 + 4
	streamHeaderSize = len(streamHeader) + 1 + 32
	frameBody        = 1
	frameEnd         = 2
)

type streamFooter struct {
	Manifest   Manifest          `json:"manifest"`
	Migrations map[string]string `json:"migrations"`
	Sequences  []sequenceNext    `json:"sequences"`
	Pending    []string          `json:"pending"`
	BodySHA256 string            `json:"body_sha256"`
}

type frames struct {
	aead cipher.AEAD
	aad  [streamHeaderSize + framePrefixSize]byte
	next uint64
	data []byte
}

func newFrames(key, header []byte) (frames, error) {
	if len(key) != 32 {
		return frames{}, ErrKey
	}
	derived, err := hkdf.Key(sha256.New, key, header[len(streamHeader)+1:], "sub-pool:archive:v2", 32)
	if err != nil {
		return frames{}, err
	}
	defer clear(derived)
	aead, err := newAEAD(derived)
	if err != nil {
		return frames{}, err
	}
	f := frames{aead: aead, data: make([]byte, 0, frameSize+aead.Overhead())}
	copy(f.aad[:], header)
	return f, nil
}

func (f *frames) nonce() [12]byte {
	var nonce [12]byte
	binary.BigEndian.PutUint64(nonce[4:], f.next)
	return nonce
}

type streamEncoder struct {
	frames
	dest io.Writer
	body []byte
}

func newStreamEncoder(dest io.Writer, key []byte, scope string) (*streamEncoder, error) {
	var header [streamHeaderSize]byte
	copy(header[:], streamHeader)
	switch scope {
	case fullScope:
		header[len(streamHeader)] = 1
	case lightScope:
		header[len(streamHeader)] = 2
	default:
		return nil, ErrInvalid
	}
	if _, err := rand.Read(header[len(streamHeader)+1:]); err != nil {
		return nil, err
	}
	f, err := newFrames(key, header[:])
	if err != nil {
		return nil, err
	}
	if _, err := dest.Write(header[:]); err != nil {
		return nil, err
	}
	return &streamEncoder{frames: f, dest: dest, body: make([]byte, 0, frameSize)}, nil
}

func (e *streamEncoder) record(kind byte, data []byte) error {
	prefix := e.aad[streamHeaderSize:]
	prefix[0] = kind
	binary.BigEndian.PutUint64(prefix[1:], e.next)
	binary.BigEndian.PutUint32(prefix[9:], uint32(len(data)+e.aead.Overhead()))
	nonce := e.nonce()
	sealed := e.aead.Seal(e.data[:0], nonce[:], data, e.aad[:])
	if _, err := e.dest.Write(prefix); err != nil {
		return err
	}
	if _, err := e.dest.Write(sealed); err != nil {
		return err
	}
	e.next++
	return nil
}

func (e *streamEncoder) Write(p []byte) (int, error) {
	for n := 0; n < len(p); {
		size := min(frameSize-len(e.body), len(p)-n)
		e.body = append(e.body, p[n:n+size]...)
		n += size
		if len(e.body) == frameSize {
			if err := e.record(frameBody, e.body); err != nil {
				return n, err
			}
			e.body = e.body[:0]
		}
	}
	return len(p), nil
}

func (e *streamEncoder) finish(footer streamFooter) error {
	if len(e.body) > 0 {
		if err := e.record(frameBody, e.body); err != nil {
			return err
		}
	}
	body, err := json.Marshal(footer)
	if err != nil {
		return err
	}
	if len(body) > frameSize {
		return ErrLimit
	}
	return e.record(frameEnd, body)
}

type streamDecoder struct {
	frames
	source io.Reader
	scope  string
	body   []byte
	end    []byte
}

func readArchivePart(source io.Reader, into []byte) error {
	_, err := io.ReadFull(source, into)
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return ErrInvalid
	}
	return err
}

func newStreamDecoder(source io.Reader, key []byte) (*streamDecoder, error) {
	var header [streamHeaderSize]byte
	if err := readArchivePart(source, header[:]); err != nil {
		return nil, err
	}
	if string(header[:len(streamHeader)]) != streamHeader {
		return nil, ErrInvalid
	}
	var scope string
	switch header[len(streamHeader)] {
	case 1:
		scope = fullScope
	case 2:
		scope = lightScope
	default:
		return nil, ErrInvalid
	}
	f, err := newFrames(key, header[:])
	if err != nil {
		return nil, err
	}
	return &streamDecoder{frames: f, source: source, scope: scope}, nil
}

func (d *streamDecoder) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(d.body) == 0 {
		if d.end != nil {
			return 0, io.EOF
		}
		prefix := d.aad[streamHeaderSize:]
		if err := readArchivePart(d.source, prefix); err != nil {
			return 0, err
		}
		size := binary.BigEndian.Uint32(prefix[9:])
		kind := prefix[0]
		if binary.BigEndian.Uint64(prefix[1:]) != d.next || kind != frameBody && kind != frameEnd ||
			size <= uint32(d.aead.Overhead()) || size > uint32(cap(d.data)) {
			return 0, ErrInvalid
		}
		sealed := d.data[:size]
		if err := readArchivePart(d.source, sealed); err != nil {
			return 0, err
		}
		nonce := d.nonce()
		cleartext, err := d.aead.Open(sealed[:0], nonce[:], sealed, d.aad[:])
		if err != nil {
			return 0, ErrInvalid
		}
		d.next++
		if kind == frameEnd {
			d.end = cleartext
			return 0, io.EOF
		}
		d.body = cleartext
	}
	n := copy(p, d.body)
	d.body = d.body[n:]
	return n, nil
}

func (d *streamDecoder) finish() (streamFooter, error) {
	var footer streamFooter
	var extra [1]byte
	_, err := io.ReadFull(d, extra[:])
	if err != io.EOF {
		if err != nil {
			return footer, err
		}
		return footer, ErrInvalid
	}
	_, err = io.ReadFull(d.source, extra[:])
	if err != io.EOF {
		if err != nil {
			return footer, err
		}
		return footer, ErrInvalid
	}
	if json.Unmarshal(d.end, &footer) != nil {
		return footer, ErrInvalid
	}
	canonical, err := json.Marshal(footer)
	if err != nil || !bytes.Equal(canonical, d.end) {
		return footer, ErrInvalid
	}
	return footer, nil
}
