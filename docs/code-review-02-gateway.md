# Code-Review 02 — apps/gateway

Date: 2026-05-13
Scope: `apps/gateway/` (13680 LoC Go), focused pass on hot-path files:
- `balancer.go` (15 LoC)
- `session/handler.go` (205 LoC)
- `session/session.go` (666 LoC)
- `sockets/gamesocket/game-socket.go` (368 LoC, first 120 lines read)

Reviewer-context: hot-path means every player packet flows through these. Bugs here multiply across concurrent players. The remaining ~5800 LoC in `session/` (mail, channels, guild, groups, battleground, chat, social, character) are feature-specific and lower priority for this pass.

## Summary

Found **11 bugs + 4 architectural issues** in the hot-path. Bug categories:

- **Goroutine leaks** (B5, B8, B14): unbuffered channel sends or fire-and-forget routines without ctx-watch.
- **Blocking channel sends** (B9): `chan <- p` without `select` wrappers across multiple opcode handlers.
- **Magic sleeps as race-condition workarounds** (B6, B10): hardcoded 200-300ms delays in cross-server handover paths.
- **UX-poor reconnect logic** (B11): up to 15 seconds of dead-air on worldserver crash before player gets feedback.
- **Wrong-thing-closed** (B7): one-off forward path closes the *main* worldserver socket on ctx-cancel.
- **Channel close from receiver side** (B18): panic risk during shutdown.

The architecture is workable but leans on package-level globals (`RealmID`, `RetrievedGatewayID`, `HandleMap`, `OpcodeBlacklist`) that make multi-realm and testing brittle.

## Bugs

### B5. `handler.go:168` — goroutine leak on unbuffered `waitDone`

```go
waitDone := make(chan struct{})   // unbuffered
go func() {
    defer cancel()
    defer func() { waitDone <- struct{}{} }()   // blocks forever if no reader
    for { ... select ... }
}()
...
if waitOpcodeToClose != 0 {
    <-waitDone     // conditional reader
} else {
    socket.Close()  // no reader, goroutine's deferred send blocks
}
```

When `waitOpcodeToClose == 0`, the `<-waitDone` is skipped. The goroutine exits its loop, then hits the deferred `waitDone <- struct{}{}` and **blocks forever** waiting for a receiver that never comes. Leaks one goroutine per `ForwardPacketToRandomGameServer` invocation.

**Fix:** `make(chan struct{}, 1)` (buffered) **or** non-blocking send: `select { case waitDone <- struct{}{}: default: }`.

### B6. `handler.go:194` — 300 ms hardcoded race-fix-by-sleep

```go
socket.SendPacket(s.authPacket)
// we need to give some time to add session on the world side
time.Sleep(time.Millisecond * 300)
socket.SendPacket(p)
```

Race between server-side session-init and packet-forward. Currently masked by 300 ms unconditional delay. Two consequences: 300 ms latency tax on every forward + no guarantee it's enough under server load. Better fix: handshake ack from worldserver before forwarding the real packet.

Cousin: `session.go:479` — same pattern, 200 ms version, in `connectToGameServerWithAddress`.

### B7. `handler.go:184` — wrong socket closed on ctx timeout

```go
case <-newCtx.Done():
    if s.worldSocket != nil {
        s.worldSocket.Close()   // closes the player's MAIN worldserver connection
    }
    return
```

`ForwardPacketToRandomGameServer` creates a **one-off** `socket` for a single packet forward. The ctx-timeout path closes `s.worldSocket` instead of `socket`. This means: any timeout in a one-off forward (e.g. `MsgQueryNextMailTime` to a random server) kicks the player off their actual worldserver connection.

**Fix:** `socket.Close()` (the local one). `s.worldSocket` should never be touched here.

### B8. `handler.go:176` — blocking forward into client gamesocket

```go
case p, open := <-socket.ReadChannel():
    if !open { return }
    s.gameSocket.WriteChannel() <- p   // blocks if client buffer is full or socket dead
```

No `select`-wrap. If the player's gamesocket buffer is full (size 10, see B16) or the goroutine reading from it has crashed, this send blocks the forward-goroutine forever.

**Fix:** `select { case s.gameSocket.WriteChannel() <- p: case <-newCtx.Done(): return }`.

### B9. `session.go:190,222,387,403,472` — blocking channel sends throughout HandlePackets

Multiple instances of `s.worldSocket.WriteChannel() <- p` or `s.gameSocket.WriteChannel() <- p` without `select`. Each is a potential block-forever site if the target socket is dead but not yet reaped.

The select-based HandlePackets main loop is itself blocked on these sends since they happen *inside* the select case body. So a dead worldsocket can stop the entire player-session goroutine from processing further packets — even harmless ones like incoming pings.

