package tapes

// Package tapes implements the HDM binary regression-tape serialization
// (Document 7). A tape stores one recorded production transaction as an
// absolute, unpadded, big-endian byte sequence that the Adversarial Critic
// replays bit-for-bit inside a shadow sandbox to prove a mutated cell does not
// diverge from verified historical behavior.

import (
	"encoding/binary"
	"fmt"
)

// Binary block layout (big-endian, unpadded):
//
//	0x00..0x03  MAGIC_MARKER          uint32  file-type signature "HDMT"
//	0x04..0x0B  MONADIC_TIMESTAMP_NS  uint64  timestamp bound to the guest clock intercept
//	0x0C..0x1B  ENTROPY_SEED_RESERVE  [16]byte pre-seeds the guest random pool
//	0x1C..0x1F  INPUT_PAYLOAD_SIZE    uint32  length of the trailing payload
//	0x20..0x3F  EXPECTED_OUTPUT_HASH  [32]byte SHA-256 of the expected result
//	0x40..0x5F  EXPECTED_STATE_HASH   [32]byte SHA-256 of the expected state delta
//	0x60..      INPUT_PAYLOAD         []byte   variable-length input packet
const (
	// MagicMarker is the 32-bit signature: ASCII "HDMT".
	MagicMarker uint32 = 0x48444D54
	// HeaderSize is the fixed byte length preceding the payload.
	HeaderSize = 0x60

	offMagic     = 0x00
	offTimestamp = 0x04
	offEntropy   = 0x0C
	offInputSize = 0x1C
	offOutHash   = 0x20
	offStateHash = 0x40
)

// TransactionFrame is a single decoded regression-tape record.
type TransactionFrame struct {
	MonadicTimestampNS uint64
	EntropySeed        [16]byte
	ExpectedOutputHash [32]byte
	ExpectedStateHash  [32]byte
	InputPayload       []byte
}

// Marshal serializes the frame into its canonical binary block.
func (f *TransactionFrame) Marshal() []byte {
	buf := make([]byte, HeaderSize+len(f.InputPayload))
	binary.BigEndian.PutUint32(buf[offMagic:], MagicMarker)
	binary.BigEndian.PutUint64(buf[offTimestamp:], f.MonadicTimestampNS)
	copy(buf[offEntropy:offEntropy+16], f.EntropySeed[:])
	binary.BigEndian.PutUint32(buf[offInputSize:], uint32(len(f.InputPayload)))
	copy(buf[offOutHash:offOutHash+32], f.ExpectedOutputHash[:])
	copy(buf[offStateHash:offStateHash+32], f.ExpectedStateHash[:])
	copy(buf[HeaderSize:], f.InputPayload)
	return buf
}

// Unmarshal decodes a canonical binary block into a TransactionFrame,
// validating the magic marker and declared payload length.
func Unmarshal(data []byte) (*TransactionFrame, error) {
	if len(data) < HeaderSize {
		return nil, fmt.Errorf("tape too short: %d bytes, need at least %d", len(data), HeaderSize)
	}
	magic := binary.BigEndian.Uint32(data[offMagic:])
	if magic != MagicMarker {
		return nil, fmt.Errorf("bad magic marker: got 0x%08X, want 0x%08X", magic, MagicMarker)
	}
	size := binary.BigEndian.Uint32(data[offInputSize:])
	if int(size) != len(data)-HeaderSize {
		return nil, fmt.Errorf("payload size mismatch: header declares %d, block carries %d", size, len(data)-HeaderSize)
	}

	f := &TransactionFrame{
		MonadicTimestampNS: binary.BigEndian.Uint64(data[offTimestamp:]),
	}
	copy(f.EntropySeed[:], data[offEntropy:offEntropy+16])
	copy(f.ExpectedOutputHash[:], data[offOutHash:offOutHash+32])
	copy(f.ExpectedStateHash[:], data[offStateHash:offStateHash+32])
	f.InputPayload = append([]byte(nil), data[HeaderSize:]...)
	return f, nil
}
