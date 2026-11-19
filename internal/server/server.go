package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/lockfile"
	"github.com/boldfield/syndic/internal/wire"
)

// Engine is the interface the server calls to handle requests.
type Engine interface {
	Hello() wire.HelloReply
	Status() (wire.StatusReply, error)
	Submit(wire.SubmitRequest) (wire.SubmitReply, error)
	Cancel(turnID string) error
	Override(turnID, reason string) error
	Release(wire.ReleaseRequest) (wire.ReleaseReply, error)
	Stopping() <-chan struct{}
}

// Options configures a Server.
type Options struct {
	Paths         lockfile.Paths
	Engine        Engine
	Store         *journal.Store
	Hub           *hub.Hub
	FrameObserver func(encodedLen int) // optional test hook
}

// Server serves the control socket protocol.
type Server struct {
	paths         lockfile.Paths
	engine        Engine
	store         *journal.Store
	hub           *hub.Hub
	frameObserver func(encodedLen int)

	mu       sync.Mutex
	listener net.Listener
	conns    map[*connection]bool
	wg       sync.WaitGroup
}

// New creates a new Server.
func New(o Options) *Server {
	return &Server{
		paths:         o.Paths,
		engine:        o.Engine,
		store:         o.Store,
		hub:           o.Hub,
		frameObserver: o.FrameObserver,
		conns:         make(map[*connection]bool),
	}
}

// Listen removes a stale socket, listens on the socket path, and sets mode 0600.
// It assumes the caller holds the engine lock.
func (s *Server) Listen() error {
	if err := lockfile.RemoveStaleSocket(s.paths); err != nil {
		return fmt.Errorf("remove stale socket: %w", err)
	}

	listener, err := net.Listen("unix", s.paths.Socket)
	if err != nil {
		return fmt.Errorf("listen on socket: %w", err)
	}

	if err := os.Chmod(s.paths.Socket, 0600); err != nil {
		_ = listener.Close()
		return fmt.Errorf("chmod socket: %w", err)
	}

	s.mu.Lock()
	s.listener = listener
	s.mu.Unlock()

	return nil
}

// Serve accepts connections and handles requests until ctx is done or the
// engine is stopping. Every open connection is sent error{code: "stopping"}
// when the engine stops, and is closed either way before Serve returns.
func (s *Server) Serve(ctx context.Context) error {
	s.mu.Lock()
	listener := s.listener
	s.mu.Unlock()
	if listener == nil {
		return errors.New("server not listening")
	}

	stopped := make(chan bool, 1)
	quit := make(chan struct{})
	defer close(quit)
	go func() {
		select {
		case <-s.engine.Stopping():
			stopped <- true
		case <-ctx.Done():
			stopped <- false
		case <-quit:
			return
		}
		_ = listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case engineStopped := <-stopped:
				if engineStopped {
					s.notifyStopping()
				}
				s.closeConnections()
				s.wg.Wait()
				return nil
			default:
			}
			s.closeConnections()
			s.wg.Wait()
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("accept connection: %w", err)
		}

		c := s.newConnection(conn)
		s.mu.Lock()
		s.conns[c] = true
		s.mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			c.handle()
		}()
	}
}

func (s *Server) snapshotConnections() []*connection {
	s.mu.Lock()
	defer s.mu.Unlock()
	conns := make([]*connection, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	return conns
}

// stoppingGrace bounds each step of shutdown for one client: being queued the
// events the journal holds, being written what is queued, and taking the
// stopping notice.
const stoppingGrace = time.Second

// notifyStopping tells every connection the engine is stopping and waits for
// the notice to be written or given up on. Subscribers are first given the
// events the journal holds (engine_stopping among them); the notice is the
// last frame a connection gets. Connections are handled together so clients
// that are not reading cost one grace in all.
func (s *Server) notifyStopping() {
	maxSeq, err := s.store.MaxSeq()
	if err != nil {
		fmt.Fprintf(os.Stderr, "server: stopping: %v\n", err)
		maxSeq = 0
	}
	var wg sync.WaitGroup
	for _, c := range s.snapshotConnections() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.stop(maxSeq)
		}()
	}
	wg.Wait()
}

