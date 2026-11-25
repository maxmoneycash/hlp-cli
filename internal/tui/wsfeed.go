package tui

// WSStateFeed carries WebSocket connection state ("connecting", "live",
// "reconnecting", "closed") from the stream to the status bar. The CLI
// passes feed.OnState as ws.Options.OnState when it dials, and the same
// feed in Options.WSFeed; the shell then shows the state live.
type WSStateFeed struct {
	ch chan string
}

// NewWSStateFeed returns a feed ready to use.
func NewWSStateFeed() *WSStateFeed {
	return &WSStateFeed{ch: make(chan string, 8)}
}

// OnState records a state change. It never blocks: if the shell is not
// keeping up, the oldest pending state is dropped (latest wins).
func (f *WSStateFeed) OnState(state string) {
	if f == nil {
		return
	}
	for {
		select {
		case f.ch <- state:
			return
		default:
			select {
			case <-f.ch:
			default:
			}
		}
	}
}

// C is the receive side used by the shell.
func (f *WSStateFeed) C() <-chan string {
	if f == nil {
		return nil
	}
	return f.ch
}
