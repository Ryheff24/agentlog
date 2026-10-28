// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// agentlog: copied from Go's internal/zstd, keeping the tests that need
// neither the zstd and xxhsum commands nor Go's internal test helpers.

package zstd

import "testing"

var xxHashTests = []struct {
	data string
	hash uint64
}{
	{
		"hello, world",
		0xb33a384e6d1b1242,
	},
	{
		"abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789$",
		0x1032d841e824f998,
	},
}

func TestXXHash(t *testing.T) {
	var xh xxhash64
	for i, test := range xxHashTests {
		xh.reset()
		xh.update([]byte(test.data))
		if got := xh.digest(); got != test.hash {
			t.Errorf("#%d: got %#x want %#x", i, got, test.hash)
		}
	}
}