func (s *Server) closeConnections() {
	for _, c := range s.snapshotConnections() {
		_ = c.conn.Close()
	}
}

// Close closes the listener and all connections.
func (s *Server) Close() error {
	s.mu.Lock()
	listener := s.listener
	s.listener = nil
	s.mu.Unlock()

	var err error
	if listener != nil {
		if closeErr := listener.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			err = closeErr
		}
	}
	s.closeConnections()
	return err
}

func (s *Server) removeConnection(c *connection) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
}

// maxOutbound bounds the events queued for a subscriber; past it the
// subscriber is lagging.
const maxOutbound = 256

// laggingGrace bounds the write of the lagging notice.
const laggingGrace = 2 * time.Second

// roomPoll is how often an event waiting for socket room looks again.
const roomPoll = 5 * time.Millisecond

// errDropped reports an event that was given up on before any of it was
// written.
var errDropped = errors.New("event dropped")

// connection handles one client connection. Every frame goes through one
// queue, written in order by the sender goroutine alone, which is the only
// writer of the socket.
type connection struct {
	server *Server
	conn   net.Conn
	reader *wire.Reader
	writer *wire.Writer
	// room reports whether n more bytes fit in the socket while leaving
	// room for a final notice; nil when the platform cannot tell.
	room func(n int) bool

	// gateSub is the subscription of the event being written, nil for any
	// other frame. Only the sender touches it.
	gateSub *hub.Subscription

	mu     sync.Mutex
	queue  []outFrame
	events int // events in queue
	// final is the last frame before the connection closes: stopping,
	// lagging, or a handshake or frame error. Nothing is queued once it is
	// set. Queued frames still go first, until dropAt; a zero dropAt drops
	// them at once.
	final  *finalFrame
	dropAt time.Time
	shut   bool // the connection is going away; the sender quits
	sub    *hub.Subscription
	// queuedSeq and writtenSeq are the last event seq of sub queued and
	// written.
	queuedSeq  int64
	writtenSeq int64
	wake       chan struct{}
	senderDone chan struct{}
}

type outFrame struct {
	frame wire.Frame
	sub   *hub.Subscription // set for an event
}

type finalFrame struct {
	id      string
	err     wire.Error
	lagging bool // last_seq is filled in with the last event written
}

// newConnection builds a connection with its reader and writer in place, so
// it is safe to publish to other goroutines (the stop notice) at once.
func (s *Server) newConnection(conn net.Conn) *connection {
	c := &connection{
		server:     s,
		conn:       conn,
		reader:     wire.NewReader(conn),
		room:       socketRoom(conn),
		wake:       make(chan struct{}, 1),
		senderDone: make(chan struct{}),
	}
	c.writer = wire.NewWriter(gateWriter{c})
	if s.frameObserver != nil {
		c.writer.SetObserver(s.frameObserver)
	}
	go c.send()
	return c
}

func (c *connection) signal() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// enqueue queues a reply or error frame, unless the connection is closing.
func (c *connection) enqueue(f wire.Frame) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.final != nil || c.shut {
		return
	}
	c.queue = append(c.queue, outFrame{frame: f})
	c.signal()
}

// enqueueEvent queues an event of sub. It reports false when sub should stop
// forwarding: it is no longer current, the connection is closing, or the
// subscriber has fallen too far behind and is now lagging.
func (c *connection) enqueueEvent(sub *hub.Subscription, ev journal.Event) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sub != sub || c.final != nil || c.shut {
		return false
	}
	if c.events >= maxOutbound {
		c.lagLocked()
		return false
	}
	c.queue = append(c.queue, outFrame{frame: wire.NewEvent(ev.Seq, ev.Kind, ev.At, ev.Data), sub: sub})
	c.events++
	c.queuedSeq = ev.Seq
	c.signal()
	return true
}

// lagging ends the connection with the lagging notice if sub is current.
func (c *connection) lagging(sub *hub.Subscription) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sub == sub {
		c.lagLocked()
	}
}

