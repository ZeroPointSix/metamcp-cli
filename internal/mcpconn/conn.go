// Package mcpconn wraps one MCP Streamable HTTP client session to a MetaMCP
// endpoint: API key injection, lazy initialize, transparent session rebuild on
// 404, and error classification into mcli error codes.
package mcpconn

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ZeroPointSix/metamcp-cli/internal/output"
	"github.com/ZeroPointSix/metamcp-cli/internal/version"
)

// ProtocolVersion is pinned to the newest legacy (stateful) revision.
//
// go-sdk defaults to 2026-07-28, which first probes with a stateless
// `server/discover` POST that carries no session id. MetaMCP treats every
// session-less POST as a new session, builds a server instance for it, and
// only frees it on DELETE, so each probe would leak a server-side session.
// MetaMCP's TypeScript SDK (1.29.0) supports up to 2025-11-25.
const ProtocolVersion = "2025-11-25"

// Facade tool names on a MetaMCP endpoint.
const (
	ToolSearch  = "search_tools"
	ToolExecute = "execute"
)

// Options configures a Conn.
type Options struct {
	URL    string
	APIKey string
	// HTTPClient overrides the base client (tests). Its Transport is wrapped.
	HTTPClient *http.Client
}

// Conn is safe for concurrent use. Concurrent calls share one MCP session.
type Conn struct {
	url    string
	http   *http.Client
	client *mcp.Client

	mu          sync.Mutex
	cur         *liveSession
	gen         uint64
	connectedAt time.Time
	rebuilds    int
	closed      bool
}

// liveSession tracks one MCP session and the calls in flight on it. A session
// that is replaced is only closed once its last in-flight call returns:
// closing it earlier would abort those calls with an error that no longer
// says why, so they could not be retried safely.
type liveSession struct {
	sess     *mcp.ClientSession
	gen      uint64
	inFlight int
	retired  bool

	done    chan struct{} // closed when the connection has terminated
	waitErr error         // why it terminated; valid after done is closed
}

// missing reports whether the server declared this session gone (HTTP 404),
// waiting up to d for a terminating connection to report its cause.
func (s *liveSession) missing(d time.Duration) bool {
	select {
	case <-s.done:
		return errors.Is(s.waitErr, mcp.ErrSessionMissing)
	case <-time.After(d):
		return false
	}
}

// New creates a Conn. No network traffic happens until the first call.
func New(opts Options) *Conn {
	base := opts.HTTPClient
	if base == nil {
		base = &http.Client{}
	}
	rt := base.Transport
	if rt == nil {
		rt = http.DefaultTransport
	}
	hc := *base
	hc.Transport = &authTransport{base: rt, apiKey: opts.APIKey, userAgent: version.UserAgent()}
	return &Conn{
		url:  opts.URL,
		http: &hc,
		client: mcp.NewClient(&mcp.Implementation{Name: "mcli", Version: version.Version}, &mcp.ClientOptions{
			// Advertise no optional client capabilities (roots, sampling, ...).
			Capabilities: &mcp.ClientCapabilities{},
		}),
	}
}

// acquire returns the live session with one more call registered on it,
// connecting lazily. Concurrent callers wait for a single initialize.
func (c *Conn) acquire(ctx context.Context) (*liveSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, output.New(output.CodeDaemon, "connection is shutting down")
	}
	if c.cur == nil {
		transport := &mcp.StreamableClientTransport{
			Endpoint:   c.url,
			HTTPClient: c.http,
			// mcli only needs request/response; skip the standing GET stream.
			DisableStandaloneSSE: true,
			// POSTs are never replayed by the SDK; this only affects SSE resumption.
			MaxRetries: -1,
		}
		rec := &statusRecorder{}
		sess, err := c.client.Connect(withRecorder(ctx, rec), transport, &mcp.ClientSessionOptions{ProtocolVersion: ProtocolVersion})
		if err != nil {
			return nil, classify(ctx, err, rec, true)
		}
		c.gen++
		c.cur = &liveSession{sess: sess, gen: c.gen, done: make(chan struct{})}
		c.connectedAt = time.Now()
		go c.watch(c.cur)
	}
	c.cur.inFlight++
	return c.cur, nil
}

// release ends one call on s, closing s if it was retired and this was the
// last call on it.
func (c *Conn) release(s *liveSession) {
	c.mu.Lock()
	s.inFlight--
	closeNow := s.retired && s.inFlight == 0
	c.mu.Unlock()
	if closeNow {
		go s.sess.Close() // the SDK skips DELETE for a session it knows is gone
	}
}

// retire stops handing out s so that the next call reconnects. Of several
// concurrent callers that saw the same 404, only the first rebuilds.
func (c *Conn) retire(s *liveSession) {
	c.mu.Lock()
	if c.cur == s {
		c.cur = nil
		c.rebuilds++
	}
	first := !s.retired
	s.retired = true
	closeNow := first && s.inFlight == 0
	c.mu.Unlock()
	if closeNow {
		go s.sess.Close()
	}
}

