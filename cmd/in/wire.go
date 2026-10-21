package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

const wireVersion = 2
const requestLimit = 32

// message carries browser requests and acknowledgements, never callback payloads.
type message struct {
	Version int    `json:"version"`
	Kind    string `json:"kind"`
	ID      string `json:"id,omitempty"`
	URL     string `json:"url,omitempty"`
	Error   string `json:"error,omitempty"`
	Owners  string `json:"owners,omitempty"`
	Rclone  bool   `json:"rclone,omitempty"`
}

// wire frames bounded JSON messages and serializes writes from concurrent requests.
type wire struct {
	scanner *bufio.Scanner
	out     io.Writer
	mu      sync.Mutex
}

// newWire allocates only a bounded control-message buffer.
func newWire(in io.Reader, out io.Writer) *wire {
	s := bufio.NewScanner(in)
	s.Buffer(make([]byte, 4096), 128*1024)
	return &wire{scanner: s, out: out}
}

// receive validates the wire version without reflecting untrusted input in errors.
func (w *wire) receive() (message, error) {
	var m message
	if !w.scanner.Scan() {
		if w.scanner.Err() != nil {
			return m, fmt.Errorf("control message too large or unreadable")
		}
		return m, io.EOF
	}
	if json.Unmarshal(w.scanner.Bytes(), &m) != nil || m.Version != wireVersion {
		return m, fmt.Errorf("in versions are incompatible or control message is invalid")
	}
	return m, nil
}

// send writes a complete frame under a single lock.
func (w *wire) send(m message) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	m.Version = wireVersion
	return json.NewEncoder(w.out).Encode(m)
}

// boundedBuffer captures a small fixed diagnostic without allowing unbounded subprocess output.
type boundedBuffer struct{ bytes.Buffer }

// Write discards output beyond 4096 bytes while satisfying the subprocess writer contract.
func (b *boundedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := 4096 - b.Len()
	if len(p) > remaining {
		p = p[:remaining]
	}
	_, err := b.Buffer.Write(p)
	return n, err
}
