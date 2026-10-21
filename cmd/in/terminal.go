package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const tunnelTimeout = 10 * time.Second

// terminalSession owns one ET session's private HTTP/2 server and callback streams.
type terminalSession struct {
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	closing bool
	wg      sync.WaitGroup
	agent   bool
	control *wire
	pending map[string]*pendingTunnel
	active  map[*httpCallback]bool
}

// pendingTunnel pairs two independent HTTP/2 streams under one random control ID.
type pendingTunnel struct {
	ctx      context.Context
	cancel   context.CancelFunc
	stream   chan *httpCallback
	up       *pipeHalf
	down     *pipeHalf
	callback *httpCallback
}

// pipeHalf owns one HTTP handler until its corresponding channel direction closes.
type pipeHalf struct {
	request  *http.Request
	response http.ResponseWriter
	done     chan struct{}
	once     sync.Once
}

// close ends exactly one HTTP/2 direction with native END_STREAM.
func (h *pipeHalf) close() { h.once.Do(func() { close(h.done) }) }

// httpCallback carries raw callback bytes over two independently closable streams.
type httpCallback struct {
	ctx         context.Context
	cancel      context.CancelFunc
	input       io.ReadCloser
	output      http.ResponseWriter
	request     *http.Request
	up          *pipeHalf
	down        *pipeHalf
	control     *wire
	id          string
	mu          sync.Mutex
	lifetime    sync.Mutex
	downAlive   bool
	closed      sync.Once
	complete    atomic.Bool
	writeClosed bool
}

// h2Protocols limits this private Unix service to the standard h2c implementation.
func h2Protocols() *http.Protocols {
	p := new(http.Protocols)
	p.SetUnencryptedHTTP2(true)
	return p
}

// flushWriter bounds control-frame writes and rejects writes after its handler exits.
type flushWriter struct {
	writer http.ResponseWriter
	mu     sync.Mutex
	closed bool
}

// signalWriter marks the first completed relay-ready frame without inspecting its contents.
type signalWriter struct {
	writer io.Writer
	once   sync.Once
	ready  chan struct{}
}

// Write forwards one frame and marks relay readiness after the write completes.
func (s *signalWriter) Write(p []byte) (int, error) {
	n, err := s.writer.Write(p)
	if err == nil {
		s.once.Do(func() { close(s.ready) })
	}
	return n, err
}

// Write flushes a frame within two seconds while the relay handler is alive.
func (f *flushWriter) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, io.ErrClosedPipe
	}
	controller := http.NewResponseController(f.writer)
	if err := controller.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
		f.closed = true
		return 0, err
	}
	n, err := f.writer.Write(p)
	if err == nil {
		err = controller.Flush()
	}
	_ = controller.SetWriteDeadline(time.Time{})
	if err != nil {
		f.closed = true
	}
	return n, err
}

// close waits for an in-flight control frame before the HTTP handler returns.
func (f *flushWriter) close() {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
}

