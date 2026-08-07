// Copyright 2020 The Gitea Authors. All rights reserved.
// Copyright 2020 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package common

import (
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLineWriter(t *testing.T) {
	lines := make([]string, 0)
	lineHandler := func(s string) bool {
		lines = append(lines, s)
		return true
	}

	lineWriter := NewLineWriter(lineHandler)

	assert := assert.New(t)
	write := func(s string) {
		n, err := lineWriter.Write([]byte(s))
		assert.NoError(err) //nolint:testifylint // pre-existing issue from nektos/act
		assert.Equal(len(s), n, s)
	}

	write("hello")
	write(" ")
	write("world!!\nextra")
	write(" line\n and another\nlast")
	write(" line\n")
	write("no newline here...")

	assert.Len(lines, 4)
	assert.Equal("hello world!!\n", lines[0])
	assert.Equal("extra line\n", lines[1])
	assert.Equal(" and another\n", lines[2])
	assert.Equal("last line\n", lines[3])
}

func TestLineWriterFlush(t *testing.T) {
	lines := make([]string, 0)
	lineHandler := func(s string) bool {
		lines = append(lines, s)
		return true
	}

	lineWriter := NewLineWriter(lineHandler)

	assert := assert.New(t)
	_, err := lineWriter.Write([]byte("complete line\npartial line without newline"))
	assert.NoError(err) //nolint:testifylint // pre-existing pattern from nektos/act

	// Only the newline-terminated line is emitted before flushing.
	assert.Equal([]string{"complete line\n"}, lines)

	// Flushing emits the buffered, not-yet-terminated trailing line.
	FlushWriter(lineWriter)
	assert.Equal([]string{"complete line\n", "partial line without newline"}, lines)

	// Flushing again is a no-op: nothing is buffered.
	FlushWriter(lineWriter)
	assert.Len(lines, 2)
}

func TestFlushWriterIgnoresNonFlusher(t *testing.T) {
	// FlushWriter must be a safe no-op for writers that do not buffer lines.
	assert.NotPanics(t, func() { FlushWriter(io.Discard) })
}
