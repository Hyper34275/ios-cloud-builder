package dev

import (
	"context"
	"io"
	"os"
	"sync"
)

// StdinMux multiplexes stdin with programmatic command injection.
// It allows sending commands to flutter attach while also forwarding user input.
type StdinMux struct {
	pr       *io.PipeReader
	pw       *io.PipeWriter
	cmdChan  chan byte
	done     chan struct{}
	doneOnce sync.Once
}

// NewStdinMux creates a new stdin multiplexer.
func NewStdinMux() *StdinMux {
	pr, pw := io.Pipe()
	return &StdinMux{
		pr:      pr,
		pw:      pw,
		cmdChan: make(chan byte, 10),
		done:    make(chan struct{}),
	}
}

// Reader returns the reader that should be used for cmd.Stdin.
func (m *StdinMux) Reader() io.Reader {
	return m.pr
}

// Start begins forwarding stdin and programmatic commands to the pipe.
// It blocks until Stop is called or context is cancelled.
func (m *StdinMux) Start(ctx context.Context) {
	go func() {
		buf := make([]byte, 1)
		for {
			select {
			case <-ctx.Done():
				return
			case <-m.done:
				return
			default:
			}

			n, err := os.Stdin.Read(buf)
			if err != nil {
				return
			}
			if n > 0 {
				if _, err := m.pw.Write(buf[:n]); err != nil {
					return // Pipe closed
				}
			}
		}
	}()

	// Forward programmatic commands
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.done:
			return
		case cmd := <-m.cmdChan:
			if _, err := m.pw.Write([]byte{cmd}); err != nil {
				return // Pipe closed
			}
		}
	}
}

// SendCommand injects a command byte into the stdin stream.
func (m *StdinMux) SendCommand(cmd byte) {
	select {
	case m.cmdChan <- cmd:
	default:
		// Channel full, drop command
	}
}

// Stop closes the multiplexer and returns any error from closing the pipe.
func (m *StdinMux) Stop() error {
	m.doneOnce.Do(func() {
		close(m.done)
	})
	return m.pw.Close()
}
