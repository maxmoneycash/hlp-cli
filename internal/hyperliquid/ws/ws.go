package ws

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// Subscription is the "subscription" object. Zero fields are omitted.
type Subscription struct {
	Type     string `json:"type"`
	Coin     string `json:"coin,omitempty"`
	Interval string `json:"interval,omitempty"`
	User     string `json:"user,omitempty"`
	Dex      string `json:"dex,omitempty"`
	NSigFigs int    `json:"nSigFigs,omitempty"`
	Mantissa int    `json:"mantissa,omitempty"`
	Fast     bool   `json:"fast,omitempty"`

	// Lossless makes this listener keep every frame instead of the latest
	// 16 (market data is latest-wins by default). It is local only: never
	// sent, not part of Key, so lossless and lossy listeners share one
	// server subscription. Tick bars need every print.
	Lossless bool `json:"-"`
	// Status includes ordered subscription acknowledgments and connection breaks for this listener.
	Status bool `json:"-"`
}

// Key is the normalized identity used to match acks and dedupe subscriptions:
// sorted keys, zero/null/false values dropped, user lowercased. The server's
// echoed form (which fills defaults like nSigFigs:null, fast:false, dex:"")
// normalizes to the same key.
func (s Subscription) Key() string {
	b, _ := json.Marshal(s)
	return canonical(b)
}

// canonical normalizes a subscription JSON object (see Key).
func canonical(raw []byte) string {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return string(raw)
	}
	for k, v := range m {
		switch x := v.(type) {
		case nil:
			delete(m, k)
		case bool:
			if !x {
				delete(m, k)
			}
		case string:
			if x == "" {
				delete(m, k)
			} else if k == "user" {
				m[k] = strings.ToLower(x)
			}
		case float64:
			if x == 0 {
				delete(m, k)
			}
		}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		vb, _ := json.Marshal(m[k])
		b.Write(kb)
		b.WriteByte(':')
		b.Write(vb)
	}
	b.WriteByte('}')
	return b.String()
}

// Event is one server frame routed to a subscriber.
type Event struct {
	Channel    string
	State      string // local lifecycle event for listeners requesting Status
	Data       json.RawMessage
	IsSnapshot bool
	Err        error // set on terminal errors for this subscription
	// Gen is the socket generation the frame arrived on (1 for the first
	// connection, +1 per reconnect). A change means frames may have been
	// missed in between: consumers that must not miss data mark a gap.
	Gen int64
}

// Validator rejects unknown coins before they reach the socket.
// hyperliquid.Registry satisfies it via a small adapter in core.
type Validator func(coin string) error

// Options configures a Conn.
type Options struct {
	URL      string    // wss://api.hyperliquid.xyz/ws
	Validate Validator // required for coin-bearing subscriptions
	// OnState reports connection state for the status bar: "connecting",
	// "live", "reconnecting", "closed".
	OnState func(state string)

	// PingInterval is the idle time after which a ping is sent (default 30 s;
	// the server closes after 60 s of silence).
	PingInterval time.Duration
	// BackoffMin / BackoffMax bound the jittered reconnect delay
	// (default 0.5 s and 15 s).
	BackoffMin, BackoffMax time.Duration
	// MaxDialsPerMinute caps new connections (default 30, the server limit).
	MaxDialsPerMinute int
}

// DefaultURL is the mainnet WebSocket endpoint.
const DefaultURL = "wss://api.hyperliquid.xyz/ws"

// MaxUsers is the server's cap on unique users across user subscriptions.
const MaxUsers = 10

// Connection states reported through Options.OnState.
const (
	StateConnecting   = "connecting"
	StateLive         = "live"
	StateReconnecting = "reconnecting"
	StateClosed       = "closed"
)

var (
	// ErrClosed: Subscribe after Close.
	ErrClosed = errors.New("ws: stream closed")
	// ErrNoValidator: a coin-bearing subscription without Options.Validate.
	ErrNoValidator = errors.New("ws: coin subscriptions need a Validator (an unknown coin kills the socket)")
	// ErrTooManyUsers: more than MaxUsers distinct users.
	ErrTooManyUsers = errors.New("ws: at most 10 unique users across user subscriptions")
	// ErrUnsupported: subscription types the server rejects (webData2).
	ErrUnsupported = errors.New("ws: unsupported subscription")
)