// lagLocked drops the backlog and makes lagging, with the last event the
// client was written, the next and last frame.
func (c *connection) lagLocked() {
	if c.final != nil {
		return
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(laggingGrace))
	c.final = &finalFrame{err: wire.Error{Code: wire.ErrorCodeLagging}, lagging: true}
	c.dropQueueLocked()
	c.signal()
}

// finish makes e the last frame. Frames already queued go first, for at most
// drain; writes are given up on after deadline.
func (c *connection) finish(id string, e wire.Error, drain, deadline time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.final != nil || c.shut {
		return
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(deadline))
	c.final = &finalFrame{id: id, err: e}
	if drain > 0 {
		c.dropAt = time.Now().Add(drain)
	} else {
		c.dropQueueLocked()
	}
	c.signal()
}

func (c *connection) dropQueueLocked() {
	c.queue = nil
	c.events = 0
}

// dropping reports whether queued frames are being given up for the final
// frame.
func (c *connection) droppingLocked() bool {
	return c.final != nil && (c.dropAt.IsZero() || !time.Now().Before(c.dropAt))
}

// stop sends the stopping notice once the subscriber, if any, has been queued
// every event up to maxSeq, and waits for the sender to finish.
func (c *connection) stop(maxSeq int64) {
	start := time.Now()
	// Set first: it releases a write that is already stuck.
	_ = c.conn.SetWriteDeadline(start.Add(2 * stoppingGrace))
	for time.Since(start) < stoppingGrace {
		c.mu.Lock()
		caughtUp := c.sub == nil || c.queuedSeq >= maxSeq || c.final != nil || c.shut
		c.mu.Unlock()
		if caughtUp {
			break
		}
		time.Sleep(roomPoll)
	}
	c.finish("", wire.Error{Code: wire.ErrorCodeStopping}, stoppingGrace, 2*stoppingGrace)
	<-c.senderDone
}

// shutdown stops the sender and closes the socket.
func (c *connection) shutdown() {
	c.mu.Lock()
	c.shut = true
	c.dropQueueLocked()
	c.signal()
	c.mu.Unlock()
	_ = c.conn.Close()
}

// next returns the next frame to write, or the final frame, or false once the
// connection is shut.
func (c *connection) next() (outFrame, *wire.Frame, bool) {
	for {
		c.mu.Lock()
		if c.shut {
			c.mu.Unlock()
			return outFrame{}, nil, false
		}
		if c.droppingLocked() {
			c.dropQueueLocked()
		}
		if len(c.queue) > 0 {
			item := c.queue[0]
			c.queue[0] = outFrame{}
			c.queue = c.queue[1:]
			if item.sub != nil {
				c.events--
			}
			c.mu.Unlock()
			return item, nil, true
		}
		if c.final != nil {
			e := c.final.err
			if c.final.lagging {
				last := c.writtenSeq
				e.LastSeq = &last
			}
			id := c.final.id
			c.mu.Unlock()
			frame, err := wire.NewError(id, &e)
			if err != nil {
				fmt.Fprintf(os.Stderr, "server: %v\n", err)
				return outFrame{}, nil, false
			}
			return outFrame{}, &frame, true
		}
		c.mu.Unlock()
		<-c.wake
	}
}

// send writes the queue to the socket until the final frame or the end of
// the connection, then closes the socket.
func (c *connection) send() {
	defer close(c.senderDone)
	defer func() {
		c.mu.Lock()
		c.shut = true
		c.dropQueueLocked()
		c.mu.Unlock()
		_ = c.conn.Close()
	}()
	for {
		item, final, ok := c.next()
		if !ok {
			return
		}
		if final != nil {
			c.logSend(c.writer.Write(*final))
			return
		}
		c.gateSub = item.sub
		err := c.writer.Write(item.frame)
		c.gateSub = nil
		switch {
		case err == nil:
			if item.sub != nil {
				c.mu.Lock()
				if c.sub == item.sub {
					c.writtenSeq = item.frame.Seq
				}
				c.mu.Unlock()
			}
		case errors.Is(err, errDropped):
		case errors.Is(err, wire.ErrFrameTooLarge):
			// Nothing was written; the frame was measured first.
			if item.sub != nil {
				fmt.Fprintf(os.Stderr, "server: event %d over the frame limit\n", item.frame.Seq)
				continue
			}
			frame, ferr := wire.NewError(item.frame.ID, &wire.Error{Code: wire.ErrorCodeFrame})
			if ferr == nil {
				ferr = c.writer.Write(frame)
			}
			if ferr != nil {
				c.logSend(ferr)
				return
			}
		default:
			c.logSend(err)
			return
		}
	}
}

