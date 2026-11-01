package loxone

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// Message identifiers of the 8-byte binary header that precedes every message.
const (
	msgText          = 0
	msgBinary        = 1
	msgValueEvents   = 2
	msgTextEvents    = 3
	msgDaytimer      = 4
	msgOutOfService  = 5
	msgKeepalive     = 6
	msgWeatherEvents = 7
)

const headerLen = 8

// header is the binary message header.
type header struct {
	ident     byte
	estimated bool
	length    uint32
}

func parseHeader(b []byte) (header, error) {
	if len(b) != headerLen || b[0] != 0x03 {
		return header{}, fmt.Errorf("not a message header (%d bytes)", len(b))
	}
	return header{
		ident:     b[1],
		estimated: b[2]&0x01 != 0,
		length:    binary.LittleEndian.Uint32(b[4:8]),
	}, nil
}

// hasPayload reports whether a message follows the header.
func (h header) hasPayload() bool {
	return h.ident != msgOutOfService && h.ident != msgKeepalive
}

// formatUUID turns the 16-byte binary UUID into the structure file's string form.
func formatUUID(b []byte) string {
	d1 := binary.LittleEndian.Uint32(b[0:4])
	d2 := binary.LittleEndian.Uint16(b[4:6])
	d3 := binary.LittleEndian.Uint16(b[6:8])
	return fmt.Sprintf("%08x-%04x-%04x-%02x%02x%02x%02x%02x%02x%02x%02x",
		d1, d2, d3, b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}

// ValueEvent is one entry of a value event table.
type ValueEvent struct {
	UUID  string
	Value float64
}

// TextEvent is one entry of a text event table.
type TextEvent struct {
	UUID string
	Text string
}

var errTruncated = errors.New("truncated event table")

func parseValueEvents(b []byte) ([]ValueEvent, error) {
	if len(b)%24 != 0 {
		return nil, fmt.Errorf("value event table of %d bytes: %w", len(b), errTruncated)
	}
	out := make([]ValueEvent, 0, len(b)/24)
	for off := 0; off < len(b); off += 24 {
		out = append(out, ValueEvent{
			UUID:  formatUUID(b[off : off+16]),
			Value: math.Float64frombits(binary.LittleEndian.Uint64(b[off+16 : off+24])),
		})
	}
	return out, nil
}

func parseTextEvents(b []byte) ([]TextEvent, error) {
	var out []TextEvent
	off := 0
	for off < len(b) {
		if len(b)-off < 36 {
			return out, errTruncated
		}
		uuid := formatUUID(b[off : off+16])
		n := int(binary.LittleEndian.Uint32(b[off+32 : off+36]))
		start := off + 36
		if n < 0 || n > len(b)-start {
			return out, errTruncated
		}
		out = append(out, TextEvent{UUID: uuid, Text: string(b[start : start+n])})
		off = start + n
		if pad := n % 4; pad != 0 {
			off += 4 - pad
		}
	}
	return out, nil
}
