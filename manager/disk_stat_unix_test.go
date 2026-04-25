//go:build !windows

package manager

import (
	"math"
	"testing"
)

func TestMultiplyStatfsBytesRejectsTotalBytesOverflow(t *testing.T) {
	blockSize := uint64(4096)

	if _, ok := multiplyStatfsBytes(math.MaxUint64/blockSize+1, blockSize); ok {
		t.Fatal("multiplyStatfsBytes() ok = true for overflow, want false")
	}
}

func TestMultiplyStatfsBytesRejectsZeroBlockSize(t *testing.T) {
	if _, ok := multiplyStatfsBytes(1, 0); ok {
		t.Fatal("multiplyStatfsBytes() ok = true for zero block size, want false")
	}
}