// Stream is the subscription API the TUI, paper broker and `hlp watch` use.
type Stream interface {
	// Subscribe adds a ref-counted subscription. The returned channel is
	// buffered; slow consumers get the latest frames (old ones dropped for
	// market data, never dropped for user fills). cancel unsubscribes when
	// the last subscriber leaves.
	Subscribe(ctx context.Context, sub Subscription) (events <-chan Event, cancel func(), err error)
	Close() error
}

// Dial opens a connection lazily (first Subscribe connects).
func Dial(opts Options) Stream { return NewConn(opts) }

// Conn is the concrete Stream: one multiplexed socket with automatic
// reconnect and resubscribe.
type Conn struct {
	opts   Options
	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	subs    map[string]*sub // by Key()
	ws      *websocket.Conn
	started bool
	closed  bool
	state   string
	nextID  int
	dials   []time.Time
	runDone chan struct{}

	writeMu  sync.Mutex
	lastSend atomic.Int64 // unix nanos of the last frame written
	dialsN   atomic.Int64 // total successful dials (tests, diagnostics)
}

type sub struct {
	s     Subscription
	key   string
	acked bool
	lis   map[int]*listener
}

// statusEvent runs under the connection lock so a subscriber gets the ack before any later print.
func (s *sub) statusEvent(state string, gen int64) {
	for _, l := range s.lis {
		if l.status {
			l.deliver(Event{State: state, Gen: gen})
		}
	}
}

// NewConn returns a lazily connecting Conn.
func NewConn(opts Options) *Conn {
	if opts.URL == "" {
		opts.URL = DefaultURL
	}
	if opts.PingInterval <= 0 {
		opts.PingInterval = 30 * time.Second
	}
	if opts.BackoffMin <= 0 {
		opts.BackoffMin = 500 * time.Millisecond
	}
	if opts.BackoffMax <= 0 {
		opts.BackoffMax = 15 * time.Second
	}
	if opts.MaxDialsPerMinute <= 0 {
		opts.MaxDialsPerMinute = 30
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Conn{opts: opts, ctx: ctx, cancel: cancel, subs: map[string]*sub{}, runDone: make(chan struct{})}
}

// losslessTypes never drop frames for slow consumers.
var losslessTypes = map[string]bool{
	"userFills": true, "userEvents": true, "orderUpdates": true, "userFundings": true,
	"userNonFundingLedgerUpdates": true, "notification": true, "userTwapHistory": true,
	"userTwapSliceFills": true,
}

// Subscribe adds a ref-counted subscription (see Stream). The channel is
// closed when cancel is called, ctx ends, the Conn closes, or the server
// rejects the subscription (after an Event with Err).
func (c *Conn) Subscribe(ctx context.Context, s Subscription) (<-chan Event, func(), error) {
	if s.Type == "" {
		return nil, func() {}, fmt.Errorf("ws: subscription type required")
	}
	if s.Type == "webData2" {
		return nil, func() {}, fmt.Errorf("%w: webData2 is rejected by the server; use webData3 + allDexsClearinghouseState", ErrUnsupported)
	}
	s.User = strings.ToLower(s.User)
	if s.Coin != "" {
		if c.opts.Validate == nil {
			return nil, func() {}, ErrNoValidator
		}
		if err := c.opts.Validate(s.Coin); err != nil {
			return nil, func() {}, err
		}
	}
	key := s.Key()
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, func() {}, ErrClosed
	}
	sb := c.subs[key]
	isNew := sb == nil
	if isNew {
		if s.User != "" && !c.hasUser(s.User) && c.userCount() >= MaxUsers {
			c.mu.Unlock()
			return nil, func() {}, ErrTooManyUsers
		}
		sb = &sub{s: s, key: key, lis: map[int]*listener{}}
		c.subs[key] = sb
	}
	id := c.nextID
	c.nextID++
	l := newListener(losslessTypes[s.Type] || s.Lossless)
	l.status = s.Status
	sb.lis[id] = l
	if l.status && sb.acked && c.ws != nil {
		l.deliver(Event{State: StateLive, Gen: c.dialsN.Load()})
	}
	if !c.started {
		c.started = true
		go c.run()
	}
	conn := c.ws
	c.mu.Unlock()

	if isNew && conn != nil {
		_ = c.send(conn, map[string]any{"method": "subscribe", "subscription": s})
	}
	var once sync.Once
	cancel := func() { once.Do(func() { c.remove(key, id) }) }
	if ctx != nil && ctx.Done() != nil {
		go func() {
			select {
			case <-ctx.Done():
				cancel()
			case <-l.done:
			}
		}()
	}
	return l.ch, cancel, nil
}