// gateWriter writes encoded frames to the socket. An event is held until the
// socket has room for it and a final notice besides, so a subscriber that
// stops reading can still be told it is lagging; it is dropped instead if the
// connection gives up on its queue meanwhile.
type gateWriter struct{ c *connection }

func (g gateWriter) Write(p []byte) (int, error) {
	c := g.c
	if c.gateSub != nil && c.room != nil {
		for !c.room(len(p)) {
			c.mu.Lock()
			drop := c.shut || c.sub != c.gateSub || c.droppingLocked()
			c.mu.Unlock()
			if drop {
				return 0, errDropped
			}
			select {
			case <-c.wake:
			case <-time.After(roomPoll):
			}
		}
	}
	return c.conn.Write(p)
}

func (c *connection) handle() {
	ctx, cancel := context.WithCancel(context.Background())
	var requests sync.WaitGroup
	defer func() {
		cancel()
		c.closeSubscription()
		c.shutdown()
		requests.Wait()
		<-c.senderDone
		c.server.removeConnection(c)
	}()

	if !c.handshake() {
		<-c.senderDone
		return
	}

	for {
		frame, err := c.reader.Read()
		if err != nil {
			var syntaxErr *json.SyntaxError
			var typeErr *json.UnmarshalTypeError
			switch {
			case errors.Is(err, wire.ErrFrameTooLarge):
				c.rejectOversized()
				return
			case errors.As(err, &syntaxErr), errors.As(err, &typeErr):
				c.sendError("", &wire.Error{Code: wire.ErrorCodeBadRequest})
				continue
			}
			return
		}

		// subscribe and unsubscribe touch connection state, so they run in
		// read order; the event stream itself runs on its own goroutine.
		switch frame.Type {
		case wire.RequestSubscribe:
			c.handleSubscribe(ctx, &requests, frame)
		case wire.RequestUnsubscribe:
			c.handleUnsubscribe(frame)
		default:
			requests.Add(1)
			go func() {
				defer requests.Done()
				c.handleRequest(frame)
			}()
		}
	}
}

// handshakeGrace bounds the write of a handshake or frame error.
const handshakeGrace = 2 * time.Second

// handshake reads the first frame and answers it. It reports whether the
// connection may go on; when it may not, the answer is the final frame.
func (c *connection) handshake() bool {
	frame, err := c.reader.Read()
	if err != nil {
		if errors.Is(err, wire.ErrFrameTooLarge) {
			c.rejectOversized()
		} else {
			c.shutdown()
		}
		return false
	}
	var req wire.HelloRequest
	if frame.Type != wire.RequestHello || json.Unmarshal(frame.Body, &req) != nil || req.Protocol != wire.ProtocolVersion {
		c.finish(frame.ID, wire.Error{Code: wire.ErrorCodeProtocol}, 0, handshakeGrace)
		return false
	}
	c.sendReply(frame.ID, c.server.engine.Hello())
	return true
}

// rejectOversized answers a frame over the limit and ends the connection. The
// rest of the oversized line is read and discarded while the answer goes out,
// so the client's write of it is not cut short before it reads the answer.
func (c *connection) rejectOversized() {
	c.finish("", wire.Error{Code: wire.ErrorCodeFrame}, 0, handshakeGrace)
	_ = c.conn.SetReadDeadline(time.Now().Add(handshakeGrace))
	_, _ = io.Copy(io.Discard, io.LimitReader(c.conn, 16<<20))
	<-c.senderDone
}