**Fix:** all internal channel sends should be `select { case ch <- p: case <-c.Done(): return }` — or use a non-blocking helper `sendOrDrop(ch, p, ctx)`.

### B10. `session.go:479` — 200 ms magic sleep in login path

Identical pattern to B6, applies to every character login (CMsgPlayerLogin). Adds 200 ms tax to login latency.

### B11. `session.go:519-550` — 3-second dead air before reconnect; up to 15 s worst case

```go
func (s *GameSession) onWorldSocketClosed() {
    go func(charGUID uint64) {
        s.SendSysMessage("Lost connection with world server...")
        time.Sleep(time.Second * 2)           // dead air 1
        s.SendSysMessage("Trying to recover...")
        time.Sleep(time.Second * 1)           // dead air 2
        for i := 0; i < 3; i++ {
            char, socket, err = s.connectToGameServer(...)
            if err != nil { ... }
            time.Sleep(time.Second * 5)        // 5s between retries
        }
        ...
    }(s.character.GUID)
}
```

Worst case (2 failed reconnect attempts then success on 3rd): 2 + 1 + 5 + 5 = **13 seconds** before the player is back in the world. With final-fail path adding another 2s for "returning to character screen", that's **15 s** of dead-air UX.

The sleeps appear to be "let the dust settle" defensive pauses, but they're applied unconditionally. Better: try reconnect immediately, only sleep on failure. Total worst-case fix: try-immediate → 0+5+5+2 = 12 s with same retry budget but much faster on success.

### B12 (not a bug, just funky). `session.go:206-207`

```go
// worldReadChan can be nil and can be forever blocked
case p, ok := <-worldReadChan:
```

Receiving from a `nil` channel in a `select` disables that case (Go language semantic). The author leaned on this to gate worldReadChan handling when `s.worldSocket == nil`. **Correct usage**, just unusual — keep, but the comment could be clearer ("disabled when worldSocket nil").

### B14. `session.go:519` — fire-and-forget reconnect goroutine not ctx-aware

```go
go func(charGUID uint64) {
    // 15 seconds of work, sleeps + retries
}(s.character.GUID)
```

The goroutine doesn't watch `s.ctx.Done()`. If the player's HandlePackets-goroutine returns (e.g. because `s.gameSocket` closed, player disconnected), this reconnect goroutine keeps trying to bring back a session for a player who's already gone. Eventually it wins a reconnect, modifies `session.worldSocket`, sends a sys-message to a closed gamesocket — error logs.

**Fix:** thread `s.ctx` through the goroutine. Bail on `<-s.ctx.Done()` between each step.

### B15. `session.go:530-531` — `context.TODO()` instead of `s.ctx` in reconnect

```go
char, socket, err = s.connectToGameServer(context.TODO(), charGUID, ...)
_, err := s.charServiceClient.SavePlayerPosition(context.TODO(), ...)
```

Two `context.TODO()` calls inside the reconnect loop. They lose the parent context cancellation signal, so even if the gateway is shutting down, these gRPC calls run to completion (with their own internal timeouts only).

**Fix:** `s.ctx` everywhere here. This is the same root cause as B14.

### B16. `gamesocket/game-socket.go:56-57` — channel buffers undersized for burst load

```go
sendChan: make(chan *packet.Packet, 10),
readChan: make(chan *packet.Packet, 10),
```

10 packets is tiny. A single `SMsgUpdateObject` burst (e.g. player walks into a city with 100 nearby actors) easily generates 50+ packets. With buffer 10, the producer-side (worldserver socket reader → gameSocket.WriteChannel) blocks the moment buffer fills — amplifies B8 and B9 into real bug-fires under load.

**Fix:** raise to 256 minimum, or make configurable per-session. Backpressure on the worldsocket side is correct behavior (avoids memory blowup) but 10 is way too low.

### B17. `gamesocket/game-socket.go:24` — package-level mutable test flag

```go
// useEncryption used to disable encryption during testing
var useEncryption = true
```

Test-code-smell promoted to production. Concurrent tests cannot safely change this; also security-relevant flag should not be globally mutable.

**Fix:** field on `GameSocket` struct, set via constructor parameter. Tests use a test-specific constructor.

### B18. `gamesocket/game-socket.go:108-110` — channel close from receiver side

```go
case <-s.ctx.Done():
    if s.session == nil {
        close(s.sendChan)
    }
    return
```

`sendChan` is closed when the receiver (this goroutine) sees ctx-done and no session. But other goroutines may still be sending into `sendChan`. **Panic risk:** "send on closed channel". The `if s.session == nil` guard is a partial mitigation, not a real one — `s.session` can be set concurrently.

**Fix:** never close `sendChan` from the receiver. Close from the sender after all writers have stopped — or use a separate "done" signaling channel and let GC handle the unused channel.

## Architectural Issues

