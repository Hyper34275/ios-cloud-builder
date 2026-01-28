package dev

import (
	"context"
	"io"
	"testing"
	"time"
)

func TestStdinMux_SendCommand(t *testing.T) {
	mux := NewStdinMux()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go mux.Start(ctx)

	// Send a command
	mux.SendCommand('r')

	// Read from the pipe
	buf := make([]byte, 1)
	done := make(chan struct{})

	go func() {
		n, err := mux.Reader().Read(buf)
		if err != nil {
			t.Errorf("Read() error = %v", err)
			return
		}
		if n != 1 || buf[0] != 'r' {
			t.Errorf("Read() = %v, %c; want 1, 'r'", n, buf[0])
		}
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(1 * time.Second):
		t.Error("timeout waiting for command")
	}

	if err := mux.Stop(); err != nil {
		t.Errorf("Stop() error = %v", err)
	}
}

func TestStdinMux_MultipleCommands(t *testing.T) {
	mux := NewStdinMux()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go mux.Start(ctx)

	// Send multiple commands
	mux.SendCommand('R')
	mux.SendCommand('r')
	mux.SendCommand('q')

	// Read all commands
	buf := make([]byte, 3)
	reader := mux.Reader()

	read := 0
	for read < 3 {
		n, err := reader.Read(buf[read:])
		if err != nil {
			t.Fatalf("Read() error = %v", err)
		}
		read += n
	}

	if string(buf) != "Rrq" {
		t.Errorf("Read() = %q, want %q", string(buf), "Rrq")
	}

	if err := mux.Stop(); err != nil {
		t.Errorf("Stop() error = %v", err)
	}
}

func TestStdinMux_Stop(t *testing.T) {
	mux := NewStdinMux()
	ctx, cancel := context.WithCancel(context.Background())

	started := make(chan struct{})
	stopped := make(chan struct{})

	go func() {
		close(started)
		mux.Start(ctx)
		close(stopped)
	}()

	<-started
	time.Sleep(10 * time.Millisecond) // Let Start() begin

	cancel()
	if err := mux.Stop(); err != nil {
		t.Errorf("Stop() error = %v", err)
	}

	// Reader should return EOF after stop
	select {
	case <-stopped:
		// Start returned
	case <-time.After(1 * time.Second):
		t.Error("timeout waiting for Start to return")
	}

	// Additional reads should fail
	buf := make([]byte, 1)
	_, err := mux.Reader().Read(buf)
	if err != io.EOF && err != io.ErrClosedPipe {
		t.Errorf("Read() error = %v, want EOF or ErrClosedPipe", err)
	}
}

func TestStdinMux_StopIdempotent(t *testing.T) {
	mux := NewStdinMux()

	// Calling Stop multiple times should not panic
	// First call closes the pipe, subsequent calls return io.ErrClosedPipe
	if err := mux.Stop(); err != nil {
		t.Errorf("first Stop() error = %v", err)
	}
	// Subsequent calls may return ErrClosedPipe, which is expected
	err := mux.Stop()
	if err != nil && err != io.ErrClosedPipe {
		t.Errorf("second Stop() error = %v, want nil or ErrClosedPipe", err)
	}
	err = mux.Stop()
	if err != nil && err != io.ErrClosedPipe {
		t.Errorf("third Stop() error = %v, want nil or ErrClosedPipe", err)
	}
}

func TestNewStdinMux(t *testing.T) {
	mux := NewStdinMux()

	if mux.pr == nil {
		t.Error("pr is nil")
	}
	if mux.pw == nil {
		t.Error("pw is nil")
	}
	if mux.cmdChan == nil {
		t.Error("cmdChan is nil")
	}
	if mux.done == nil {
		t.Error("done is nil")
	}
}