// runTerminal adds one reverse Unix tunnel while ET retains its ordinary terminal interface.
func runTerminal(parent context.Context, args []string) error {
	for _, arg := range args {
		if arg == "--help" || arg == "-h" || arg == "--version" {
			return runPlainET(parent, args)
		}
	}
	root, err := stateDir()
	if err != nil {
		return err
	}
	if !terminalOptedIn(root, args) {
		return runPlainET(parent, args)
	}
	if !terminalSocketSupported(parent) {
		fmt.Fprintln(os.Stderr, "in: this ET client lacks session Unix socket forwarding; browser forwarding is unavailable")
		return runPlainET(parent, args)
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	gate, err := startupGate(ctx, root)
	if err != nil {
		return err
	}
	defer gate.Close()
	name := requestID()
	path := filepath.Join(root, "t-"+name+".sock")
	base := filepath.Join(root, "l-"+name)
	owner, err := lock(base + ".lock")
	if err != nil {
		return err
	}
	defer owner.Close()
	listener, err := socketListener(path)
	if err != nil {
		return err
	}
	defer func() { listener.Close(); _ = os.Remove(path) }()
	management, err := socketListener(base + ".sock")
	if err != nil {
		return err
	}
	defer func() { management.Close(); _ = os.Remove(base + ".sock") }()
	_ = gate.Close()
	session := &terminalSession{ctx: ctx, cancel: cancel, pending: make(map[string]*pendingTunnel), active: make(map[*httpCallback]bool)}
	server := &http.Server{ErrorLog: log.New(io.Discard, "", 0), Handler: session, Protocols: h2Protocols(), ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 4096}
	serveDone := make(chan struct{})
	go func() { defer close(serveDone); _ = server.Serve(listener) }()
	go session.manage(management)
	var cleanup sync.Once
	stopBridge := func() {
		cleanup.Do(func() {
			cancel()
			session.mu.Lock()
			session.closing = true
			session.mu.Unlock()
			_ = server.Close()
			_ = management.Close()
			<-serveDone
			session.closeChannels()
			session.wg.Wait()
			_ = os.Remove(path)
			_ = os.Remove(base + ".sock")
			_ = owner.Close()
		})
	}
	defer stopBridge()
	go func() { <-ctx.Done(); stopBridge() }()
	// ET creates the remote private socket and exports its path to the shell.
	commandArgs := append(append([]string{}, args...), "--reversetunnel", "IN_SESSION_SOCKET:"+path)
	cmd := exec.CommandContext(parent, "et", commandArgs...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	err = cmd.Wait()
	stopBridge()
	if err != nil {
		return err
	}
	return nil
}

// terminalSocketSupported checks the installed ET client before adding a tunnel option.
func terminalSocketSupported(parent context.Context) bool {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	version, err := exec.CommandContext(ctx, "et", "--version").Output()
	if err != nil {
		return false
	}
	var major int
	if _, err = fmt.Sscanf(strings.TrimSpace(string(version)), "et version %d", &major); err != nil || major < 7 {
		return false
	}
	help, err := exec.CommandContext(ctx, "et", "--help").Output()
	return err == nil && strings.Contains(string(help), "--reversetunnel")
}

// runPlainET delegates help and version requests without requiring tunnel support.
func runPlainET(ctx context.Context, args []string) error {
	cmd := exec.CommandContext(ctx, "et", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// manage accepts plaintext stop requests and ends only bridge resources.
func (s *terminalSession) manage(l net.Listener) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		_ = c.SetDeadline(time.Now().Add(time.Second))
		var buf [16]byte
		n, _ := c.Read(buf[:])
		if strings.TrimSpace(string(buf[:n])) == "stop" {
			s.cancel()
			_ = l.Close()
		}
		_ = c.Close()
	}
}

// ServeHTTP admits one remote agent and its bounded callback streams.
func (s *terminalSession) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		http.Error(w, "tunnel stopped", http.StatusServiceUnavailable)
		return
	}
	s.wg.Add(1)
	s.mu.Unlock()
	defer s.wg.Done()
	if r.ProtoMajor != 2 || r.Method != http.MethodPost || r.URL.RawQuery != "" {
		http.Error(w, "invalid tunnel request", http.StatusBadRequest)
		return
	}
	switch {
	case r.URL.Path == "/relay":
		s.serveRelay(w, r)
	case strings.HasPrefix(r.URL.Path, "/pipe/"):
		s.servePipe(w, r)
	default:
		http.NotFound(w, r)
	}
}

