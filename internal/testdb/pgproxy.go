package testdb

// A fault-injecting PostgreSQL proxy for tests (R4-CTX). It forwards the wire
// protocol unchanged (the client must use sslmode=disable) and can, per armed
// connection, hold, drop or cut the few messages that decide what a client
// believes about its transaction: COMMIT and ROLLBACK, the CancelRequest a
// driver sends on a new connection, and the Terminate and FIN that end a
// connection.
//
// Every statement forwarded, and on each connection every CancelRequest,
// Terminate and FIN, is logged with the time and whether it was forwarded, held
// or dropped, so a test can say which of them reached the server. The package
// is test only: the import check keeps it out of production code.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fold/internal/txguard"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	cancelRequestCode = 80877102
	sslRequestCode    = 80877103
	gssRequestCode    = 80877104
)

// CommitFault is what the proxy does with a connection's COMMIT.
type CommitFault int

const (
	CommitPass             CommitFault = iota
	CommitHoldReply                    // (a) forward it, hold every reply until ReleaseReply
	CommitForwardThenClose             // (b) forward it, then close the client before the reply
	CommitCloseUnforwarded             // (c) close both sides without forwarding it
)

// RollbackFault is what the proxy does with a connection's ROLLBACK.
type RollbackFault int

const (
	RollbackPass             RollbackFault = iota
	RollbackCloseUnforwarded               // (g) close without forwarding
	RollbackForwardThenClose               // (g) forward, then close the client before the reply
)

// CancelFault is what the proxy does with a CancelRequest aimed at the
// connection's backend (matched by the process id the server sent).
type CancelFault int

const (
	CancelForward CancelFault = iota
	CancelHold                // (d) keep the cancel connection open, send nothing, until ReleaseCancel
	CancelDrop                // (e) close it without forwarding
)

// CloseFault is what the proxy does with the client's Terminate and with the
// close of the connection toward the server (the FIN).
type CloseFault int

const (
	CloseForward     CloseFault = iota
	CloseWithholdAll            // (f) forward neither Terminate nor FIN; the server side stays open until ReleaseClose
	CloseWithholdFIN            // (f2) forward Terminate; hold the FIN until ReleaseClose
)

// Faults is one armed behavior. Match is a case-insensitive piece of SQL: the
// first connection to send a statement containing it claims the fault (an
// empty Match claims on the next statement of any connection), and it applies
// to that connection only.
type Faults struct {
	Match    string
	Commit   CommitFault
	Rollback RollbackFault
	Cancel   CancelFault
	Close    CloseFault
}

// What happened to a logged message.
const (
	ActionForwarded = "forwarded"
	ActionHeld      = "held"
	ActionDropped   = "dropped"
)

// Event kinds.
const (
	KindStatement    = "statement" // a simple Query or an extended Parse
	KindCancel       = "cancel"
	KindTerminate    = "terminate"
	KindFIN          = "fin"      // the proxy closing its side toward the server
	KindServerClosed = "upstream" // the server closed the connection
	KindCommit       = "commit"   // a COMMIT the proxy recognized (also logged as a statement when forwarded)
	KindRollback     = "rollback"
)

// Event is one log line. Conn is the proxy's own connection number (0 for a
// cancel connection whose backend is unknown), PID the backend process id when
// known.
type Event struct {
	Conn   int
	PID    uint32
	Time   time.Time
	Kind   string
	SQL    string
	Action string
}

type gate struct {
	once sync.Once
	ch   chan struct{}
}

func newGate() *gate  { return &gate{ch: make(chan struct{})} }
func (g *gate) open() { g.once.Do(func() { close(g.ch) }) }

// Proxy is one listening proxy in front of the test server.
type Proxy struct {
	t        testing.TB
	ln       net.Listener
	upstream string

	mu      sync.Mutex
	events  []Event
	byPID   map[uint32]*proxyConn
	conns   []*proxyConn
	pending *Faults
	nextID  int

	// afterForward (under mu), when set by a test in this package, runs after
	// a transaction-ending message was sent to the server and before the
	// client is cut, so a test can let the server's reply arrive first.
	afterForward func(c *proxyConn)

	replyGate, cancelGate, closeGate *gate
	closed                           chan struct{}
	closeOnce                        sync.Once
	wg                               sync.WaitGroup
}

