package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestStartupStampEnvReplacesInheritedValue gives a detached relay one launch generation.
func TestStartupStampEnvReplacesInheritedValue(t *testing.T) {
	env := withStartupStampEnv([]string{"A=1", "IN_START_STAMP=old", "IN_START_STAMP=older"}, "fresh")
	if strings.Join(env, "|") != "A=1|IN_START_STAMP=fresh" {
		t.Fatalf("relay inherited duplicate startup stamps: %v", env)
	}
}

// testTunnel serves the production HTTP/2 handler on a private Unix socket.
func testTunnel(t *testing.T) (*terminalSession, *http.Client) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s := &terminalSession{ctx: ctx, cancel: cancel, pending: map[string]*pendingTunnel{}, active: map[*httpCallback]bool{}}
	directory, err := os.MkdirTemp("/tmp", "in-h2-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	path := filepath.Join(directory, "t.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: s, Protocols: h2Protocols()}
	go func() { _ = server.Serve(l) }()
	client := tunnelClient(path)
	t.Cleanup(func() { cancel(); _ = server.Close(); client.CloseIdleConnections() })
	return s, client
}

// TestTunnelCallbackRoundTrip pins callback bytes and closure across separate loopback and Unix sockets.
func TestTunnelCallbackRoundTrip(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("callback ownership uses Linux socket tables")
	}
	s, client := testTunnel(t)
	remote, err := net.Listen("tcp4", "127.0.0.2:0")
	if err != nil {
		t.Skipf("alternate loopback unavailable: %v", err)
	}
	defer remote.Close()
	port := fmt.Sprint(remote.Addr().(*net.TCPAddr).Port)
	owners, err := callbackOwner(endpoint{Host: "127.0.0.2", Port: port})
	if err != nil {
		t.Fatal(err)
	}
	wantRequest := "GET /callback?code=abc123&state=unchanged HTTP/1.1\r\nHost: 127.0.0.2\r\n\r\n"
	wantResponse := "HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nhello"
	serverDone := make(chan error, 1)
	go func() {
		c, e := remote.Accept()
		if e != nil {
			serverDone <- e
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		b, e := io.ReadAll(c)
		if e == nil && string(b) != wantRequest {
			e = fmt.Errorf("callback request changed")
		}
		_ = remote.Close()
		if e == nil {
			_, e = io.WriteString(c, wantResponse)
		}
		serverDone <- e
	}()
	controlRead, controlWrite := io.Pipe()
	s.control = newWire(controlRead, controlWrite)
	// A remote agent receives only the framed pipe command and opens its own HTTP/2 stream.
	go func() {
		w := newWire(controlRead, io.Discard)
		m, e := w.receive()
		if e == nil {
			remoteTunnelPipe(s.ctx, client, m)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := s.Open(ctx, endpoint{Host: "127.0.0.2", Port: port}, owners)
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	if err := readyChannel(ch); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(ch, wantRequest); err != nil {
		t.Fatal(err)
	}
	if err := ch.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, len(wantResponse))
	if _, err := io.ReadFull(ch, response); err != nil {
		t.Fatal(err)
	}
	if string(response) != wantResponse {
		t.Fatalf("callback response changed: %q", response)
	}
	// The HTTP/2 request trailer is visible only after reading its body to EOF.
	_, _ = io.Copy(io.Discard, ch)
	closed, err := ch.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if !closed {
		t.Fatal("listener closure was not reported")
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

// TestTunnelSecondAgentRefused verifies the one-agent admission rule.
func TestTunnelSecondAgentRefused(t *testing.T) {
	_, client := testTunnel(t)
	reader, writer := io.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://in/relay", reader)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	defer writer.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first agent: %d", resp.StatusCode)
	}
	req2, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://in/relay", strings.NewReader(""))
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("second agent: %d", resp2.StatusCode)
	}
}

// TestTunnelPendingCancellation verifies an unclaimed stream releases its pending slot.
func TestTunnelPendingCancellation(t *testing.T) {
	s, _ := testTunnel(t)
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	s.control = newWire(reader, writer)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = s.Open(ctx, endpoint{Host: "127.0.0.1", Port: "34567"}, "socket:1@127.0.0.1")
	}()
	line, err := bufio.NewReader(reader).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, `"kind":"pipe"`) {
		t.Fatalf("unexpected control frame")
	}
	cancel()
	wg.Wait()
	s.mu.Lock()
	pending := len(s.pending)
	s.mu.Unlock()
	if pending != 0 {
		t.Fatalf("pending streams after cancellation: %d", pending)
	}
}

