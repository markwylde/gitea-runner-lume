// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

package guestbootstrap

import (
	"bytes"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLimitedBufferBoundsConcurrentGuestOutput(t *testing.T) {
	buffer := &limitedBuffer{limit: 1024}
	value := bytes.Repeat([]byte("x"), 128)
	var group sync.WaitGroup
	for range 20 {
		group.Add(1)
		go func() {
			defer group.Done()
			written, err := buffer.Write(value)
			require.NoError(t, err)
			require.Equal(t, len(value), written)
		}()
	}
	group.Wait()
	require.Len(t, buffer.Bytes(), 1024)
}
