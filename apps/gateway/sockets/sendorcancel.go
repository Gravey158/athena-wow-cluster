package sockets

import (
	"context"

	"github.com/walkline/ToCloud9/apps/gateway/packet"
)

// SendOrCancel writes p into ch unless ctx is canceled first. Returns true if
// the packet was sent, false if it was dropped because the context ended.
//
// Use this anywhere the previous code wrote `ch <- p` raw -- those sends would
// block forever if the consumer side was dead, leaking the calling goroutine.
// With this helper the goroutine bails on session/gateway shutdown instead.
//
// Concrete bug history (code-review-02): blocking raw sends in
// session/{session,handler,chat,character,battleground}.go stalled the player
// goroutine on the slightest socket-side issue (worldserver crash, gamesocket
// buffer full, peer disconnect race). Wrapped in this helper they fail-fast
// instead.
func SendOrCancel(ctx context.Context, ch chan<- *packet.Packet, p *packet.Packet) bool {
	select {
	case ch <- p:
		return true
	case <-ctx.Done():
		return false
	}
}
