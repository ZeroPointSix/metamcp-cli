// Package daemon is the background process that holds one long-lived MCP
// session per (endpoint, API key) and serves CLI requests over local IPC.
//
// Agents often kill a CLI on timeout before it can send DELETE; the session
// lives here instead, and is closed (DELETE) when the daemon idles out.
package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gofrs/flock"

	"github.com/ZeroPointSix/metamcp-cli/internal/ipc"
	"github.com/ZeroPointSix/metamcp-cli/internal/mcpconn"
	"github.com/ZeroPointSix/metamcp-cli/internal/ops"
	"github.com/ZeroPointSix/metamcp-cli/internal/output"
	"github.com/ZeroPointSix/metamcp-cli/internal/version"
)

const (
	// DefaultIdle is how long the daemon lives without requests.
	DefaultIdle = 10 * time.Minute
	// MaxInFlight bounds concurrent search/execute calls on the session.
	MaxInFlight = 8
	// shutdownGrace bounds how long in-flight calls may finish on shutdown.
	shutdownGrace = 5 * time.Second
	readTimeout   = 10 * time.Second
)

// Config starts a daemon.
type Config struct {
	Endpoint string
	// RunLock is a file the daemon keeps locked while it runs, so that
	// `mcli daemon stop` can wait for it to exit.
	RunLock string
	URL     string
	APIKey  string
	Idle    time.Duration
	Logger  *log.Logger
}

// Status is returned by the status op.
type Status struct {
	ipc.Hello
	InFlight      int64          `json:"in_flight"`
	Served        int64          `json:"served"`
	IdleTimeout   string         `json:"idle_timeout"`
	IdleRemaining string         `json:"idle_remaining"`
	Session       mcpconn.Status `json:"session"`
}

type server struct {
	cfg     Config
	ln      ipc.Listener
	conn    *mcpconn.Conn
	log     *log.Logger
	hello   ipc.Hello
	slots   chan struct{}
	baseCtx context.Context
	cancel  context.CancelFunc

	inFlight atomic.Int64
	served   atomic.Int64
	lastUsed atomic.Int64 // unix nanos
	wg       sync.WaitGroup
	stopOnce sync.Once
	stopped  chan struct{}
}

// Run serves until idle timeout, a shutdown request or a signal.
func Run(cfg Config) error {
	if cfg.Idle <= 0 {
		cfg.Idle = DefaultIdle
	}
	if cfg.Logger == nil {
		cfg.Logger = log.New(os.Stderr, "", log.LstdFlags|log.Lmicroseconds)
	}
	if cfg.RunLock != "" {
		run := flock.New(cfg.RunLock)
		locked, err := run.TryLock()
		if err != nil {
			return err
		}
		if !locked {
			return errors.New("another daemon for this endpoint is running")
		}
		// Deferred first, so it is released last: after the session DELETE.
		defer run.Unlock()
	}
	ln, err := ipc.Listen(cfg.Endpoint)
	if err != nil {
		return err
	}
	baseCtx, cancel := context.WithCancel(context.Background())
	s := &server{
		cfg:     cfg,
		ln:      ln,
		conn:    mcpconn.New(mcpconn.Options{URL: cfg.URL, APIKey: cfg.APIKey}),
		log:     cfg.Logger,
		hello:   ipc.Hello{V: ipc.ProtocolVersion, Build: version.Build(), PID: os.Getpid(), StartedAt: time.Now().UTC()},
		slots:   make(chan struct{}, MaxInFlight),
		baseCtx: baseCtx,
		cancel:  cancel,
		stopped: make(chan struct{}),
	}
	s.touch()
	s.log.Printf("daemon started pid=%d build=%s endpoint=%s idle=%v", s.hello.PID, s.hello.Build, cfg.Endpoint, cfg.Idle)

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	signal.Ignore(syscall.SIGHUP)
	go func() {
		select {
		case sig := <-sigs:
			s.stop("signal " + sig.String())
		case <-s.stopped:
		}
	}()
	go s.idleLoop()

	for {
		c, err := ln.Accept()
		if err != nil {
			if ipc.IsPeerRejected(err) {
				s.log.Printf("rejected connection: %v", err)
				continue
			}
			select {
			case <-s.stopped:
			default:
				s.stop("accept error: " + err.Error())
			}
			break
		}
		s.wg.Add(1)
		go s.handle(c)
	}

	// Let in-flight calls finish briefly, then cancel them.
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(shutdownGrace):
		cancel()
		<-done
	}
	cancel()
	if err := s.conn.Close(); err != nil {
		s.log.Printf("closing MCP session: %v", err)
	}
	s.log.Printf("daemon exited (served %d requests)", s.served.Load())
	return nil
}

func (s *server) touch() { s.lastUsed.Store(time.Now().UnixNano()) }