// watch retires a session once its connection terminates (for example after
// a 404 or a broken stream), so the next call reconnects instead of failing.
func (c *Conn) watch(s *liveSession) {
	s.waitErr = s.sess.Wait()
	close(s.done)
	c.retire(s)
}

// retryable reports whether a failed call never ran on the server: the
// session it used was gone (404). MetaMCP rejects requests on a missing
// session before executing them, so even execute can be retried. Any other
// failure is final: retrying a timed-out execute could repeat writes.
func retryable(ctx context.Context, s *liveSession, rec *statusRecorder, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	if st := rec.status.Load(); st != 0 && st != http.StatusNotFound {
		return false // rejected for another reason (auth, rate limit, 5xx)
	}
	if errors.Is(err, mcp.ErrSessionMissing) {
		return true
	}
	// A call that was in flight when another call hit the 404 fails with a
	// generic "connection closed" error; ask the session why it ended.
	return s.missing(250 * time.Millisecond)
}

// CallTool calls a tool, rebuilding the session and retrying once if the
// server reports the session as gone.
func (c *Conn) CallTool(ctx context.Context, name string, args any) (*mcp.CallToolResult, error) {
	for attempt := 0; ; attempt++ {
		s, err := c.acquire(ctx)
		if err != nil {
			return nil, err
		}
		rec := &statusRecorder{}
		res, err := s.sess.CallTool(withRecorder(ctx, rec), &mcp.CallToolParams{Name: name, Arguments: args})
		c.release(s)
		if err == nil {
			return res, nil
		}
		if retryable(ctx, s, rec, err) {
			c.retire(s)
			if attempt == 0 {
				continue
			}
		}
		return nil, classify(ctx, err, rec, false)
	}
}

// ListToolNames returns the tool names the endpoint advertises.
func (c *Conn) ListToolNames(ctx context.Context) ([]string, error) {
	for attempt := 0; ; attempt++ {
		s, err := c.acquire(ctx)
		if err != nil {
			return nil, err
		}
		rec := &statusRecorder{}
		var names []string
		var listErr error
		for tool, err := range s.sess.Tools(withRecorder(ctx, rec), nil) {
			if err != nil {
				listErr = err
				break
			}
			names = append(names, tool.Name)
		}
		c.release(s)
		if listErr == nil {
			return names, nil
		}
		if retryable(ctx, s, rec, listErr) {
			c.retire(s)
			if attempt == 0 {
				continue
			}
		}
		return nil, classify(ctx, listErr, rec, false)
	}
}

// Status describes the session for `mcli daemon status`.
type Status struct {
	Connected       bool      `json:"connected"`
	SessionID       string    `json:"session_id,omitempty"`
	ProtocolVersion string    `json:"protocol_version,omitempty"`
	ServerName      string    `json:"server_name,omitempty"`
	ServerVersion   string    `json:"server_version,omitempty"`
	ConnectedAt     time.Time `json:"connected_at,omitzero"`
	Rebuilds        int       `json:"rebuilds"`
}

// Status reports the current session state.
func (c *Conn) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := Status{Rebuilds: c.rebuilds}
	if c.cur != nil {
		sess := c.cur.sess
		st.Connected = true
		if id := sess.ID(); len(id) > 8 {
			st.SessionID = id[:8] + "…"
		}
		if ir := sess.InitializeResult(); ir != nil {
			st.ProtocolVersion = ir.ProtocolVersion
			if ir.ServerInfo != nil {
				st.ServerName, st.ServerVersion = ir.ServerInfo.Name, ir.ServerInfo.Version
			}
		}
		st.ConnectedAt = c.connectedAt
	}
	return st
}

// Close ends the session with an HTTP DELETE so the server frees it promptly.
// Calls still in flight are aborted.
func (c *Conn) Close() error {
	c.mu.Lock()
	s := c.cur
	c.cur = nil
	c.closed = true
	if s != nil {
		s.retired = true
	}
	c.mu.Unlock()
	if s == nil {
		return nil
	}
	return s.sess.Close()
}

// authTransport injects the API key and user agent and records HTTP status
// codes for error classification.
//
// X-API-Key is used rather than Authorization: Bearer because MetaMCP checks a
// bearer token as OAuth first when an endpoint enables both API key and OAuth.
type authTransport struct {
	base      http.RoundTripper
	apiKey    string
	userAgent string
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Header.Set("X-API-Key", t.apiKey)
	r.Header.Set("User-Agent", t.userAgent)
	resp, err := t.base.RoundTrip(r)
	if err == nil && resp.StatusCode >= 400 && r.Method == http.MethodPost {
		if rec := recorderFrom(req.Context()); rec != nil {
			rec.status.Store(int32(resp.StatusCode))
		}
	}
	return resp, err
}

type statusRecorder struct{ status atomic.Int32 }

type recorderKey struct{}

func withRecorder(ctx context.Context, rec *statusRecorder) context.Context {
	return context.WithValue(ctx, recorderKey{}, rec)
}

func recorderFrom(ctx context.Context) *statusRecorder {
	rec, _ := ctx.Value(recorderKey{}).(*statusRecorder)
	return rec
}