func (c *Conn) hasUser(u string) bool {
	for _, sb := range c.subs {
		if sb.s.User == u {
			return true
		}
	}
	return false
}

func (c *Conn) userCount() int {
	seen := map[string]bool{}
	for _, sb := range c.subs {
		if sb.s.User != "" {
			seen[sb.s.User] = true
		}
	}
	return len(seen)
}

// remove drops one listener and unsubscribes when it was the last.
func (c *Conn) remove(key string, id int) {
	c.mu.Lock()
	sb := c.subs[key]
	if sb == nil {
		c.mu.Unlock()
		return
	}
	l := sb.lis[id]
	delete(sb.lis, id)
	last := len(sb.lis) == 0
	if last {
		delete(c.subs, key)
	}
	conn := c.ws
	c.mu.Unlock()
	if l != nil {
		l.close()
	}
	if last && conn != nil {
		_ = c.send(conn, map[string]any{"method": "unsubscribe", "subscription": sb.s})
	}
}

// Close shuts the socket and closes every subscriber channel.
func (c *Conn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	started := c.started
	subs := c.subs
	c.subs = map[string]*sub{}
	conn := c.ws
	c.mu.Unlock()
	c.cancel()
	if conn != nil {
		_ = conn.Close(websocket.StatusNormalClosure, "bye")
	}
	for _, sb := range subs {
		for _, l := range sb.lis {
			l.close()
		}
	}
	if started {
		<-c.runDone
	}
	c.setState(StateClosed)
	return nil
}

// State returns the last reported connection state ("" before first use).
func (c *Conn) State() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// Acked reports whether the server acknowledged sub on the current socket.
func (c *Conn) Acked(s Subscription) bool {
	s.User = strings.ToLower(s.User)
	c.mu.Lock()
	defer c.mu.Unlock()
	sb := c.subs[s.Key()]
	return sb != nil && sb.acked
}

// Dials returns how many sockets were opened so far.
func (c *Conn) Dials() int { return int(c.dialsN.Load()) }

func (c *Conn) setState(st string) {
	c.mu.Lock()
	changed := c.state != st
	c.state = st
	c.mu.Unlock()
	if changed && c.opts.OnState != nil {
		c.opts.OnState(st)
	}
}

func (c *Conn) send(conn *websocket.Conn, msg any) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
		return err
	}
	c.lastSend.Store(time.Now().UnixNano())
	return nil
}

