# 006. Replace ToCloud9 chatserver with IRC backend; gateway as translator

Date: 2026-05-13
Status: Proposed

## Context

ToCloud9 ships `apps/chatserver/` (3352 LoC Go) as the cluster-aware chat
service. It handles:

- Persistent channel state (joined players, ban/mute lists, channel
  passwords)
- Cross-pod broadcast via NATS (`chat-channels-listener`)
- Player presence (online characters by GUID/name with the dual-mutex
  bug fixed in commit `18c7b59`)
- Server-internal messaging (system MOTD, broadcast announces)

Open-source IRC daemons exist that solve the same general problem at
much larger scale and with battle-tested reliability:

- **UnrealIRCd 6** (C, modular)
- **InspIRCd 4** (C++, modular)
- **Solanum** (C, charybdis-derived, used by Libera.Chat)
- **Ergo** (Go, single-binary, opinionated)

The IRC protocol (RFC 1459 / 2812, IRCv3 extensions) is conceptually a
superset of WoW chat:
- Channels: PRIVMSG #channel <msg>
- Whispers: PRIVMSG nick <msg>
- Join/part with passwords (MODE +k)
- Operator powers (MODE +o, KICK, KILL)
- Ban lists (MODE +b)
- ignore-list-equivalent: client-side or via channel +m mode

Plus IRC enables **external Federation**: third-party IRC clients,
Discord bridges (`matterbridge`), Matrix's `matrix-appservice-irc`, all
plug in for free.

The user's request:

> "chatserver könnte man auch auslagern? sowas wie ein irc chat?"

The pragmatic answer is yes, with non-trivial translation work. This ADR
records the design + concrete migration shape.

## Decision

**Replace `apps/chatserver/` with an IRC daemon + a thin translator in the
gateway.** The gateway's existing chat opcode handlers (`session/chat.go`,
`session/channels.go`, `session/channels_moderation.go` — currently ~1800
LoC combined) become IRC-client adapters. ToCloud9 channels live as
IRC channels on the backend daemon.

Chosen IRC daemon: **Ergo (ergochat.org)** — pure Go, single-binary,
SQLite-or-Postgres-backed, scriptable via WebSocket admin API, RFC 1459
+ IRCv3 v3.6. Reasons:

- Same language as rest of ToCloud9 stack (Go) — easier diagnostics,
  shared `shared/healthandmetrics`-style integration possible
- No C++ build chain to maintain
- Built-in account registration (we map to existing AC `account` table)
- Already used by Libera-style federation patterns

Alternative backends considered:

- **UnrealIRCd**: most feature-rich, but C codebase + module system to
  maintain. Not blocking.
- **InspIRCd**: similar tradeoff. Slightly cleaner module API than
  Unreal, still C++.
- **Solanum**: battle-tested at Libera.Chat scale, but C + low-level
  config. Overkill for our scale (<1000 concurrent).
- **Embed a library** (e.g. `gopkg.in/sorcix/irc.v2`): would let chat
  live in-process with the gateway. Tempting but couples chat lifetime
  to gateway lifetime; explicit external daemon is more decoupled.

## Translation matrix

WoW 3.3.5a chat-message-types (CMSG_MESSAGE_CHAT type field) → IRC:

| WoW msg type | Description | IRC equivalent |
|---|---|---|
| `CHAT_MSG_SAY` | local zone say | `PRIVMSG #zone-<map>-<zone> <txt>` (broadcast to nearby) |
| `CHAT_MSG_YELL` | zone-wide yell | `PRIVMSG #zone-<map>-<zone>-yell <txt>` |
| `CHAT_MSG_WHISPER` | direct PM | `PRIVMSG <target-nick> <txt>` |
| `CHAT_MSG_PARTY` | party (5p) | `PRIVMSG #party-<group-uuid> <txt>` |
| `CHAT_MSG_RAID` | raid (40p) | `PRIVMSG #raid-<group-uuid> <txt>` |
| `CHAT_MSG_GUILD` | guild | `PRIVMSG #guild-<guild-id> <txt>` |
| `CHAT_MSG_CHANNEL` | user channel | `PRIVMSG #user-<name> <txt>` |
| `CHAT_MSG_SYSTEM` | server announce | `NOTICE` from `system` user |
| `CHAT_MSG_BG` | battleground | `PRIVMSG #bg-<instance-id> <txt>` |
| `CHAT_MSG_EMOTE` / `TEXTEMOTE` | /me action | IRCv3 `ACTION` ctcp |
| `CHAT_MSG_RAID_BOSS_EMOTE` | boss yell | `PRIVMSG #zone-<map>-<zone> <txt>` flagged |

