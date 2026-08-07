// Copyright 2020 The Gitea Authors. All rights reserved.
// Copyright 2020 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package common

import (
	"bytes"
	"io"
)

// LineHandler is a callback function for handling a line
type LineHandler func(line string) bool

// Flusher is implemented by writers that buffer a trailing, not-yet-terminated
// line. Callers should flush once the underlying stream has reached EOF so the
// final line (when it is not newline-terminated) is not lost.
type Flusher interface {
	Flush()
}

type lineWriter struct {
	buffer   bytes.Buffer
	handlers []LineHandler
}

// NewLineWriter creates a new instance of a line writer
func NewLineWriter(handlers ...LineHandler) io.Writer {
	w := new(lineWriter)
	w.handlers = handlers
	return w
}

// FlushWriter flushes w if it implements Flusher. It is a no-op otherwise, so
// callers can flush an io.Writer without knowing its concrete type.
func FlushWriter(w io.Writer) {
	if f, ok := w.(Flusher); ok {
		f.Flush()
	}
}

func (lw *lineWriter) Write(p []byte) (n int, err error) {
	pBuf := bytes.NewBuffer(p)
	written := 0
	for {
		line, err := pBuf.ReadString('\n')
		w, _ := lw.buffer.WriteString(line)
		written += w
		if err == nil {
			lw.handleLine(lw.buffer.String())
			lw.buffer.Reset()
		} else if err == io.EOF {
			break
		} else {
			return written, err
		}
	}

	return written, nil
}

// Flush emits any buffered, not-yet-newline-terminated content as a final line.
// It is safe to call multiple times; subsequent calls with an empty buffer are
// no-ops.
func (lw *lineWriter) Flush() {
	if lw.buffer.Len() == 0 {
		return
	}
	lw.handleLine(lw.buffer.String())
	lw.buffer.Reset()
}

func (lw *lineWriter) handleLine(line string) {
	for _, h := range lw.handlers {
		ok := h(line)
		if !ok {
			break
		}
	}
}
