package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
)

// callbackTransport opens one callback stream bound to the current terminal session.
type callbackTransport interface {
	Open(context.Context, endpoint, string) (callbackChannel, error)
}

// callbackChannel carries unchanged callback bytes and reports observed listener closure.
type callbackChannel interface {
	io.Reader
	io.Writer
	CloseWrite() error
	Close() error
	Wait() (bool, error)
}

// Open starts one SSH command on the selected authenticated master.
func (p peer) Open(ctx context.Context, ep endpoint, owners string) (callbackChannel, error) {
	args := []string{"pipe", ep.Host, ep.Port, owners}
	if p.Session != "" {
		args = append(args, p.Session)
	}
	cmd := p.sshCommand(ctx, args...)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		_ = in.Close()
		return nil, err
	}
	var diagnostic boundedBuffer
	cmd.Stderr = &diagnostic
	if err = cmd.Start(); err != nil {
		_ = in.Close()
		_ = out.Close()
		return nil, err
	}
	c := &sshCallback{cmd: cmd, input: in, output: out, reader: bufio.NewReaderSize(out, 4096), diagnostic: &diagnostic}
	return c, nil
}

// sshCallback owns the SSH process and both pipes until Wait or Close reaps it.
type sshCallback struct {
	cmd        *exec.Cmd
	input      io.WriteCloser
	output     io.ReadCloser
	reader     *bufio.Reader
	diagnostic *boundedBuffer
	once       sync.Once
	waitErr    error
	closed     bool
}

// Read returns remote callback bytes, including the initial readiness line.
func (c *sshCallback) Read(p []byte) (int, error) { return c.reader.Read(p) }

// Write sends local callback bytes to the remote helper.
func (c *sshCallback) Write(p []byte) (int, error) { return c.input.Write(p) }

// CloseWrite half-closes the SSH helper input after browser request EOF.
func (c *sshCallback) CloseWrite() error { return c.input.Close() }

// Close stops the helper and releases both process pipes.
func (c *sshCallback) Close() error {
	_ = c.input.Close()
	_ = c.output.Close()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	_, err := c.Wait()
	return err
}

// Wait reaps the helper once and reports its observed listener-close marker.
func (c *sshCallback) Wait() (bool, error) {
	c.once.Do(func() {
		c.waitErr = c.cmd.Wait()
		c.closed = bytes.Contains(c.diagnostic.Bytes(), []byte("IN_CALLBACK_CLOSED\n"))
	})
	return c.closed, c.waitErr
}

// readyChannel verifies that a remote listener is connected before forwarding any callback bytes.
func readyChannel(c callbackChannel) error {
	header := make([]byte, 6)
	if _, err := io.ReadFull(c, header); err != nil || string(header) != "READY\n" {
		return fmt.Errorf("remote listener unavailable")
	}
	return nil
}