// serveRelay binds the single agent's control wire to the terminal lifetime.
func (s *terminalSession) serveRelay(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if s.agent || s.ctx.Err() != nil {
		s.mu.Unlock()
		http.Error(w, "agent unavailable", http.StatusConflict)
		return
	}
	s.agent = true
	controlOut := &flushWriter{writer: w}
	s.mu.Unlock()
	agentCtx, cancelAgent := context.WithCancel(s.ctx)
	stopAgent := context.AfterFunc(r.Context(), cancelAgent)
	defer func() {
		cancelAgent()
		stopAgent()
		s.mu.Lock()
		s.control = nil
		s.agent = false
		s.mu.Unlock()
		controlOut.close()
	}()
	controller := http.NewResponseController(w)
	if err := controller.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return
	}
	w.WriteHeader(http.StatusOK)
	if err := controller.Flush(); err != nil {
		return
	}
	_ = controller.SetWriteDeadline(time.Time{})
	control := newWire(r.Body, controlOut)
	s.mu.Lock()
	s.control = control
	s.mu.Unlock()
	ready, err := control.receive()
	if err != nil || ready.Kind != "ready" {
		return
	}
	_ = serveRequests(agentCtx, control, s)
}

// Open requests one remote callback and waits for its matching HTTP/2 stream.
func (s *terminalSession) Open(ctx context.Context, ep endpoint, owners string) (callbackChannel, error) {
	id := requestID()
	ch := make(chan *httpCallback, 1)
	streamCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	if s.control == nil || s.ctx.Err() != nil || len(s.pending)+len(s.active) >= requestLimit*8 {
		s.mu.Unlock()
		cancel()
		return nil, fmt.Errorf("callback tunnel unavailable or busy")
	}
	s.pending[id] = &pendingTunnel{ctx: streamCtx, cancel: cancel, stream: ch}
	control := s.control
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.pending, id); s.mu.Unlock() }()
	if err := control.send(message{Kind: "pipe", ID: id, Host: ep.Host, Port: ep.Port, Owners: owners}); err != nil {
		cancel()
		return nil, err
	}
	timer := time.NewTimer(tunnelTimeout)
	defer timer.Stop()
	select {
	case stream := <-ch:
		return stream, nil
	case <-ctx.Done():
		cancel()
		return nil, ctx.Err()
	case <-streamCtx.Done():
		return nil, fmt.Errorf("callback tunnel stream closed")
	case <-s.ctx.Done():
		cancel()
		return nil, fmt.Errorf("callback tunnel stopped")
	case <-timer.C:
		cancel()
		return nil, fmt.Errorf("remote callback listener unavailable")
	}
}

// servePipe pairs distinct upload and download handlers before exposing a callback channel.
func (s *terminalSession) servePipe(w http.ResponseWriter, r *http.Request) {
	tail := strings.TrimPrefix(r.URL.Path, "/pipe/")
	id, part, ok := strings.Cut(tail, "/")
	if len(id) != 32 || strings.Trim(id, "0123456789abcdef") != "" {
		http.NotFound(w, r)
		return
	}
	if !ok || (part != "up" && part != "down") {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	pending, found := s.pending[id]
	if !found || s.ctx.Err() != nil || pending.ctx.Err() != nil || (part == "up" && pending.up != nil) || (part == "down" && pending.down != nil) {
		s.mu.Unlock()
		http.Error(w, "unknown or duplicate callback", http.StatusConflict)
		return
	}
	half := &pipeHalf{request: r, response: w, done: make(chan struct{})}
	if part == "up" {
		pending.up = half
	} else {
		pending.down = half
	}
	if pending.up != nil && pending.down != nil {
		stream := &httpCallback{ctx: pending.ctx, cancel: pending.cancel, input: pending.up.request.Body, output: pending.down.response, request: pending.up.request, up: pending.up, down: pending.down, control: s.control, id: id, downAlive: true}
		pending.callback = stream
		s.active[stream] = true
	}
	s.mu.Unlock()
	defer func() {
		if part == "down" {
			s.mu.Lock()
			stream := pending.callback
			s.mu.Unlock()
			if stream != nil {
				if !half.doneClosed() {
					stream.interruptWrite()
				}
				stream.finishDownload()
			}
		}
		half.close()
		s.mu.Lock()
		if pending.callback != nil && pending.up != nil && pending.down != nil && pending.up.doneClosed() && pending.down.doneClosed() {
			delete(s.active, pending.callback)
		}
		s.mu.Unlock()
	}()
	controller := http.NewResponseController(w)
	if err := controller.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
		pending.cancel()
		return
	}
	w.WriteHeader(http.StatusOK)
	if err := controller.Flush(); err != nil {
		pending.cancel()
		return
	}
	_ = controller.SetWriteDeadline(time.Time{})
	s.mu.Lock()
	stream := pending.callback
	s.mu.Unlock()
	if stream != nil {
		select {
		case pending.stream <- stream:
		default:
		}
	}
	select {
	case <-half.done:
	case <-pending.ctx.Done():
	case <-s.ctx.Done():
	case <-r.Context().Done():
		pending.cancel()
	}
}