func (c *connection) handleRequest(frame wire.Frame) {
	switch frame.Type {
	case wire.RequestStatus:
		if !c.decode(frame, &struct{}{}) {
			return
		}
		status, err := c.server.engine.Status()
		c.respond(frame.ID, "status", status, err)
	case wire.RequestSubmit:
		var req wire.SubmitRequest
		if !c.decode(frame, &req, "submission_id", "input") {
			return
		}
		reply, err := c.server.engine.Submit(req)
		c.respond(frame.ID, "submit", reply, err)
	case wire.RequestCancel:
		var req wire.CancelRequest
		if !c.decode(frame, &req, "turn_id") {
			return
		}
		c.respond(frame.ID, "cancel", wire.CancelReply{Accepted: true}, c.server.engine.Cancel(req.TurnID))
	case wire.RequestOverride:
		var req wire.OverrideRequest
		if !c.decode(frame, &req, "turn_id", "reason") {
			return
		}
		c.respond(frame.ID, "override", wire.OverrideReply{State: "overridden"}, c.server.engine.Override(req.TurnID, req.Reason))
	case wire.RequestRelease:
		var req wire.ReleaseRequest
		if !c.decode(frame, &req, "pool", "reason") {
			return
		}
		reply, err := c.server.engine.Release(req)
		c.respond(frame.ID, "release", reply, err)
	case wire.RequestTurn:
		var req wire.TurnRequest
		if !c.decode(frame, &req, "turn_id") {
			return
		}
		reply, err := buildTurnRecord(c.server.store, req.TurnID)
		c.respond(frame.ID, "turn", reply, err)
	case wire.RequestTranscript:
		var req wire.TranscriptRequest
		if !c.decode(frame, &req, "turn_id") {
			return
		}
		limit := maxTranscriptLimit
		if req.Limit != nil && *req.Limit > 0 && *req.Limit < limit {
			limit = *req.Limit
		}
		reply, err := buildTranscript(c.server.store, req.TurnID, req.Cursor, limit)
		c.respond(frame.ID, "transcript", reply, err)
	case wire.RequestContent:
		var req wire.ContentRequest
		if !c.decode(frame, &req, "ref", "offset", "limit") {
			return
		}
		if req.Offset < 0 || req.Limit <= 0 {
			c.sendError(frame.ID, &wire.Error{Code: wire.ErrorCodeBadRequest})
			return
		}
		limit := min(req.Limit, wire.MaxContentPageBytes)
		reply, err := getContent(c.server.store, req.Ref, req.Offset, limit)
		c.respond(frame.ID, "content", reply, err)
	default:
		c.sendError(frame.ID, &wire.Error{Code: wire.ErrorCodeBadRequest})
	}
}

const maxTranscriptLimit = 200

// decode unmarshals the request body into req and checks that every named
// member is present. A missing member or a body that does not decode is
// answered with bad_request and reported as false.
func (c *connection) decode(frame wire.Frame, req any, required ...string) bool {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(frame.Body, &members); err != nil {
		c.badRequest(frame.ID, errors.New("body is not an object"))
		return false
	}
	for _, name := range required {
		if v, ok := members[name]; !ok || string(v) == "null" {
			c.badRequest(frame.ID, fmt.Errorf("missing %s", name))
			return false
		}
	}
	if err := json.Unmarshal(frame.Body, req); err != nil {
		c.badRequest(frame.ID, err)
		return false
	}
	return true
}

func (c *connection) badRequest(id string, cause error) {
	detail := cause.Error()
	c.sendError(id, &wire.Error{Code: wire.ErrorCodeBadRequest, Detail: &detail})
}

// respond sends body as the reply to id, or the error err maps to.
func (c *connection) respond(id, op string, body any, err error) {
	if err != nil {
		var wireErr *wire.Error
		switch {
		case errors.As(err, &wireErr):
			c.sendError(id, wireErr)
		case errors.Is(err, journal.ErrTurnNotFound), errors.Is(err, journal.ErrNoSuchContent):
			c.sendError(id, &wire.Error{Code: "not_found"})
		case errors.Is(err, journal.ErrOutOfRange):
			c.sendError(id, &wire.Error{Code: wire.ErrorCodeBadRequest})
		default:
			fmt.Fprintf(os.Stderr, "%s: %v\n", op, err)
			c.sendError(id, &wire.Error{Code: wire.ErrorCodeInternal, Detail: ptrStr(err.Error())})
		}
		return
	}
	c.sendReply(id, body)
}