Each WoW chat message has:
- Source GUID + name + race + class (visual rendering uses class color)
- Language ID (Common, Orcish, Darnassian, … — auto-translates between
  faction)
- Channel name (for `CHAT_MSG_CHANNEL` only)
- Body text

Mapping to IRC:
- Source GUID/name → IRC `nick` = `realm<N>_<charname>` (avoids collision
  across realms; e.g. `r1_Salan`, `r2_Salan`)
- Class color → IRCv3 `+draft/color` tag (modern IRC clients support;
  older clients see plain text)
- Language ID → IRCv3 `+server-time` + custom `+wow-language` tag;
  faction-incomprehensibility is enforced gateway-side (translator
  scrambles text for the receiving player if their faction can't
  understand source's language)
- Channel name → IRC channel name (lowercase, prefix `#user-`)

## Namespace handling

WoW client expects player-names to be unique per-realm. IRC nicks are
flat. Mapping:

- Local realm only: IRC nick = `<charname>` (current behavior, no
  collision)
- Multi-realm: IRC nick = `r<realm-id>_<charname>`, gateway renders as
  `<charname>-<realm-name>` to the WoW client (3.3.5a doesn't display
  realm-suffix; we'd need to either prefix the message body with a
  visual `[RealmA]` tag, or wait for Phase-3 CRZ-style Cross-Realm
  support)

Practical compromise: prefix realm-suffix in the rendered chat body for
cross-realm channels (BG cross-realm, global system); strip in same-realm
contexts.

## Auth

IRC traditionally uses PASS (cleartext) or SASL (PLAIN, EXTERNAL,
SCRAM-SHA-256).

ToCloud9 already authenticates the player at the gateway level via
SRP6 against AC's `account.authsession` data. The gateway has the
`accountID` after CMSG_AUTH_SESSION succeeds. The translator can then
log into Ergo via SASL EXTERNAL using a TLS client-cert that Ergo
trusts, mapped to a service-account. Player identity is communicated
to Ergo via WHOX extra fields or just baked into the IRC nick.

**Important**: Ergo's account table is separate from AC's. We don't
sync passwords; Ergo never sees player passwords. The gateway is the
authoritative auth — Ergo trusts the gateway-as-SASL-EXTERNAL service.

Single-realm: pre-register each char on first login (or lazily).
Multi-realm: per-realm Ergo instance OR single Ergo with realm-suffix
nicks.

## Features that need re-implementation (gateway-side translator scope)

These don't map 1:1 to standard IRC and need explicit translator logic:

1. **Player ignore lists**: WoW ignores are server-side (you stop seeing
   ignored player's msgs even in same channel). IRC `/ignore` is
   client-side. Translator: maintain per-player ignore-list in gateway
   state, drop incoming PRIVMSG/CHANNEL-MSG before forwarding to WoW
   client when source is on receiver's ignore list. Persistence: re-use
   AC characters-DB ignore tables.

2. **GM mute (server-imposed silence)**: `+m` channel mode (moderated) +
   GM-only +v voice doesn't quite match. Better: `KICKBAN` on a
   per-channel basis OR `MODE +q` (quiet, IRCv3) on the muted nick. The
   translator needs an admin API into Ergo to apply these — Ergo's
   REST admin API or `OPER` command from a service connection.

3. **Channel passwords**: `MODE +k <password>`. Map directly.

4. **Channel ownership / moderator tiers**: WoW channels have multiple
   admin levels. IRC has `+o` (operator), `+v` (voice), `+h` (halfop,
   InspIRCd only — not in Ergo). Map: WoW channel owner → IRC channel
   `+o` with `+f` (founder) status. Moderators → `+o` only. Plain users
   → no mode.

5. **Cross-faction language scrambling**: same as today (gateway
   scrambles in CHAT_MSG handler before forwarding to client). IRC
   delivers the original text; gateway transforms per-player based on
   faction membership.

6. **Server announcements (MOTD, news)**: NOTICE from a service account
   to `#wow-system` channel which all players auto-join.

7. **Player presence**: ToCloud9 has `charactersInMemRepo` for "who's
   online and where". Replace with IRC's `WHO` / `WHOIS` queries
   + cached in gateway.

## Lifecycle for instance-specific channels

When a player joins a party/raid/BG/arena, ToCloud9 creates a per-instance
channel (`party-<group-uuid>`). With IRC backend:

- Gateway issues `JOIN #party-<uuid>` on player's IRC connection upon
  party formation event from NATS (groupserver still owns party state).
- On party disband: gateway issues `PART` for all members.
- Channel auto-deletes when last user parts (Ergo handles this — `MODE
  +P` for persistent channels disabled for these).

This pattern matches ADR-004 (1:1 Map=Pod) instance lifecycle: ephemeral
channels for ephemeral game-state.

## Migration path

Phase-by-phase rollout, never breaking existing functionality:

**Phase A (1-2 weeks)**:
- Run Ergo alongside ToCloud9 chatserver. Both authoritative for their
  own channels.
- Gateway gets `IRCAdapter` interface, opt-in via per-channel config:
  certain channels (e.g. `World`) routed via Ergo, others via legacy
  chatserver.
- Validate: external IRC clients can join `#world`; in-game players see
  IRC user messages.

**Phase B (2-3 weeks)**:
- All public/user channels migrate to Ergo. ToCloud9 chatserver still
  handles party/raid/guild (those are tightly coupled to group state).
- Whispers route via Ergo PRIVMSG.

**Phase C (3-4 weeks)**:
- Party/raid/BG/arena channels move to Ergo too. groupserver gains an
  Ergo-admin client to JOIN/PART members on group events.
- Guild channels migrate (guildserver gets the same Ergo-admin client).

**Phase D (cleanup)**:
- Delete `apps/chatserver/` (3352 LoC).
- Delete corresponding gateway session/chat.go internal state caches
  (~500 LoC).
- Update Helm chart: drop chatserver Deployment, add ergo StatefulSet
  with SQLite PVC.

Estimated total effort: **6-12 weeks** of focused work, plus operational
shakedown.

## Resource cost

Ergo (single-instance, 1000 concurrent users):
- ~50-100m CPU
- ~150-300 MiB RAM
- Cluster-mode Ergo (multi-instance with shared backend): not GA;
  single-instance is fine for <5000 concurrent

Compared to current chatserver: roughly equivalent. Net resource impact
zero; net code reduction substantial.

## Open questions (need decisions during implementation)

1. **Realm-suffix display format in WoW client**: prefix `[r2]<name>: msg`
   or rendered class-color overlay? 3.3.5a client doesn't natively support
   cross-realm suffix display. Best workaround TBD.

2. **Ergo persistence backend**: SQLite (default, file-on-PV) or
   Postgres? For <1000 concurrent SQLite is enough. Above that consider
   Postgres for write-scale. Galera-3 is overkill for chat state.

3. **Bridge to external networks**: which Discord/Matrix bridge is
   trustworthy + maintained? `matterbridge` is the obvious answer but
   has its own ops burden.

4. **GM oper privileges**: how is gateway-as-translator authenticated to
   Ergo as an OPER? TLS client cert + SASL EXTERNAL is cleanest.

5. **Performance under load**: WoW broadcast messages (yell across zone)
   can send to 100+ players at once. Ergo handles this fine but adds
   one TCP-write per recipient on the gateway-to-ergo side. Net latency
   vs current NATS-broadcast model: roughly equivalent.

6. **IPv6**: Ergo supports v6 natively; ToCloud9 chatserver is v4-only
   today. Bonus.

## Status

Proposed. Implementation NOT started — this is a multi-week roadmap item.
Current `chatserver` race-bugs are fixed (commit `18c7b59` and the
dual-mutex single-mutex refactor) so the existing service is stable
until/unless we decide to execute this ADR.

## Consequences

### Positive

- Drop 3352 LoC of in-house chat code in favor of a community-maintained,
  protocol-standard daemon
- Free Discord/Matrix federation
- Better operator tools (rate-limiting, spam-control, account-locking)
  via Ergo's existing toolkit
- IPv6 support
- External admins can use any IRC client to monitor in-game chat

### Negative

- 6-12 weeks of focused work for migration
- Translator layer in gateway grows (~1000 LoC new) — net code is still
  less, but distribution shifts
- New operational dependency (Ergo StatefulSet, its database)
- IRC v.s. WoW-Chat impedance mismatch in places (faction-language
  scrambling, ignore-list semantics) means translator complexity is
  real, not cosmetic

### Risk mitigations

- Phase A and B can run in parallel with existing chatserver — no big-bang
  switchover required
- If Phase C blocks on group-state coupling, we can stop at Phase B and
  keep chatserver for party/raid/guild while public channels live on
  IRC
- Revert path: switch gateway's `IRCAdapter` config back to "use legacy
  chatserver" per channel — at any point during Phase A/B

## Related

- ADR 004 (1:1 Map=Pod): same microservice-ownership philosophy
- Goal-file Phase 3 "Global Channels cluster-aware": now ✅ upstream
  walkline; an Ergo-backed solution would be a Phase-4+ replacement
- `code-review-02-gateway.md` documented the chat-specific files in
  gateway/session that become the translator