// doneClosed reports whether a handler's direction has ended.
func (h *pipeHalf) doneClosed() bool {
	select {
	case <-h.done:
		return true
	default:
		return false
	}
}

// closeChannels cancels pending and active streams before releasing the session lock.
func (s *terminalSession) closeChannels() {
	s.mu.Lock()
	var pending []*pendingTunnel
	for _, p := range s.pending {
		pending = append(pending, p)
	}
	var active []*httpCallback
	for c := range s.active {
		active = append(active, c)
	}
	s.mu.Unlock()
	for _, p := range pending {
		p.cancel()
	}
	for _, c := range active {
		_ = c.Close()
	}
}

// Read receives raw bytes from the remote callback listener.
func (c *httpCallback) Read(p []byte) (int, error) { return c.input.Read(p) }

// Write flushes raw browser bytes into the remote listener's response stream.
func (c *httpCallback) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.writeClosed {
		return 0, io.ErrClosedPipe
	}
	deadline, ok := c.ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(callbackLease)
	}
	controller := http.NewResponseController(c.output)
	c.lifetime.Lock()
	err := controller.SetWriteDeadline(deadline)
	c.lifetime.Unlock()
	if err != nil {
		return 0, err
	}
	n, err := c.output.Write(p)
	if err == nil {
		err = controller.Flush()
	}
	c.lifetime.Lock()
	_ = controller.SetWriteDeadline(time.Time{})
	c.lifetime.Unlock()
	return n, err
}

// CloseWrite ends the download response, letting the remote TCP input see EOF.
func (c *httpCallback) CloseWrite() error {
	c.mu.Lock()
	c.writeClosed = true
	c.down.close()
	c.mu.Unlock()
	return nil
}

// interruptWrite wakes a blocked data flush while the download handler is alive.
func (c *httpCallback) interruptWrite() {
	c.lifetime.Lock()
	if c.downAlive {
		_ = http.NewResponseController(c.output).SetWriteDeadline(time.Now())
	}
	c.lifetime.Unlock()
}

// finishDownload prevents later ResponseWriter calls before its handler returns.
func (c *httpCallback) finishDownload() {
	c.mu.Lock()
	c.writeClosed = true
	c.down.close()
	c.lifetime.Lock()
	c.downAlive = false
	c.lifetime.Unlock()
	c.mu.Unlock()
}

// Close ends both HTTP handlers and cancels unfinished remote work.
func (c *httpCallback) Close() error {
	c.closed.Do(func() {
		// Interrupt a stalled HTTP/2 data write before waiting for its mutex.
		completed := c.complete.Load()
		if !completed {
			c.interruptWrite()
		}
		_ = c.CloseWrite()
		c.cancel()
		_ = c.input.Close()
		c.up.close()
		if !completed && c.control != nil {
			_ = c.control.send(message{Kind: "pipe-cancel", ID: c.id})
		}
	})
	return nil
}

// Wait reports the remote listener-close observation after the request body reaches EOF.
func (c *httpCallback) Wait() (bool, error) {
	if c.ctx.Err() != nil {
		return false, c.ctx.Err()
	}
	c.complete.Store(true)
	return c.request.Trailer.Get("In-Callback-Closed") == "1", nil
}

