package mcpconn_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/ZeroPointSix/metamcp-cli/internal/mcpconn"
	"github.com/ZeroPointSix/metamcp-cli/internal/ops"
	"github.com/ZeroPointSix/metamcp-cli/internal/output"
	"github.com/ZeroPointSix/metamcp-cli/test/fakeserver"
)

func newConn(t *testing.T, srv *fakeserver.Server, key string) *mcpconn.Conn {
	t.Helper()
	c := mcpconn.New(mcpconn.Options{URL: srv.URL(), APIKey: key})
	t.Cleanup(func() { c.Close() })
	return c
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return c
}

func TestSessionReuseAndDelete(t *testing.T) {
	srv := fakeserver.New("k")
	defer srv.Close()
	c := mcpconn.New(mcpconn.Options{URL: srv.URL(), APIKey: "k"})
	for i := 0; i < 3; i++ {
		if r := ops.Search(ctx(t), c, ops.SearchArgs{Query: "github"}); !r.OK {
			t.Fatalf("search: %+v", r.Error)
		}
	}
	if got := srv.Created.Load(); got != 1 {
		t.Errorf("sessions created = %d, want 1", got)
	}
	if st := c.Status(); !st.Connected || st.ProtocolVersion != mcpconn.ProtocolVersion {
		t.Errorf("status = %+v", st)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if srv.Deleted.Load() != 1 || srv.LiveSessions() != 0 {
		t.Errorf("deleted=%d live=%d, want 1/0", srv.Deleted.Load(), srv.LiveSessions())
	}
	// The pinned protocol must never send a session-less non-initialize POST
	// (server/discover), which would leak a server-side session on MetaMCP.
	if srv.Orphans.Load() != 0 {
		t.Errorf("orphan session-less POSTs = %d", srv.Orphans.Load())
	}
}

func TestSessionExpiryRebuildsOnce(t *testing.T) {
	srv := fakeserver.New("k")
	defer srv.Close()
	c := newConn(t, srv, "k")
	if r := ops.Execute(ctx(t), c, ops.ExecuteArgs{Code: "return 1;"}); !r.OK {
		t.Fatalf("execute: %+v", r.Error)
	}
	srv.ExpireSessions()

	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r := ops.Execute(ctx(t), c, ops.ExecuteArgs{Code: "return 1;"}); !r.OK {
				t.Errorf("execute after expiry: %+v", r.Error)
			}
		}()
	}
	wg.Wait()
	if got := srv.Created.Load(); got != 2 {
		t.Errorf("sessions created = %d, want 2 (one rebuild shared by concurrent calls)", got)
	}
	if got := srv.Executes.Load(); got != 7 {
		t.Errorf("executes = %d, want 7 (a 404 is rejected before the tool runs)", got)
	}
}

func TestAuthFailure(t *testing.T) {
	srv := fakeserver.New("right")
	defer srv.Close()
	c := newConn(t, srv, "wrong")
	r := ops.Search(ctx(t), c, ops.SearchArgs{Query: "x"})
	if r.OK || r.Error.Code != output.CodeAuth || r.ExitCode() != output.ExitAuth {
		t.Fatalf("got %+v", r.Error)
	}
}

func TestHTTPStatusClassification(t *testing.T) {
	cases := map[int]struct {
		code string
		exit int
	}{
		http.StatusTooManyRequests:     {output.CodeRateLimited, output.ExitLimit},
		http.StatusInternalServerError: {output.CodeServer, output.ExitUncertain},
		http.StatusForbidden:           {output.CodeAuth, output.ExitAuth},
	}
	for status, want := range cases {
		srv := fakeserver.New("k")
		c := newConn(t, srv, "k")
		// Establish the session first so the failure hits tools/call itself.
		if r := ops.Search(ctx(t), c, ops.SearchArgs{Query: "x"}); !r.OK {
			t.Fatal(r.Error)
		}
		srv.FailStatus.Store(int32(status))
		r := ops.Execute(ctx(t), c, ops.ExecuteArgs{Code: "return 1;"})
		if r.OK || r.Error.Code != want.code || r.ExitCode() != want.exit {
			t.Errorf("HTTP %d: got %+v", status, r.Error)
		}
		srv.Close()
	}
}

func TestEndpointNotFound(t *testing.T) {
	srv := fakeserver.New("k")
	defer srv.Close()
	c := mcpconn.New(mcpconn.Options{URL: srv.HTTP.URL + "/metamcp/missing/mcp", APIKey: "k"})
	defer c.Close()
	r := ops.Search(ctx(t), c, ops.SearchArgs{Query: "x"})
	if r.OK || r.Error.Code != output.CodeConfig {
		t.Fatalf("got %+v", r.Error)
	}
}

func TestTimeoutAndCancellation(t *testing.T) {
	srv := fakeserver.New("k")
	defer srv.Close()
	c := newConn(t, srv, "k")
	short, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	r := ops.Execute(short, c, ops.ExecuteArgs{Code: "__SLEEP_MS_5000__"})
	if r.OK || r.Error.Code != output.CodeTimeout || r.ExitCode() != output.ExitUncertain {
		t.Fatalf("got %+v", r.Error)
	}
	if r.Error.Hint != output.HintUncertain {
		t.Errorf("exit-4 hint missing: %q", r.Error.Hint)
	}
	// The client cancels the request; the server observes it.
	deadline := time.Now().Add(5 * time.Second)
	for srv.Cancelled.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if srv.Cancelled.Load() == 0 {
		t.Error("server never saw the cancellation")
	}
	// The session survives a timed-out call.
	if r := ops.Search(ctx(t), c, ops.SearchArgs{Query: "x"}); !r.OK {
		t.Fatalf("after timeout: %+v", r.Error)
	}
	if srv.Created.Load() != 1 {
		t.Errorf("created = %d, want 1", srv.Created.Load())
	}
}

func TestListToolNames(t *testing.T) {
	srv := fakeserver.New("k")
	defer srv.Close()
	names, err := newConn(t, srv, "k").ListToolNames(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 3 {
		t.Fatalf("names = %v", names)
	}
}

func TestUnreachable(t *testing.T) {
	c := mcpconn.New(mcpconn.Options{URL: "http://127.0.0.1:1/metamcp/x/mcp", APIKey: "k"})
	defer c.Close()
	r := ops.Search(ctx(t), c, ops.SearchArgs{Query: "x"})
	var e *output.Error
	if r.OK || !errors.As(r.Error, &e) || e.Code != output.CodeNetwork {
		t.Fatalf("got %+v", r.Error)
	}
}