// NewProxy starts a proxy to the server DATABASE_URL names, on a loopback
// port, and stops it when the test ends.
func NewProxy(t testing.TB) *Proxy {
	t.Helper()
	base := os.Getenv("DATABASE_URL")
	if base == "" {
		t.Fatal("testdb: DATABASE_URL is not set; run `make test`, or start Postgres and set it")
	}
	u, err := url.Parse(base)
	if err != nil || u.Hostname() == "" {
		t.Fatalf("testdb: DATABASE_URL has no host the proxy can dial: %v", err)
	}
	port := u.Port()
	if port == "" {
		port = "5432"
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("testdb: proxy listen: %v", err)
	}
	p := &Proxy{
		t: t, ln: ln, upstream: net.JoinHostPort(u.Hostname(), port),
		byPID:     map[uint32]*proxyConn{},
		replyGate: newGate(), cancelGate: newGate(), closeGate: newGate(),
		closed: make(chan struct{}),
	}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.serve()
	}()
	t.Cleanup(p.Close)
	return p
}

func (p *Proxy) setAfterForward(f func(c *proxyConn)) {
	p.mu.Lock()
	p.afterForward = f
	p.mu.Unlock()
}

func (p *Proxy) afterForwardHook() func(c *proxyConn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.afterForward
}

// Addr is the host:port to connect to.
func (p *Proxy) Addr() string { return p.ln.Addr().String() }

// Open connects GORM to database name through the proxy, with the txguard
// plugin the server installs, and closes the handle when the test ends.
func (p *Proxy) Open(name string) (*gorm.DB, error) {
	base, err := URL(name)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	u.Host = p.Addr()
	q := u.Query()
	q.Set("sslmode", "disable")
	u.RawQuery = q.Encode()
	db, err := gorm.Open(postgres.Open(u.String()), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		return nil, err
	}
	if err := db.Use(txguard.Plugin{}); err != nil {
		return nil, err
	}
	if sqlDB, err := db.DB(); err == nil {
		p.t.Cleanup(func() { sqlDB.Close() })
	}
	return db, nil
}

// Arm sets the next fault. The first connection whose statement contains
// f.Match claims it; arming again before it is claimed replaces it.
func (p *Proxy) Arm(f Faults) {
	p.mu.Lock()
	p.pending = &f
	p.mu.Unlock()
}

// ReleaseReply lets every held reply through (and later ones).
func (p *Proxy) ReleaseReply() { p.replyGate.open() }

// ReleaseCancel forwards every held CancelRequest.
func (p *Proxy) ReleaseCancel() { p.cancelGate.open() }

// ReleaseClose ends every withheld Terminate and FIN: a held Terminate is
// forwarded, then the server side is closed.
func (p *Proxy) ReleaseClose() { p.closeGate.open() }

// Events returns a copy of the log in the order it was written.
func (p *Proxy) Events() []Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Event(nil), p.events...)
}

// Find returns the events of kind (and action, unless empty) in order.
func (p *Proxy) Find(kind, action string) []Event {
	var out []Event
	for _, e := range p.Events() {
		if e.Kind == kind && (action == "" || e.Action == action) {
			out = append(out, e)
		}
	}
	return out
}

// Close stops the listener and every connection. Nothing held outlives it: a
// held reply, cancel or close ends without being forwarded.
func (p *Proxy) Close() {
	p.closeOnce.Do(func() {
		close(p.closed)
		p.ln.Close()
		p.mu.Lock()
		conns := append([]*proxyConn(nil), p.conns...)
		p.mu.Unlock()
		for _, c := range conns {
			c.client.Close()
			c.upstream.Close()
		}
	})
	p.wg.Wait()
}

func (p *Proxy) record(e Event) {
	e.Time = time.Now()
	p.mu.Lock()
	p.events = append(p.events, e)
	p.mu.Unlock()
}

func (p *Proxy) serve() {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			p.handle(c)
		}()
	}
}