### A3. Dual gameserver-grpc state (`s.gameServerGRPCClient` + `s.gameServerGRPCConnMgr`)

```go
type GameSession struct {
    ...
    gameServerGRPCClient          pbGameServ.WorldServerServiceClient
    gameServerGRPCConnMgr         conn.GameServerGRPCConnMgr
    ...
}
```

`gameServerGRPCConnMgr` manages a map of `address → conn`. `gameServerGRPCClient` is set to a specific connection in `connectToGameServer` (line 443). When player switches worldservers (map transition), `gameServerGRPCClient` is overwritten but the manager's prior connections remain.

State inconsistency risk: code that uses `gameServerGRPCClient` might be on a stale address. Code that uses `gameServerGRPCConnMgr.GRPCConnByGameServerAddress(...)` is correct.

**Fix:** drop `gameServerGRPCClient` field; always look up via manager.

### A4. Package-level globals: `RealmID`, `RetrievedGatewayID`, `HandleMap`, `OpcodeBlacklist`

- `apps/gateway/balancer.go:3,5`: `var RealmID uint32`, `var RetrievedGatewayID string`
- `apps/gateway/session/handler.go:16-117`: `var OpcodeBlacklist = ...`, `var HandleMap = ...`

These are read everywhere as `root.RealmID`, `HandleMap[opcode]`, etc. Consequences:
- **No multi-realm** in a single gateway process. The gateway is hardcoded to one realm at startup.
- **Tests can't isolate** — overriding `HandleMap` mutates global state.
- **Initialisation order** is fragile.

**Fix path:** instance-scoped state. Constructor injects `realmID`, `handlerMap`. Migration is large but mechanical (search-replace + thread parameter). Not Phase 0 work.

### A5. `OpcodeBlacklist` as a global map (handler.go:16)

Three opcodes hardcoded as "drop these from worldserver" (friend-status, contact-list, channel-list). The reason is documented: gateway implements these features distributedly. But:

- The list is global immutable — fine for now
- But it's package-level so refactoring it into "gateway-feature-decisions" config is a search-replace job
- And expanding the list (e.g. to drop more opcodes when guild-bank moves to gateway in Phase 3) means editing this map across multiple feature work

**Fix path:** make it part of a `FeatureRoutingConfig` struct, owned by `GameSessionParams`, propagated explicitly.

### A6. `HandleMap` as a global map — same issue, larger impact

60+ entries hardcoded at package init. Same problems as A4. Should be a registry built per-session or per-realm.

## Slimming Opportunities

- **`s.gameServerGRPCClient` field deletable** (A3 above) → -1 field, -2 cleanup sites.
- **`var WorldSocketCreator = worldsocket.NewWorldSocketWithAddress`** (session.go:611) — package-level constructor injection point. OK pattern for testing, but the indirection adds noise. Could be a `SessionFactory` interface instead.
- **`var useEncryption = true`** (B17) — delete + struct field.

## What's worth fixing first

Priority order, by player-impact × ease-of-fix:

1. **B16** (channel buffers 10→256) — one-line fix, immediately better under load.
2. **B7** (wrong-socket-closed) — one-line `s.worldSocket.Close()` → `socket.Close()`, prevents random kicks.
3. **B5, B8, B18** (channel-close/leak triplet) — small refactor of `ForwardPacketToRandomGameServer` to use buffered done-chan or non-blocking pattern.
4. **B11** (15-second dead-air UX) — restructure `onWorldSocketClosed` to "try-immediate-then-backoff" instead of "sleep-then-try".
5. **B14 + B15** (ctx-aware reconnect) — thread `s.ctx`, watch in retry loop.
6. **B6, B10** (magic sleeps as race-fixes) — replace with handshake-ack. Requires worldserver-side cooperation; deferred until we own that codebase end-to-end.
7. **B9** (blocking sends everywhere) — `sendOrCancel` helper, mechanical refactor across files.

The architectural issues (A3-A6) are deferred to a dedicated refactoring sprint, not addressed in this pass.

## What I did *not* read

These are session/-subsystem files I skipped this pass — likely have analogous patterns (blocking channel sends, magic sleeps) since they share the same `GameSession` shape:

- `session/mail.go` (686 LoC)
- `session/channels.go` (663 LoC) + `channels_moderation.go` (471 LoC)
- `session/guild.go` (662 LoC)
- `session/groups.go` (644 LoC)
- `session/battleground.go` (379 LoC)
- `session/chat.go` (373 LoC)
- `session/social.go` (357 LoC)
- `session/interceptors.go` (292 LoC)
- `session/character.go` (235 LoC)
- `packet/` (3062 LoC — protocol codec)
- `events-broadcaster/` (1062 LoC — NATS pub/sub)
- `service/` (931 LoC — gRPC clients to other microservices)

A second-pass review of these is a separate sprint.