// waitDialBudget enforces MaxDialsPerMinute.
func (c *Conn) waitDialBudget() error {
	for {
		c.mu.Lock()
		now := time.Now()
		keep := c.dials[:0]
		for _, t := range c.dials {
			if now.Sub(t) < time.Minute {
				keep = append(keep, t)
			}
		}
		c.dials = keep
		if len(c.dials) < c.opts.MaxDialsPerMinute {
			c.dials = append(c.dials, now)
			c.mu.Unlock()
			return nil
		}
		wait := time.Minute - now.Sub(c.dials[0])
		c.mu.Unlock()
		if err := sleep(c.ctx, wait); err != nil {
			return err
		}
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return d/2 + time.Duration(rand.Int64N(int64(d)))
}

// run owns the socket: dial, resubscribe, read, reconnect.
func (c *Conn) run() {
	defer close(c.runDone)
	backoff := c.opts.BackoffMin
	first := true
	for c.ctx.Err() == nil {
		if err := c.waitDialBudget(); err != nil {
			return
		}
		if first {
			c.setState(StateConnecting)
		} else {
			c.setState(StateReconnecting)
		}
		conn, _, err := websocket.Dial(c.ctx, c.opts.URL, nil)
		if err != nil {
			first = false
			if sleep(c.ctx, jitter(backoff)) != nil {
				return
			}
			backoff = min(2*backoff, c.opts.BackoffMax)
			continue
		}
		first = false
		gen := c.dialsN.Add(1)
		conn.SetReadLimit(32 << 20)
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			_ = conn.CloseNow()
			return
		}
		c.ws = conn
		subs := make([]Subscription, 0, len(c.subs))
		for _, sb := range c.subs {
			sb.acked = false
			subs = append(subs, sb.s)
		}
		c.mu.Unlock()
		c.setState(StateLive)
		for _, s := range subs {
			_ = c.send(conn, map[string]any{"method": "subscribe", "subscription": s})
		}
		start := time.Now()
		pctx, pcancel := context.WithCancel(c.ctx)
		go c.pinger(pctx, conn)
		c.readLoop(conn, gen)
		pcancel()
		_ = conn.CloseNow()
		c.mu.Lock()
		if c.ws == conn {
			c.ws = nil
			for _, sb := range c.subs {
				sb.statusEvent(StateReconnecting, gen)
			}
		}
		c.mu.Unlock()
		if c.ctx.Err() != nil {
			return
		}
		if time.Since(start) > 30*time.Second {
			backoff = c.opts.BackoffMin
		}
		c.setState(StateReconnecting)
		if sleep(c.ctx, jitter(backoff)) != nil {
			return
		}
		backoff = min(2*backoff, c.opts.BackoffMax)
	}
}

// pinger sends {"method":"ping"} after PingInterval of write silence.
func (c *Conn) pinger(ctx context.Context, conn *websocket.Conn) {
	tick := max(c.opts.PingInterval/4, 5*time.Millisecond)
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if time.Since(time.Unix(0, c.lastSend.Load())) >= c.opts.PingInterval {
				if err := c.send(conn, map[string]string{"method": "ping"}); err != nil {
					return
				}
			}
		}
	}
}

// frame is a server message.
type frame struct {
	Channel string          `json:"channel"`
	Data    json.RawMessage `json:"data"`
}

func (c *Conn) readLoop(conn *websocket.Conn, gen int64) {
	for {
		_, b, err := conn.Read(c.ctx)
		if err != nil {
			return
		}
		c.handle(b, gen)
	}
}

// channelType maps a data channel to the subscription type that produces it.
func channelType(ch string) string {
	switch ch {
	case "activeSpotAssetCtx":
		return "activeAssetCtx"
	case "user":
		return "userEvents"
	}
	return ch
}

// probe extracts routing fields from a data payload.
type probe struct {
	Coin       string  `json:"coin"`
	S          string  `json:"s"`
	I          string  `json:"i"`
	User       string  `json:"user"`
	Dex        *string `json:"dex"`
	IsSnapshot bool    `json:"isSnapshot"`
	UserState  *struct {
		User string `json:"user"`
	} `json:"userState"`
}

func (p probe) coin() string {
	if p.Coin != "" {
		return p.Coin
	}
	return p.S
}

func (p probe) user() string {
	if p.User != "" {
		return p.User
	}
	if p.UserState != nil {
		return p.UserState.User
	}
	return ""
}

func (c *Conn) handle(b []byte, gen int64) {
	var f frame
	if err := json.Unmarshal(b, &f); err != nil || f.Channel == "" {
		return // e.g. bare [] frames
	}
	switch f.Channel {
	case "pong", "post":
		return
	case "subscriptionResponse":
		var ack struct {
			Method       string          `json:"method"`
			Subscription json.RawMessage `json:"subscription"`
		}
		if json.Unmarshal(f.Data, &ack) == nil && ack.Method == "subscribe" {
			key := canonical(ack.Subscription)
			c.mu.Lock()
			if sb := c.subs[key]; sb != nil {
				if !sb.acked {
					sb.acked = true
					sb.statusEvent(StateLive, gen)
				}
			}
			c.mu.Unlock()
		}
		return
	case "error":
		c.handleError(f.Data)
		return
	}
	var p probe
	data := bytes.TrimSpace(f.Data)
	if len(data) > 0 && data[0] == '[' {
		var arr []json.RawMessage
		if json.Unmarshal(data, &arr) == nil && len(arr) > 0 {
			_ = json.Unmarshal(arr[0], &p)
		}
	} else if len(data) > 0 && data[0] == '{' {
		_ = json.Unmarshal(data, &p)
	}
	typ := channelType(f.Channel)
	ev := Event{Channel: f.Channel, Data: f.Data, IsSnapshot: p.IsSnapshot, Gen: gen}
	c.mu.Lock()
	var targets []*listener
	for _, sb := range c.subs {
		if sb.s.Type == typ && matches(sb.s, p) {
			for _, l := range sb.lis {
				targets = append(targets, l)
			}
		}
	}
	c.mu.Unlock()
	for _, l := range targets {
		l.deliver(ev)
	}
}