type proxyConn struct {
	p        *Proxy
	id       int
	client   net.Conn
	upstream net.Conn
	pid      atomic.Uint32

	mu             sync.Mutex
	faults         *Faults
	heldTerminate  []byte
	hasHeldTermin  bool
	closeOnSync    bool
	upstreamClosed bool

	holdReply       atomic.Bool
	closeAfterReady atomic.Bool
	clientClosed    atomic.Bool
	// suppress drops every server reply instead of forwarding it. It is set
	// before the transaction-ending message is sent upstream, so no reply to
	// it can reach the client first. suppressedReady counts the ReadyForQuery
	// messages dropped this way.
	suppress        atomic.Bool
	suppressedReady atomic.Int32

	// Extended-query state, kept by fromClient only: the SQL of each prepared
	// statement and the statement each portal was bound from.
	stmts   map[string]string
	portals map[string]string
}

func (c *proxyConn) f() Faults {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.faults == nil {
		return Faults{}
	}
	return *c.faults
}

func (c *proxyConn) event(kind, sql, action string) {
	c.p.record(Event{Conn: c.id, PID: c.pid.Load(), Kind: kind, SQL: sql, Action: action})
}

func readMsg(r io.Reader) (byte, []byte, error) {
	var h [5]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, nil, err
	}
	n := int(binary.BigEndian.Uint32(h[1:])) - 4
	if n < 0 || n > 1<<26 {
		return 0, nil, fmt.Errorf("bad message length %d", n+4)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return 0, nil, err
	}
	return h[0], body, nil
}

func writeMsg(w io.Writer, typ byte, body []byte) error {
	buf := make([]byte, 5+len(body))
	buf[0] = typ
	binary.BigEndian.PutUint32(buf[1:], uint32(len(body)+4))
	copy(buf[5:], body)
	_, err := w.Write(buf)
	return err
}

func cstring(b []byte) (string, []byte) {
	i := bytes.IndexByte(b, 0)
	if i < 0 {
		return string(b), nil
	}
	return string(b[:i]), b[i+1:]
}

func (p *Proxy) handle(client net.Conn) {
	defer client.Close()
	var hdr [8]byte
	for {
		if _, err := io.ReadFull(client, hdr[:]); err != nil {
			return
		}
		code := binary.BigEndian.Uint32(hdr[4:])
		if code == sslRequestCode || code == gssRequestCode {
			if _, err := client.Write([]byte{'N'}); err != nil {
				return
			}
			continue
		}
		break
	}
	length := binary.BigEndian.Uint32(hdr[:4])
	if binary.BigEndian.Uint32(hdr[4:]) == cancelRequestCode {
		p.handleCancel(client, hdr[:])
		return
	}
	if length < 8 || length > 1<<20 {
		return
	}
	rest := make([]byte, length-8)
	if _, err := io.ReadFull(client, rest); err != nil {
		return
	}
	up, err := net.Dial("tcp", p.upstream)
	if err != nil {
		return
	}
	defer up.Close()
	if _, err := up.Write(append(hdr[:], rest...)); err != nil {
		return
	}
	p.mu.Lock()
	p.nextID++
	c := &proxyConn{p: p, id: p.nextID, client: client, upstream: up,
		stmts: map[string]string{}, portals: map[string]string{}}
	p.conns = append(p.conns, c)
	p.mu.Unlock()

	done := make(chan struct{})
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer close(done)
		c.fromServer()
	}()
	c.fromClient()
	// The client side is over. Wait for the server side to end (it ends when
	// the server closes, or when closeUpstream's close lands).
	select {
	case <-done:
	case <-p.closed:
		up.Close()
		<-done
	}
}

func (p *Proxy) handleCancel(client net.Conn, hdr []byte) {
	rest := make([]byte, 8)
	if _, err := io.ReadFull(client, rest); err != nil {
		return
	}
	pid := binary.BigEndian.Uint32(rest[:4])
	p.mu.Lock()
	c := p.byPID[pid]
	p.mu.Unlock()
	mode, id := CancelForward, 0
	if c != nil {
		mode, id = c.f().Cancel, c.id
	}
	ev := Event{Conn: id, PID: pid, Kind: KindCancel}
	switch mode {
	case CancelDrop:
		ev.Action = ActionDropped
		p.record(ev)
		return
	case CancelHold:
		ev.Action = ActionHeld
		p.record(ev)
		select {
		case <-p.cancelGate.ch:
		case <-p.closed:
			return
		}
	}
	ev.Action = ActionForwarded
	p.record(ev)
	up, err := net.Dial("tcp", p.upstream)
	if err != nil {
		return
	}
	defer up.Close()
	if _, err := up.Write(append(append([]byte(nil), hdr...), rest...)); err != nil {
		return
	}
	// The server closes the cancel connection when it has acted on it.
	up.SetReadDeadline(time.Now().Add(5 * time.Second))
	io.Copy(io.Discard, up)
}

