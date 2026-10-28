// Package zstdtest writes zstd for tests: a valid frame whose blocks are
// stored as they are, uncompressed, so a test can turn any fixture it builds
// into a compressed log without a compressor.
package zstdtest

import "encoding/binary"

// maxBlock is the largest block the format allows.
const maxBlock = 128 << 10

// Frame returns b as one zstd frame (RFC 8878): the magic number, a
// single-segment header declaring the content size, and raw blocks.
func Frame(b []byte) []byte {
	out := binary.LittleEndian.AppendUint32(nil, 0xFD2FB528)
	out = append(out, 0xE0) // 8-byte content size, single segment, no checksum
	out = binary.LittleEndian.AppendUint64(out, uint64(len(b)))
	for {
		n := min(len(b), maxBlock)
		header := uint32(n) << 3 // block type 0: raw
		if n == len(b) {
			header |= 1 // the last block
		}
		out = append(out, byte(header), byte(header>>8), byte(header>>16))
		out = append(out, b[:n]...)
		b = b[n:]
		if len(b) == 0 && header&1 == 1 {
			return out
		}
	}
}