// TestTunnelActiveCancellation cancels one paired channel while its agent remains live.
func TestTunnelActiveCancellation(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("callback ownership uses Linux socket tables")
	}
	s, client := testTunnel(t)
	remote, err := net.Listen("tcp4", "127.0.0.2:0")
	if err != nil {
		t.Skipf("alternate loopback unavailable: %v", err)
	}
	defer remote.Close()
	port := fmt.Sprint(remote.Addr().(*net.TCPAddr).Port)
	owners, err := callbackOwner(endpoint{Host: "127.0.0.2", Port: port})
	if err != nil {
		t.Fatal(err)
	}
	listenerDone := make(chan struct{})
	go func() {
		c, e := remote.Accept()
		if e == nil {
			var buf [1]byte
			_, _ = c.Read(buf[:])
			_ = c.Close()
		}
		close(listenerDone)
	}()
	controlRead, controlWrite := io.Pipe()
	defer controlRead.Close()
	defer controlWrite.Close()
	s.control = newWire(controlRead, controlWrite)
	clientDone := make(chan struct{})
	canceled := make(chan struct{})
	workerDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		wire := newWire(controlRead, io.Discard)
		m, err := wire.receive()
		if err != nil {
			return
		}
		workerCtx, stop := context.WithCancel(s.ctx)
		defer stop()
		go func() { defer close(workerDone); remoteTunnelPipe(workerCtx, client, m) }()
		if cancelMessage, err := wire.receive(); err == nil && cancelMessage.Kind == "pipe-cancel" && cancelMessage.ID == m.ID {
			stop()
			close(canceled)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := s.Open(ctx, endpoint{Host: "127.0.0.2", Port: port}, owners)
	if err != nil {
		t.Fatal(err)
	}
	if err := readyChannel(ch); err != nil {
		t.Fatal(err)
	}
	_ = ch.Close()
	select {
	case <-canceled:
	case <-ctx.Done():
		t.Fatal("remote callback worker was not canceled")
	}
	<-clientDone
	select {
	case <-workerDone:
	case <-ctx.Done():
		t.Fatal("remote worker did not stop")
	}
	select {
	case <-listenerDone:
	case <-ctx.Done():
		t.Fatal("remote TCP connection did not close")
	}
	deadline := time.Now().Add(time.Second)
	for {
		s.mu.Lock()
		count := len(s.active)
		s.mu.Unlock()
		if count == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("active streams after close: %d", count)
		}
		time.Sleep(time.Millisecond)
	}
	s.mu.Lock()
	count := len(s.active)
	s.mu.Unlock()
	if count != 0 {
		t.Fatalf("active streams after close: %d", count)
	}
}