// claim binds the pending fault to c when sql matches.
func (c *proxyConn) claim(sql string) {
	p := c.p
	p.mu.Lock()
	defer p.mu.Unlock()
	if c.faults != nil || p.pending == nil {
		return
	}
	if !strings.Contains(strings.ToLower(sql), strings.ToLower(p.pending.Match)) {
		return
	}
	f := *p.pending
	p.pending = nil
	c.mu.Lock()
	c.faults = &f
	c.mu.Unlock()
}

func transactionEnd(sql string) string {
	s := strings.ToLower(strings.TrimSpace(strings.TrimRight(strings.TrimSpace(sql), ";")))
	switch s {
	case "commit":
		return KindCommit
	case "rollback":
		return KindRollback
	}
	return ""
}

// closeClient closes the client side without a Terminate from this end.
func (c *proxyConn) closeClient() {
	c.clientClosed.Store(true)
	c.client.Close()
}

// closeUpstream is the FIN toward the server, held when the fault says so.
func (c *proxyConn) closeUpstream() {
	c.mu.Lock()
	if c.upstreamClosed {
		c.mu.Unlock()
		return
	}
	c.upstreamClosed = true
	c.mu.Unlock()
	if mode := c.f().Close; mode == CloseWithholdAll || mode == CloseWithholdFIN {
		c.withholdUpstream()
		return
	}
	c.event(KindFIN, "", ActionForwarded)
	c.upstream.Close()
}

// withholdUpstream logs the FIN as held and starts the one goroutine that, on
// ReleaseClose, forwards a held Terminate and then closes the server side. It
// runs at most once per connection (callers set upstreamClosed first). A client
// that sent Terminate does not close its socket until the server closes (pgx
// drains it), so a held Terminate must arm this at once, not on client EOF.
func (c *proxyConn) withholdUpstream() {
	c.event(KindFIN, "", ActionHeld)
	c.p.wg.Add(1)
	go func() {
		defer c.p.wg.Done()
		select {
		case <-c.p.closeGate.ch:
			c.mu.Lock()
			held, term := c.heldTerminate, c.hasHeldTermin
			c.mu.Unlock()
			if term {
				c.event(KindTerminate, "", ActionForwarded)
				writeMsg(c.upstream, 'X', held)
			}
			c.event(KindFIN, "", ActionForwarded)
		case <-c.p.closed:
		}
		c.upstream.Close()
	}()
}

// dropBoth closes the client at once and the server side the way a FIN would.
func (c *proxyConn) dropBoth() {
	c.closeClient()
	c.closeUpstream()
}

func (c *proxyConn) fromClient() {
	for {
		typ, body, err := readMsg(c.client)
		if err != nil {
			c.clientClosed.Store(true)
			c.closeUpstream()
			return
		}
		// What the message asks the server to run, when it does: a simple
		// Query at once, a prepared statement only when a portal made from it
		// is executed (Execute), so preparing a COMMIT is not committing.
		sql, runs := "", false
		switch typ {
		case 'Q':
			sql, _ = cstring(body)
			runs = true
			c.claim(sql)
		case 'P':
			name, rest := cstring(body)
			sql, _ = cstring(rest)
			c.stmts[name] = sql
			c.claim(sql)
		case 'B':
			portal, rest := cstring(body)
			stmt, _ := cstring(rest)
			c.portals[portal] = stmt
		case 'C':
			if len(body) > 0 {
				name, _ := cstring(body[1:])
				switch body[0] {
				case 'S':
					delete(c.stmts, name)
				case 'P':
					delete(c.portals, name)
				}
			}
		case 'E':
			portal, _ := cstring(body)
			sql, runs = c.stmts[c.portals[portal]], true
			c.claim(sql)
		}
		f := c.f()
		switch typ {
		case 'X':
			if f.Close == CloseWithholdAll {
				c.event(KindTerminate, "", ActionHeld)
				c.mu.Lock()
				c.heldTerminate, c.hasHeldTermin = body, true
				already := c.upstreamClosed
				c.upstreamClosed = true
				c.mu.Unlock()
				if !already {
					c.withholdUpstream()
				}
				continue
			}
			c.event(KindTerminate, "", ActionForwarded)
			writeMsg(c.upstream, typ, body)
			continue
		case 'Q', 'E':
			if !runs {
				break
			}
			switch transactionEnd(sql) {
			case KindCommit:
				if c.endOfTransaction(typ, body, sql, KindCommit, int(f.Commit), int(CommitHoldReply), int(CommitForwardThenClose), int(CommitCloseUnforwarded)) {
					return
				}
				continue
			case KindRollback:
				if c.endOfTransaction(typ, body, sql, KindRollback, int(f.Rollback), -1, int(RollbackForwardThenClose), int(RollbackCloseUnforwarded)) {
					return
				}
				continue
			}
			c.event(KindStatement, sql, ActionForwarded)
		case 'S':
			if writeMsg(c.upstream, typ, body) != nil {
				return
			}
			c.mu.Lock()
			cut := c.closeOnSync
			c.mu.Unlock()
			if cut {
				if hook := c.p.afterForwardHook(); hook != nil {
					hook(c)
				}
				c.closeClient()
				return
			}
			continue
		}
		if writeMsg(c.upstream, typ, body) != nil {
			return
		}
	}
}