func (s *server) idleRemaining() time.Duration {
	if s.inFlight.Load() > 0 {
		return s.cfg.Idle
	}
	return s.cfg.Idle - time.Since(time.Unix(0, s.lastUsed.Load()))
}

func (s *server) idleLoop() {
	tick := min(max(s.cfg.Idle/10, 20*time.Millisecond), 5*time.Second)
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-s.stopped:
			return
		case <-t.C:
			if s.idleRemaining() <= 0 {
				s.stop("idle timeout")
				return
			}
		}
	}
}

// stop unlinks the endpoint and stops accepting. Run then drains and closes
// the MCP session.
func (s *server) stop(reason string) {
	s.stopOnce.Do(func() {
		s.log.Printf("shutting down: %s", reason)
		close(s.stopped)
		_ = s.ln.Close()
	})
}

func (s *server) handle(c net.Conn) {
	defer s.wg.Done()
	defer c.Close()

	_ = c.SetReadDeadline(time.Now().Add(readTimeout))
	r := bufio.NewReader(c)
	var req ipc.Request
	if err := ipc.ReadMessage(r, &req); err != nil {
		return
	}
	_ = c.SetReadDeadline(time.Time{})

	reply := func(resp *ipc.Response) {
		resp.V = ipc.ProtocolVersion
		_ = ipc.WriteMessage(c, resp)
	}
	if req.V != ipc.ProtocolVersion && req.Op != ipc.OpHello && req.Op != ipc.OpShutdown {
		reply(&ipc.Response{Error: output.Newf(output.CodeDaemon, "IPC protocol %d not supported (daemon speaks %d)", req.V, ipc.ProtocolVersion)})
		return
	}

	switch req.Op {
	case ipc.OpHello:
		reply(okResponse(s.hello))
	case ipc.OpStatus:
		reply(okResponse(Status{
			Hello:         s.hello,
			InFlight:      s.inFlight.Load(),
			Served:        s.served.Load(),
			IdleTimeout:   s.cfg.Idle.String(),
			IdleRemaining: max(s.idleRemaining(), 0).Round(time.Second).String(),
			Session:       s.conn.Status(),
		}))
	case ipc.OpShutdown:
		reply(okResponse(map[string]bool{"stopping": true}))
		s.stop("shutdown requested")
	case ops.OpSearch, ops.OpExecute:
		s.serveOp(c, r, &req, reply)
	default:
		reply(&ipc.Response{Error: output.Newf(output.CodeUsage, "unknown op %q", req.Op)})
	}
}

func (s *server) serveOp(c net.Conn, r *bufio.Reader, req *ipc.Request, reply func(*ipc.Response)) {
	s.inFlight.Add(1)
	defer func() {
		s.inFlight.Add(-1)
		s.served.Add(1)
		s.touch()
	}()

	timeout := ops.Deadline(req.Op, req.TimeoutMS)
	ctx, cancel := context.WithTimeout(s.baseCtx, timeout)
	defer cancel()

	// If the CLI goes away (killed by an agent timeout), cancel the call; the
	// SDK forwards notifications/cancelled to the server.
	go func() {
		var b [1]byte
		_, _ = r.Read(b[:])
		cancel()
	}()

	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		reply(resultResponse(output.Fail(output.New(output.CodeCancelled, "cancelled while waiting for a free slot"))))
		return
	}

	start := time.Now()
	var res *output.Result
	switch req.Op {
	case ops.OpSearch:
		var args ops.SearchArgs
		if err := json.Unmarshal(req.Args, &args); err != nil {
			res = output.Fail(output.Newf(output.CodeUsage, "bad search args: %v", err))
			break
		}
		res = ops.Search(ctx, s.conn, args)
	case ops.OpExecute:
		var args ops.ExecuteArgs
		if err := json.Unmarshal(req.Args, &args); err != nil {
			res = output.Fail(output.Newf(output.CodeUsage, "bad execute args: %v", err))
			break
		}
		res = ops.Execute(ctx, s.conn, args)
	}
	code := ""
	if res.Error != nil {
		code = res.Error.Code
	}
	s.log.Printf("%s ok=%v code=%s duration=%v", req.Op, res.OK, code, time.Since(start).Round(time.Millisecond))
	reply(resultResponse(res))
}

func okResponse(v any) *ipc.Response {
	data, err := json.Marshal(v)
	if err != nil {
		return &ipc.Response{Error: output.New(output.CodeInternal, err.Error())}
	}
	return &ipc.Response{OK: true, Result: data}
}

func resultResponse(res *output.Result) *ipc.Response {
	data, err := json.Marshal(res)
	if err != nil {
		return &ipc.Response{Error: output.New(output.CodeInternal, err.Error())}
	}
	return &ipc.Response{OK: res.OK, Result: data, Error: res.Error}
}