// TestTunnelPairingRejectsDuplicate verifies that two halves with the same direction cannot claim a stream.
func TestTunnelPairingRejectsDuplicate(t *testing.T) {
	s, client := testTunnel(t)
	read, write := io.Pipe()
	defer read.Close()
	defer write.Close()
	s.control = newWire(read, write)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := make(chan callbackChannel, 1)
	go func() {
		ch, _ := s.Open(ctx, endpoint{Host: "127.0.0.1", Port: "34567"}, "socket:1@127.0.0.1")
		result <- ch
	}()
	m, err := newWire(read, io.Discard).receive()
	if err != nil {
		t.Fatal(err)
	}
	first, err := client.Post("http://in/pipe/"+m.ID+"/up", "application/octet-stream", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Body.Close()
	duplicate, err := client.Post("http://in/pipe/"+m.ID+"/up", "application/octet-stream", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	defer duplicate.Body.Close()
	if duplicate.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate part: %d", duplicate.StatusCode)
	}
	down, err := client.Post("http://in/pipe/"+m.ID+"/down", "application/octet-stream", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	defer down.Body.Close()
	select {
	case ch := <-result:
		if ch == nil {
			t.Fatal("pair not admitted")
		}
		closed := make(chan struct{})
		go func() { _ = ch.Close(); close(closed) }()
		cancelMessage, err := newWire(read, io.Discard).receive()
		if err != nil || cancelMessage.Kind != "pipe-cancel" {
			t.Fatal("paired callback did not cancel")
		}
		select {
		case <-closed:
		case <-ctx.Done():
			t.Fatal("paired channel did not close")
		}
	case <-ctx.Done():
		t.Fatal("pair did not become ready")
	}
}

// TestTunnelSocketPrivacy rejects an arbitrary Unix socket outside an owned private directory.
func TestTunnelSocketPrivacy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sock")
	if err := os.Chmod(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := validTunnelSocket(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0777); err != nil {
		t.Fatal(err)
	}
	if err := validTunnelSocket(path); err == nil {
		t.Fatal("public socket accepted")
	}
}

// TestTunnelAgentReplacement verifies an EOF releases only the agent, allowing a new callback on the same ET session.
func TestTunnelAgentReplacement(t *testing.T) {
	s, client := testTunnel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	firstRead, firstWrite := io.Pipe()
	firstReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://in/relay", firstRead)
	first, err := client.Do(firstReq)
	if err != nil {
		t.Fatal(err)
	}
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first agent: %d", first.StatusCode)
	}
	if err := newWire(strings.NewReader(""), firstWrite).send(message{Kind: "ready"}); err != nil {
		t.Fatal(err)
	}
	_ = firstWrite.Close()
	_, _ = io.Copy(io.Discard, first.Body)
	_ = first.Body.Close()
	for {
		s.mu.Lock()
		active := s.agent
		s.mu.Unlock()
		if !active {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("first agent did not release session")
		case <-time.After(time.Millisecond):
		}
	}
	if s.ctx.Err() != nil {
		t.Fatal("ET parent session canceled after agent EOF")
	}
	secondRead, secondWrite := io.Pipe()
	defer secondWrite.Close()
	secondReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://in/relay", secondRead)
	second, err := client.Do(secondReq)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Body.Close()
	if second.StatusCode != http.StatusOK {
		t.Fatalf("replacement agent: %d", second.StatusCode)
	}
	if err := newWire(strings.NewReader(""), secondWrite).send(message{Kind: "ready"}); err != nil {
		t.Fatal(err)
	}
	opened := make(chan callbackChannel, 1)
	go func() {
		ch, _ := s.Open(ctx, endpoint{Host: "127.0.0.1", Port: "34567"}, "socket:1@127.0.0.1")
		opened <- ch
	}()
	pipe, err := newWire(second.Body, io.Discard).receive()
	if err != nil || pipe.Kind != "pipe" {
		t.Fatal("replacement agent did not receive callback request")
	}
	up, err := client.Post("http://in/pipe/"+pipe.ID+"/up", "application/octet-stream", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	defer up.Body.Close()
	down, err := client.Post("http://in/pipe/"+pipe.ID+"/down", "application/octet-stream", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	defer down.Body.Close()
	select {
	case ch := <-opened:
		if ch == nil {
			t.Fatal("replacement callback not paired")
		}
		_, _ = io.Copy(io.Discard, ch)
		_, _ = ch.Wait()
		_ = ch.CloseWrite()
		_ = ch.Close()
	case <-ctx.Done():
		t.Fatal("replacement callback timed out")
	}
}

// TestTunnelBackpressureCancellation stops one blocked stream while the control wire and next callback remain usable.
func TestTunnelBackpressureCancellation(t *testing.T) {
	s, client := testTunnel(t)
	client.Transport.(*http.Transport).HTTP2 = &http.HTTP2Config{MaxReceiveBufferPerStream: 64 << 10, MaxReceiveBufferPerConnection: 64 << 10}
	read, write := io.Pipe()
	defer read.Close()
	defer write.Close()
	s.control = newWire(read, write)
	frames := make(chan message, 4)
	go func() {
		w := newWire(read, io.Discard)
		for {
			m, err := w.receive()
			if err != nil {
				return
			}
			frames <- m
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	opened := make(chan callbackChannel, 1)
	go func() {
		ch, _ := s.Open(ctx, endpoint{Host: "127.0.0.1", Port: "34567"}, "socket:1@127.0.0.1")
		opened <- ch
	}()
	pipe := <-frames
	up, err := client.Post("http://in/pipe/"+pipe.ID+"/up", "application/octet-stream", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	defer up.Body.Close()
	down, err := client.Post("http://in/pipe/"+pipe.ID+"/down", "application/octet-stream", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	defer down.Body.Close()
	ch := <-opened
	if ch == nil {
		t.Fatal("callback not paired")
	}
	var sent atomic.Int64
	writeDone := make(chan error, 1)
	go func() {
		block := bytes.Repeat([]byte("x"), 32<<10)
		for i := 0; i < 2048; i++ {
			if _, err := ch.Write(block); err != nil {
				writeDone <- err
				return
			}
			sent.Add(int64(len(block)))
		}
		writeDone <- nil
	}()
	select {
	case err := <-writeDone:
		t.Fatalf("write completed without backpressure: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if sent.Load() == 0 {
		t.Fatal("no callback bytes reached HTTP/2")
	}
	closed := make(chan struct{})
	go func() { _ = ch.Close(); close(closed) }()
	select {
	case <-closed:
	case <-ctx.Done():
		t.Fatal("blocked callback did not close")
	}
	select {
	case <-writeDone:
	case <-ctx.Done():
		t.Fatal("blocked write did not unblock")
	}
	if m := <-frames; m.Kind != "pipe-cancel" || m.ID != pipe.ID {
		t.Fatal("canceled stream did not notify agent")
	}
	if s.ctx.Err() != nil {
		t.Fatal("session stopped with one callback")
	}
	nextCtx, nextCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer nextCancel()
	nextOpened := make(chan callbackChannel, 1)
	go func() {
		ch, _ := s.Open(nextCtx, endpoint{Host: "127.0.0.1", Port: "34568"}, "socket:1@127.0.0.1")
		nextOpened <- ch
	}()
	next := <-frames
	if next.Kind != "pipe" {
		t.Fatal("agent did not receive next callback")
	}
	up2, err := client.Post("http://in/pipe/"+next.ID+"/up", "application/octet-stream", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	defer up2.Body.Close()
	down2, err := client.Post("http://in/pipe/"+next.ID+"/down", "application/octet-stream", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	defer down2.Body.Close()
	ch2 := <-nextOpened
	if ch2 == nil {
		t.Fatal("next callback not paired")
	}
	if _, err := ch2.Write([]byte("alive")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(down2.Body, buf); err != nil || string(buf) != "alive" {
		t.Fatal("next callback bytes lost")
	}
	_, _ = io.Copy(io.Discard, ch2)
	_, _ = ch2.Wait()
	_ = ch2.CloseWrite()
	_ = ch2.Close()
}