// matches routes a frame to a subscription. Frames carry coin/s, i, user
// and dex; fields absent from the frame do not filter.
func matches(s Subscription, p probe) bool {
	if s.Coin != "" {
		if c := p.coin(); c != "" && c != s.Coin {
			return false
		}
	}
	if s.Interval != "" && p.I != "" && p.I != s.Interval {
		return false
	}
	if u := p.user(); s.User != "" && u != "" && !strings.EqualFold(u, s.User) {
		return false
	}
	dex := ""
	if p.Dex != nil {
		dex = *p.Dex
	}
	return dex == s.Dex
}

// handleError routes "Error parsing JSON into valid websocket request: {…}"
// to the echoed subscription, which is then dropped.
func (c *Conn) handleError(data json.RawMessage) {
	var msg string
	if json.Unmarshal(data, &msg) != nil {
		return
	}
	if strings.Contains(msg, "Already subscribed") {
		return
	}
	i := strings.Index(msg, "{")
	if i < 0 {
		return
	}
	var req struct {
		Method       string          `json:"method"`
		Subscription json.RawMessage `json:"subscription"`
	}
	if json.Unmarshal([]byte(msg[i:]), &req) != nil || len(req.Subscription) == 0 {
		return
	}
	key := canonical(req.Subscription)
	c.mu.Lock()
	sb := c.subs[key]
	if sb != nil {
		delete(c.subs, key)
	}
	c.mu.Unlock()
	if sb == nil {
		return
	}
	ev := Event{Channel: "error", Err: fmt.Errorf("ws: server rejected %s: %s", sb.s.Type, msg)}
	for _, l := range sb.lis {
		l.deliver(ev)
		l.close()
	}
}

// --- listener: per-subscriber queue with a delivery policy ---

// marketQueue bounds pending market-data frames (oldest dropped).
const marketQueue = 16

type listener struct {
	ch       chan Event
	lossless bool
	status   bool

	mu     sync.Mutex
	queue  []Event
	closed bool
	notify chan struct{}
	done   chan struct{}
}

func newListener(lossless bool) *listener {
	l := &listener{ch: make(chan Event, 1), lossless: lossless, notify: make(chan struct{}, 1), done: make(chan struct{})}
	go l.pump()
	return l
}

func (l *listener) deliver(ev Event) {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	if !l.lossless && len(l.queue) >= marketQueue {
		l.queue = l.queue[1:]
	}
	l.queue = append(l.queue, ev)
	l.mu.Unlock()
	select {
	case l.notify <- struct{}{}:
	default:
	}
}

// close stops delivery. Pending data frames are dropped; a pending error
// event is still handed to the reader (with a short grace period).
func (l *listener) close() {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	l.mu.Unlock()
	close(l.done)
}

// pop removes the oldest queued event.
func (l *listener) pop() (ev Event, ok, closed bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.queue) > 0 {
		ev, ok = l.queue[0], true
		l.queue = l.queue[1:]
	}
	return ev, ok, l.closed
}

// pump forwards queued events to ch until the listener closes.
func (l *listener) pump() {
	defer close(l.ch)
	for {
		ev, ok, closed := l.pop()
		if ok {
			if closed && ev.Err == nil {
				continue // closed: drop data, keep looking for a final error
			}
			select {
			case l.ch <- ev:
			case <-l.done:
				if ev.Err != nil {
					select {
					case l.ch <- ev:
					case <-time.After(time.Second):
					}
					return
				}
			}
			continue
		}
		if closed {
			return
		}
		select {
		case <-l.notify:
		case <-l.done:
		}
	}
}