// endOfTransaction applies a COMMIT or ROLLBACK fault, for a simple Query or
// for the Execute of a portal. It reports whether the client loop should stop.
func (c *proxyConn) endOfTransaction(typ byte, body []byte, sql, kind string, mode, hold, thenClose, drop int) bool {
	switch {
	case mode == drop && mode != 0:
		c.event(KindStatement, sql, ActionDropped)
		c.event(kind, sql, ActionDropped)
		c.dropBoth()
		return true
	case mode == hold && mode != 0:
		c.holdReply.Store(true)
		c.event(KindStatement, sql, ActionForwarded)
		c.event(kind, sql, ActionHeld)
	case mode == thenClose && mode != 0:
		// Drop the server's replies from before the message is sent, so
		// none can reach the client ahead of the close; the server still
		// runs the statement and its replies are read and discarded.
		c.suppress.Store(true)
		c.closeAfterReady.Store(true)
		c.event(KindStatement, sql, ActionForwarded)
		c.event(kind, sql, ActionForwarded)
		if writeMsg(c.upstream, typ, body) != nil {
			return true
		}
		if typ == 'Q' {
			if hook := c.p.afterForwardHook(); hook != nil {
				hook(c)
			}
			c.closeClient()
			return true
		}
		// An Execute: the Sync after it still goes to the server, then the
		// client is cut.
		c.mu.Lock()
		c.closeOnSync = true
		c.mu.Unlock()
		return false
	default:
		c.event(KindStatement, sql, ActionForwarded)
		c.event(kind, sql, ActionForwarded)
	}
	return writeMsg(c.upstream, typ, body) != nil
}

func (c *proxyConn) fromServer() {
	for {
		typ, body, err := readMsg(c.upstream)
		if err != nil {
			c.event(KindServerClosed, "", ActionForwarded)
			// While replies are suppressed the client is cut by the
			// fromClient side, after the afterForward hook has run: closing
			// it here, microseconds after the dropped ReadyForQuery closed
			// the server side, would end the client's call before a test
			// could observe that the reply had reached the proxy first.
			// fromClient always cuts it (closeClient), and handle's deferred
			// close covers a failed write.
			if !c.suppress.Load() {
				c.client.Close()
			}
			return
		}
		if typ == 'K' && len(body) >= 8 {
			pid := binary.BigEndian.Uint32(body[:4])
			c.pid.Store(pid)
			c.p.mu.Lock()
			c.p.byPID[pid] = c
			c.p.mu.Unlock()
		}
		if c.holdReply.Load() {
			select {
			case <-c.p.replyGate.ch:
			case <-c.p.closed:
				return
			}
		}
		if c.suppress.Load() {
			if typ == 'Z' {
				c.suppressedReady.Add(1)
			}
		} else if !c.clientClosed.Load() {
			writeMsg(c.client, typ, body)
		}
		if typ == 'Z' && c.closeAfterReady.Load() {
			c.closeUpstream()
		}
	}
}