// tunnelClient uses only the forwarded Unix socket and standard cleartext HTTP/2.
func tunnelClient(path string) *http.Client {
	transport := &http.Transport{Protocols: h2Protocols(), DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}
	return &http.Client{Transport: transport}
}

// validTunnelSocket accepts only a private Unix socket owned by this remote account.
func validTunnelSocket(path string) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("ET session socket is unavailable or not private")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("ET session socket belongs to another account")
	}
	parent, err := os.Lstat(filepath.Dir(path))
	if err != nil || !parent.IsDir() || parent.Mode()&os.ModeSymlink != 0 || parent.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("ET session socket parent is not private")
	}
	owner, ok := parent.Sys().(*syscall.Stat_t)
	if !ok || int(owner.Uid) != os.Getuid() {
		return fmt.Errorf("ET session socket parent belongs to another account")
	}
	return nil
}

// startTunnelRelay launches a session-owned remote agent and waits for its browser socket.
func startTunnelRelay(ctx context.Context, path string) error {
	if path == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("ET session socket is invalid")
	}
	if err := validTunnelSocket(path); err != nil {
		return err
	}
	root, err := stateDir()
	if err != nil {
		return err
	}
	gate, err := startupGate(ctx, root)
	if err != nil {
		return err
	}
	stamp, err := startupStamp(root)
	gate.Close()
	if err != nil {
		return err
	}
	checkStamp := func() error {
		current, err := startupStamp(root)
		if err != nil {
			return err
		}
		if current != stamp {
			return fmt.Errorf("ET browser relay startup was cancelled by stop")
		}
		return nil
	}
	base := filepath.Join(root, "r-"+digest("tunnel:"+path))
	if c, err := net.DialTimeout("unix", base+".sock", 100*time.Millisecond); err == nil {
		_ = c.Close()
		return checkStamp()
	}
	// Serialize detached startup until relay() owns its long-lived identity lock.
	var startup *os.File
	deadline := time.NewTimer(tunnelTimeout)
	defer deadline.Stop()
	for startup == nil {
		startup, err = lock(filepath.Join(root, "a-"+digest("tunnel:"+path)+".lock"))
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return fmt.Errorf("cannot coordinate ET relay startup")
		}
		if c, e := net.DialTimeout("unix", base+".sock", 100*time.Millisecond); e == nil {
			_ = c.Close()
			return checkStamp()
		}
		if err := checkStamp(); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("ET browser relay unavailable")
		case <-time.After(50 * time.Millisecond):
		}
	}
	defer startup.Close()
	if c, e := net.DialTimeout("unix", base+".sock", 100*time.Millisecond); e == nil {
		_ = c.Close()
		return checkStamp()
	}
	if err := checkStamp(); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer null.Close()
	cmd := exec.Command(exe, "relay-tunnel", path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = null, null, null
	cmd.Env = withStartupStampEnv(os.Environ(), stamp)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("cannot start ET browser relay")
	}
	_ = cmd.Process.Release()
	for {
		if c, e := net.DialTimeout("unix", base+".sock", 100*time.Millisecond); e == nil {
			_ = c.Close()
			return checkStamp()
		}
		if err := checkStamp(); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("ET browser relay unavailable; check the terminal session")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// runTunnelRelay keeps one remote browser relay connected to its ET session.
func runTunnelRelay(parent context.Context, path string) error {
	if path == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("ET session socket is invalid")
	}
	if err := validTunnelSocket(path); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	client := tunnelClient(path)
	defer client.CloseIdleConnections()
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://in/relay", reader)
	if err != nil {
		return err
	}
	readyCtx, readyCancel := context.WithTimeout(ctx, tunnelTimeout)
	stopReady := context.AfterFunc(readyCtx, func() { _ = reader.Close(); _ = writer.Close() })
	resp, err := client.Do(req)
	stopReady()
	readyCancel()
	if err != nil {
		return fmt.Errorf("ET callback tunnel unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ET callback tunnel refused agent")
	}
	stop := context.AfterFunc(ctx, func() { _ = writer.Close(); _ = reader.Close(); _ = resp.Body.Close() })
	defer stop()
	readySignal := &signalWriter{writer: writer, ready: make(chan struct{})}
	w := newWire(resp.Body, readySignal)
	readyTimer := time.AfterFunc(tunnelTimeout, cancel)
	defer readyTimer.Stop()
	go func() {
		select {
		case <-readySignal.ready:
			readyTimer.Stop()
		case <-ctx.Done():
		}
	}()
	slots := make(chan struct{}, requestLimit*8)
	var mu sync.Mutex
	closing := false
	workers := map[string]context.CancelFunc{}
	var wg sync.WaitGroup
	err = relay(ctx, "tunnel:"+path, w, func(m message) bool {
		switch m.Kind {
		case "pipe-cancel":
			mu.Lock()
			stop := workers[m.ID]
			mu.Unlock()
			if stop != nil {
				stop()
			}
			return true
		case "pipe":
			if len(m.ID) != 32 || strings.Trim(m.ID, "0123456789abcdef") != "" {
				return true
			}
			select {
			case slots <- struct{}{}:
			default:
				return true
			}
			mu.Lock()
			if closing {
				mu.Unlock()
				<-slots
				return true
			}
			if _, duplicate := workers[m.ID]; duplicate {
				mu.Unlock()
				<-slots
				return true
			}
			workCtx, workCancel := context.WithCancel(ctx)
			workers[m.ID] = workCancel
			wg.Add(1)
			mu.Unlock()
			go func() {
				defer wg.Done()
				defer func() { <-slots }()
				defer workCancel()
				defer func() { mu.Lock(); delete(workers, m.ID); mu.Unlock() }()
				remoteTunnelPipe(workCtx, client, m)
			}()
			return true
		}
		return false
	})
	cancel()
	_ = writer.Close()
	_ = reader.Close()
	_ = resp.Body.Close()
	mu.Lock()
	closing = true
	for _, stop := range workers {
		stop()
	}
	mu.Unlock()
	wg.Wait()
	return err
}