func (c *connection) handleSubscribe(ctx context.Context, requests *sync.WaitGroup, frame wire.Frame) {
	var req wire.SubscribeRequest
	if !c.decode(frame, &req, "since_seq") {
		return
	}

	// A second subscribe replaces the first.
	c.closeSubscription()

	sub, liveFromSeq, err := c.server.hub.Subscribe(req.SinceSeq)
	if err != nil {
		c.respond(frame.ID, "subscribe", nil, err)
		return
	}
	reply, err := wire.NewReply(frame.ID, wire.SubscribeReply{OK: true, LiveFromSeq: liveFromSeq})
	if err != nil {
		sub.Close()
		c.respond(frame.ID, "subscribe", nil, err)
		return
	}

	// The reply is queued as the subscription becomes current, so every
	// event of it follows the reply.
	c.mu.Lock()
	if c.final != nil || c.shut {
		c.mu.Unlock()
		sub.Close()
		return
	}
	c.sub = sub
	c.queuedSeq = req.SinceSeq
	c.writtenSeq = req.SinceSeq
	c.queue = append(c.queue, outFrame{frame: reply})
	c.signal()
	c.mu.Unlock()

	requests.Add(1)
	go func() {
		defer requests.Done()
		c.forward(ctx, sub)
	}()
}

// forward queues the events of sub until it is closed or replaced, the
// connection closes, or the subscriber lags.
func (c *connection) forward(ctx context.Context, sub *hub.Subscription) {
	defer sub.Close()
	for {
		event, err := sub.Next(ctx)
		if err != nil {
			if errors.Is(err, hub.ErrLagging) {
				c.lagging(sub)
			} else if !errors.Is(err, hub.ErrClosed) && ctx.Err() == nil {
				fmt.Fprintf(os.Stderr, "subscription: %v\n", err)
			}
			return
		}
		if !c.enqueueEvent(sub, event) {
			return
		}
	}
}

// closeSubscription ends the current subscription, if any, and drops its
// queued events, so none of them follows a reply queued after this.
func (c *connection) closeSubscription() {
	c.mu.Lock()
	sub := c.sub
	c.sub = nil
	if c.events > 0 {
		kept := c.queue[:0]
		for _, item := range c.queue {
			if item.sub == nil {
				kept = append(kept, item)
			}
		}
		c.queue = kept
		c.events = 0
	}
	c.mu.Unlock()
	if sub != nil {
		sub.Close()
	}
}

func (c *connection) handleUnsubscribe(frame wire.Frame) {
	if !c.decode(frame, &struct{}{}) {
		return
	}
	c.closeSubscription()
	c.sendReply(frame.ID, wire.UnsubscribeReply{OK: true})
}

// sendReply queues body as the reply to id. A reply over the frame limit is
// answered with error{code: "frame"} when it is written.
func (c *connection) sendReply(id string, body any) {
	frame, err := wire.NewReply(id, body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "server: reply %s: %v\n", id, err)
		c.sendError(id, &wire.Error{Code: wire.ErrorCodeInternal, Detail: ptrStr(err.Error())})
		return
	}
	c.enqueue(frame)
}

func (c *connection) sendError(id string, e *wire.Error) {
	frame, err := wire.NewError(id, e)
	if err != nil {
		fmt.Fprintf(os.Stderr, "server: error reply %s: %v\n", id, err)
		return
	}
	c.enqueue(frame)
}

// logSend reports a failed write. A closed connection is the client leaving
// and is not worth a line.
func (c *connection) logSend(err error) {
	if err == nil || errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, os.ErrDeadlineExceeded) {
		return
	}
	fmt.Fprintf(os.Stderr, "server: send: %v\n", err)
}

func ptrStr(s string) *string {
	return &s
}