// remoteTunnelPipe uses two HTTP/2 streams so each TCP direction can end independently.
func remoteTunnelPipe(ctx context.Context, client *http.Client, m message) {
	if len(m.ID) != 32 || strings.Trim(m.ID, "0123456789abcdef") != "" {
		return
	}
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	upReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://in/pipe/"+m.ID+"/up", reader)
	if err != nil {
		return
	}
	upReq.Trailer = http.Header{"In-Callback-Closed": nil}
	downReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://in/pipe/"+m.ID+"/down", http.NoBody)
	if err != nil {
		return
	}
	type result struct {
		response *http.Response
		err      error
	}
	upDone := make(chan result, 1)
	go func() { r, e := client.Do(upReq); upDone <- result{r, e} }()
	downResp, err := client.Do(downReq)
	if err != nil {
		_ = writer.Close()
		<-upDone
		return
	}
	defer downResp.Body.Close()
	upResult := <-upDone
	if upResult.err != nil || upResult.response == nil {
		return
	}
	defer upResult.response.Body.Close()
	if downResp.StatusCode != http.StatusOK || upResult.response.StatusCode != http.StatusOK {
		return
	}
	stop := context.AfterFunc(ctx, func() { _ = downResp.Body.Close(); _ = writer.Close(); _ = reader.Close() })
	defer stop()
	closed, streamErr := callbackStream(ctx, m.Host, m.Port, m.Owners, downResp.Body, writer)
	if closed {
		upReq.Trailer.Set("In-Callback-Closed", "1")
	}
	if streamErr != nil {
		upReq.Trailer.Set("In-Callback-Closed", "0")
	}
	_ = writer.Close()
	_, _ = io.Copy(io.Discard, upResult.response.Body)
}

var _ callbackTransport = (*terminalSession)(nil)
var _ callbackChannel = (*httpCallback)(nil)
