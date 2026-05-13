package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog"

	root "github.com/walkline/ToCloud9/apps/gateway"
	eBroadcaster "github.com/walkline/ToCloud9/apps/gateway/events-broadcaster"
	"github.com/walkline/ToCloud9/apps/gateway/packet"
	"github.com/walkline/ToCloud9/apps/gateway/service"
	"github.com/walkline/ToCloud9/apps/gateway/sockets"
	"github.com/walkline/ToCloud9/apps/gateway/sockets/worldsocket"
	pbChar "github.com/walkline/ToCloud9/gen/characters/pb"
	pbChat "github.com/walkline/ToCloud9/gen/chat/pb"
	pbGroup "github.com/walkline/ToCloud9/gen/group/pb"
	pbGuild "github.com/walkline/ToCloud9/gen/guilds/pb"
	pbMail "github.com/walkline/ToCloud9/gen/mail/pb"
	pbMatchmaking "github.com/walkline/ToCloud9/gen/matchmaking/pb"
	pbServ "github.com/walkline/ToCloud9/gen/servers-registry/pb"
	pbGameServ "github.com/walkline/ToCloud9/gen/worldserver/pb"
	"github.com/walkline/ToCloud9/shared/events"
	"github.com/walkline/ToCloud9/shared/gameserver/conn"
)

var (
	worldConnectErrInstanceNotFound  = errors.New("no available world instances")
	worldConnectErrCharacterNotFound = errors.New("character not found")
)

// GameSession represents session of the player, holds world and game sockets, routes and handles packets.
type GameSession struct {
	ctx    context.Context
	logger *zerolog.Logger

	gameSocket  sockets.Socket
	worldSocket sockets.Socket

	eventsChan        <-chan eBroadcaster.Event
	sessionSafeFuChan chan func(*GameSession)

	charServiceClient             pbChar.CharactersServiceClient
	serversRegistryClient         pbServ.ServersRegistryServiceClient
	chatServiceClient             pbChat.ChatServiceClient
	guildServiceClient            pbGuild.GuildServiceClient
	mailServiceClient             pbMail.MailServiceClient
	groupServiceClient            pbGroup.GroupServiceClient
	gameServerGRPCClient          pbGameServ.WorldServerServiceClient
	matchmakingServiceClient      pbMatchmaking.MatchmakingServiceClient
	eventsProducer                events.GatewayProducer
	eventsBroadcaster             eBroadcaster.Broadcaster
	chatChannelsEventsBroadcaster *eBroadcaster.ChatChannelsService
	charsUpdsBarrier              *service.CharactersUpdatesBarrier
	realmNamesService             *service.RealmNamesService
	gameServerGRPCConnMgr         conn.GameServerGRPCConnMgr

	groupUpdateCounter uint32

	packetProcessTimeout time.Duration

	authPacket *packet.Packet

	pingToWorldServerStarted time.Time

	accountID uint32
	character *LoggedInCharacter

	teleportingToNewMap *uint32

	packetSendingControl PacketSendingControl

	channelMembership          *ChannelMembership
	worldserverChannelBuffer   []WorldserverChannelInfo
	worldserverChannelBufferMu sync.Mutex
	worldserverChannelTimer    *time.Timer

	// showGameserverConnChangeToClient when enabled sends chat system message
	// to the player with information about connection change.
	showGameserverConnChangeToClient bool

	// realmID + gatewayID were previously read from package-level globals
	// (root.RealmID, root.RetrievedGatewayID). Per-session fields now (A4):
	// foundation for multi-realm support, removes init-order fragility.
	realmID   uint32
	gatewayID string
}

type GameSessionParams struct {
	CharServiceClient                pbChar.CharactersServiceClient
	ServersRegistryClient            pbServ.ServersRegistryServiceClient
	ChatServiceClient                pbChat.ChatServiceClient
	GuildsServiceClient              pbGuild.GuildServiceClient
	MailServiceClient                pbMail.MailServiceClient
	MatchmakingServiceClient         pbMatchmaking.MatchmakingServiceClient
	GroupServiceClient               pbGroup.GroupServiceClient
	EventsProducer                   events.GatewayProducer
	CharsUpdsBarrier                 *service.CharactersUpdatesBarrier
	RealmNamesService                *service.RealmNamesService
	EventsBroadcaster                eBroadcaster.Broadcaster
	ChatChannelsEventBroadcaster     *eBroadcaster.ChatChannelsService
	GameServerGRPCConnMgr            conn.GameServerGRPCConnMgr
	PacketProcessTimeout             time.Duration
	ShowGameserverConnChangeToClient bool

	// RealmID + GatewayID injected from gateway main.go config.
	// Previously read from root.RealmID/root.RetrievedGatewayID globals (A4).
	RealmID   uint32
	GatewayID string
}

func NewGameSession(
	ctx context.Context, logger *zerolog.Logger,
	gameSocket sockets.Socket, accountID uint32,
	authPacket *packet.Packet, params GameSessionParams,
) *GameSession {
	const defaultPacketProcessingTimeout = time.Second * 5
	packetProcessTimeout := params.PacketProcessTimeout
	if packetProcessTimeout == 0 {
		packetProcessTimeout = defaultPacketProcessingTimeout
	}

	s := &GameSession{
		ctx:        ctx,
		logger:     logger,
		gameSocket: gameSocket,
		authPacket: authPacket,
		accountID:  accountID,

		charServiceClient:                params.CharServiceClient,
		serversRegistryClient:            params.ServersRegistryClient,
		chatServiceClient:                params.ChatServiceClient,
		guildServiceClient:               params.GuildsServiceClient,
		mailServiceClient:                params.MailServiceClient,
		matchmakingServiceClient:         params.MatchmakingServiceClient,
		groupServiceClient:               params.GroupServiceClient,
		eventsProducer:                   params.EventsProducer,
		eventsBroadcaster:                params.EventsBroadcaster,
		chatChannelsEventsBroadcaster:    params.ChatChannelsEventBroadcaster,
		charsUpdsBarrier:                 params.CharsUpdsBarrier,
		realmNamesService:                params.RealmNamesService,
		gameServerGRPCConnMgr:            params.GameServerGRPCConnMgr,
		showGameserverConnChangeToClient: params.ShowGameserverConnChangeToClient,

		sessionSafeFuChan:        make(chan func(*GameSession), 100),
		packetProcessTimeout:     packetProcessTimeout,
		channelMembership:        NewChannelMembership(0, params.ChatChannelsEventBroadcaster),
		worldserverChannelBuffer: make([]WorldserverChannelInfo, 0),

		realmID:   params.RealmID,
		gatewayID: params.GatewayID,
	}
	return s
}

// HandlePackets handles game and world packets, as well as general events (like messages).
// Has infinite loop that can be broken with ctx or by closing gameSocket read channel.
func (s *GameSession) HandlePackets(ctx context.Context) {
	c, cancel := context.WithCancel(ctx)
	defer cancel()
	defer s.logger.Debug().Msg("Stopped to handle packets")

	defer func() {
		if s.character != nil {
			s.onLoggedOut()
		}
	}()

	handleEvent := func(event eBroadcaster.Event) {
		handler, found := EventsHandleMap[event.Type]
		if !found {
			return
		}

		pCtx, pCancel := context.WithTimeout(c, s.packetProcessTimeout)
		defer pCancel()

		if err := handler.Handle(pCtx, s, &event); err != nil {
			s.logger.Error().Err(err).Msgf("can't handle event with name %s", handler.name)
		}
	}

	var worldReadChan <-chan *packet.Packet
	var err error
	for {
		if s.worldSocket != nil {
			worldReadChan = s.worldSocket.ReadChannel()
		} else {
			worldReadChan = nil
		}
		select {
		case f := <-s.sessionSafeFuChan:
			f(s)
		case p, ok := <-s.gameSocket.ReadChannel():
			if !ok {
				return
			}
			handler, found := HandleMap[p.Opcode]
			if !found {
				if s.worldSocket != nil {
					// B9: select-wrapped send so a dead worldsocket doesn't block
					// the entire session-goroutine on otherwise-harmless forwards.
					sockets.SendOrCancel(c, s.worldSocket.WriteChannel(), p)
				}
				break
			}

			pCtx, pCancel := context.WithTimeout(c, s.packetProcessTimeout)
			if err = handler.Handle(pCtx, s, p); err != nil {
				s.logger.Error().Err(err).Msgf("can't handle packet with name %s", handler.name)
				if userFriendlyErr, ok := err.(*UserFriendlyError); ok {
					if s.character != nil {
						s.SendSysMessage(userFriendlyErr.UserError)
					}
				}
			}
			pCancel()

		// worldReadChan can be nil and can be forever blocked
		case p, ok := <-worldReadChan:
			if !ok {
				s.worldSocket = nil
				s.onWorldSocketClosed()
				break
			}

			// Check if this opcode should be dropped (blacklisted)
			if OpcodeBlacklist[p.Opcode] {
				s.logger.Debug().Msgf("Dropped blacklisted opcode from worldserver: %d", p.Opcode)
				break
			}

			handler, found := HandleMap[p.Opcode]
			if !found {
				if s.gameSocket != nil {
					// B9: see comment above.
					sockets.SendOrCancel(c, s.gameSocket.WriteChannel(), p)
				}
				break
			}

			pCtx, pCancel := context.WithTimeout(c, s.packetProcessTimeout)
			if err = handler.Handle(pCtx, s, p); err != nil {
				s.logger.Error().Err(err).Msgf("can't handle packet with name %s", handler.name)
			}
			pCancel()

		case event := <-s.eventsChan:
			handleEvent(event)

		case event := <-s.channelMembership.GetEventsStream():
			handleEvent(event)

		case <-c.Done():
			return
		}
	}
}

func (s *GameSession) Login(ctx context.Context, p *packet.Packet) error {
	// Reset sending control for new login.
	s.packetSendingControl = PacketSendingControl{}

	char, socket, err := s.connectToGameServer(ctx, p.Reader().Uint64(), nil, nil)
	if err != nil {
		code := packet.LoginErrorCodeLoginFailed
		switch {
		case errors.Is(err, worldConnectErrCharacterNotFound):
			code = packet.LoginErrorCodeCharNotFound
		case errors.Is(err, worldConnectErrInstanceNotFound):
			code = packet.LoginErrorCodeNoInstanceServers
		}

		resp := packet.NewWriterWithSize(packet.SMsgCharacterLoginFailed, 1)
		resp.Uint8(uint8(code))
		s.gameSocket.Send(resp)
		return fmt.Errorf("failed to connect to game server, err: %w", err)
	}

	s.character = &LoggedInCharacter{
		GUID:                    char.GUID,
		Name:                    char.Name,
		Race:                    uint8(char.Race),
		Class:                   uint8(char.Class),
		Gender:                  uint8(char.Gender),
		Skin:                    uint8(char.Skin),
		Face:                    uint8(char.Face),
		HairStyle:               uint8(char.HairStyle),
		HairColor:               uint8(char.HairColor),
		FacialStyle:             uint8(char.FacialStyle),
		Level:                   uint8(char.Level),
		Zone:                    char.Zone,
		Map:                     char.Map,
		PositionX:               char.PositionX,
		PositionY:               char.PositionY,
		PositionZ:               char.PositionZ,
		GuildID:                 char.GuildID,
		PlayerFlags:             char.PlayerFlags,
		AtLogin:                 char.AtLogin,
		PetEntry:                char.PetEntry,
		PetModelID:              char.PetModelID,
		PetLevel:                char.PetLevel,
		Banned:                  char.Banned,
		AccountID:               char.AccountID,
		GroupMangedByGameServer: false,
	}
	s.worldSocket = socket

	err = s.eventsProducer.CharacterLoggedIn(&events.GWEventCharacterLoggedInPayload{
		RealmID:     s.realmID,
		GatewayID:   s.gatewayID,
		CharGUID:    char.GUID,
		CharName:    char.Name,
		CharRace:    uint8(char.Race),
		CharClass:   uint8(char.Class),
		CharGender:  uint8(char.Gender),
		CharLevel:   uint8(char.Level),
		CharZone:    char.Zone,
		CharMap:     char.Map,
		CharPosX:    char.PositionX,
		CharPosY:    char.PositionY,
		CharPosZ:    char.PositionZ,
		CharGuildID: char.GuildID,
		AccountID:   char.AccountID,
	})
	if err != nil {
		s.logger.Err(err).Msg("can't send login event")
	}

	s.eventsChan = s.eventsBroadcaster.RegisterCharacter(char.GUID)

	if s.character.GuildID != 0 {
		if err = s.GuildLoginCommand(ctx); err != nil {
			s.logger.Err(err).Msg("can't process guild login command")
		}
	}

	if err = s.HandleQueryNextMailTime(ctx, p); err != nil {
		return err
	}

	if err = s.LoadGroupForPlayer(ctx); err != nil {
		return err
	}

	s.channelMembership = NewChannelMembership(char.GUID, s.chatChannelsEventsBroadcaster)

	return err
}

func (s *GameSession) RealmSplit(ctx context.Context, p *packet.Packet) error {
	reader := p.Reader()
	unk := reader.Uint32()
	splitDate := "01/01/01"
	resp := packet.NewWriterWithSize(packet.SMsgRealmSplit, uint32(4+4+len(splitDate)+1))
	resp.Uint32(unk)
	resp.Uint32(0)
	resp.String(splitDate)
	s.gameSocket.Send(resp)
	return nil
}

func (s *GameSession) ReadyForAccountDataTimes(ctx context.Context, p *packet.Packet) error {
	accountData, err := s.charServiceClient.AccountDataForAccount(ctx, &pbChar.AccountDataForAccountRequest{
		Api:       root.SupportedCharServiceVer,
		AccountID: s.accountID,
		RealmID:   s.realmID,
	})
	if err != nil {
		return err
	}

	globalCacheMask := uint32(0x15)
	resp := packet.NewWriterWithSize(packet.SMsgAccountDataTimes, 4+1+4+8*4)
	resp.Uint32(uint32(time.Now().Unix()))
	resp.Uint8(1)
	resp.Uint32(globalCacheMask)
	for i := uint32(0); i < 8; i++ {
		if globalCacheMask&(uint32(1)<<i) > 0 {
			found := false
			for _, data := range accountData.AccountData {
				if data.Type == i {
					resp.Uint32(uint32(data.Time))
					found = true
					break
				}
			}
			if !found {
				resp.Uint32(0)
			}
		}
	}

	s.gameSocket.Send(resp)
	return nil
}

func (s *GameSession) HandlePing(ctx context.Context, p *packet.Packet) error {
	s.pingToWorldServerStarted = time.Now()
	if s.worldSocket != nil {
		// B9: drop ping rather than block forever if worldsocket is dead.
		sockets.SendOrCancel(ctx, s.worldSocket.WriteChannel(), p)
	} else {
		resp := packet.NewWriterWithSize(packet.SMsgPong, 4)
		resp.Uint32(p.Reader().Uint32())
		s.gameSocket.Send(resp)
	}

	return nil
}

func (s *GameSession) InterceptPong(ctx context.Context, p *packet.Packet) error {
	s.logger.Info().
		Uint32("account", s.accountID).
		Str("latency", time.Since(s.pingToWorldServerStarted).String()).
		Msg("Latency with world server")

	// B9: select-wrapped.
	sockets.SendOrCancel(ctx, s.gameSocket.WriteChannel(), p)
	return nil
}

func (s *GameSession) connectToGameServer(ctx context.Context, characterGUID uint64, mapID *uint32, preLoginHook func(sockets.Socket)) (*pbChar.LogInCharacter, sockets.Socket, error) {
	r, err := s.charServiceClient.CharactersToLoginByGUID(ctx, &pbChar.CharactersToLoginByGUIDRequest{
		Api:           root.SupportedCharServiceVer,
		CharacterGUID: characterGUID,
		RealmID:       s.realmID,
	})

	if err != nil {
		return nil, nil, fmt.Errorf("can't get characters to login, err: %w", err)
	}

	if r.Character == nil {
		return nil, nil, fmt.Errorf("char id: %q, err: %w", characterGUID, worldConnectErrCharacterNotFound)
	}

	mapIDToLogin := r.Character.Map
	if mapID != nil {
		mapIDToLogin = *mapID
	}

	serversResult, err := s.serversRegistryClient.AvailableGameServersForMapAndRealm(s.ctx, &pbServ.AvailableGameServersForMapAndRealmRequest{
		Api:     root.SupportedCharServiceVer,
		RealmID: s.realmID,
		MapID:   mapIDToLogin,
	})

	if err != nil {
		return nil, nil, fmt.Errorf("can't get available game servers for map, err: %w", err)
	}

	if len(serversResult.GameServers) == 0 {
		return nil, nil, fmt.Errorf("%w, mapID %v", worldConnectErrInstanceNotFound, mapIDToLogin)
	}

	s.gameServerGRPCConnMgr.AddAddressMapping(serversResult.GameServers[0].Address, serversResult.GameServers[0].GrpcAddress)

	s.gameServerGRPCClient, err = s.gameServerGRPCConnMgr.GRPCConnByGameServerAddress(serversResult.GameServers[0].Address)
	if err != nil {
		return nil, nil, fmt.Errorf("can't get game server grpc client, err: %w", err)
	}

	socket, err := s.connectToGameServerWithAddress(ctx, characterGUID, serversResult.GameServers[0].Address, preLoginHook)
	return r.Character, socket, err
}

func (s *GameSession) connectToGameServerWithAddress(ctx context.Context, characterGUID uint64, gameserverAddress string, preLoginHook func(sockets.Socket)) (sockets.Socket, error) {
	s.logger.Debug().
		Str("address", gameserverAddress).
		Msg("Connecting to the world server")

	socket, err := WorldSocketCreator(s.logger, gameserverAddress)
	if err != nil {
		return nil, fmt.Errorf("can't connect to the world server, err: %w", err)
	}

	go socket.ListenAndProcess(s.ctx)

	socket.SendPacket(s.authPacket)

	// B10 fix: replace blind read-then-sleep with explicit wait for
	// SMsgAuthResponse. AC sends SMSG_AUTH_RESPONSE(AUTH_OK) from
	// WorldSession::InitializeSessionCallback (src/server/game/Server/
	// WorldSession.cpp:1498 in walkline AC fork) AFTER the WorldSession
	// has been fully added to the session manager and its character-DB
	// query holder loaded. That's exactly the "session is ready"
	// signal the previous 200ms sleep was approximating.
	//
	// Bonus B-cousin fix: the legacy `if p.Opcode != SMsgAuthChallenge`
	// branch echoed unexpected packets back to the WORLDSOCKET, which
	// is almost certainly a bug (would have caused worldserver to see
	// its own packet echoed). Now we forward to the gamesocket (client)
	// for any packet that isn't SMsgAuthResponse / SMsgAuthChallenge.
	authWaitCtx, authWaitCancel := context.WithTimeout(ctx, 5*time.Second)
	defer authWaitCancel()
	authReceived := false
	for !authReceived {
		select {
		case p, open := <-socket.ReadChannel():
			if !open {
				return nil, fmt.Errorf("world socket closed before SMsgAuthResponse")
			}
			switch p.Opcode {
			case packet.SMsgAuthResponse:
				// WorldSession fully initialized. Do NOT forward to client --
				// gateway already sent client its own SMsgAuthResponse during
				// the earlier client-side auth handshake (AuthSession in
				// gamesocket.go). A duplicate would confuse the client.
				authReceived = true
			case packet.SMsgAuthChallenge:
				// Legacy branch: worldserver re-challenges gateway. Not
				// observed in standard ToCloud9+AC config but preserve
				// silent-drop behavior for safety.
			default:
				// Any other packet the worldserver sends pre-auth-response
				// (rare; possibly addon-info / warden init from C++ patches)
				// should be forwarded to the client, not echoed back to the
				// worldserver as the previous code did.
				sockets.SendOrCancel(s.ctx, s.gameSocket.WriteChannel(), p)
			}
		case <-authWaitCtx.Done():
			return nil, fmt.Errorf("timeout waiting for SMsgAuthResponse from worldserver")
		}
	}

	if preLoginHook != nil {
		preLoginHook(socket)
	}

	resp := packet.NewWriterWithSize(packet.CMsgPlayerLogin, 8)
	resp.Uint64(characterGUID)
	socket.Send(resp)

	return socket, nil
}

func (s *GameSession) processWorldPacketsInPlace(ctx context.Context, f func(*packet.Packet) (stopProcessing bool, err error)) error {
	if s.worldSocket == nil {
		return nil
	}

	for {
		select {
		case p, open := <-s.worldSocket.ReadChannel():
			if !open {
				return fmt.Errorf("world socket closed")
			}

			stop, err := f(p)
			if err != nil {
				return err
			}
			if stop {
				return nil
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *GameSession) onWorldSocketClosed() {
	if s.character == nil {
		return
	}
	// Capture player state at goroutine start. Avoids racing against the main
	// session goroutine which may mutate s.character (B14/B15 cleanup).
	snap := *s.character

	go func() {
		// B14/B15: respect session lifecycle. context.TODO() previously meant
		// reconnect attempts kept running after the gateway / session shut down.
		if s.ctx.Err() != nil {
			return
		}

		s.SendSysMessage("Lost connection with world server... trying to recover.")

		// B11: try-immediate then exponential backoff. Previous design slept
		// 2+1 seconds before even the first attempt, then 5s flat between
		// retries -> up to ~15s of dead-air UX on a transient worldserver
		// blip. Now: 1st try immediately, 2nd after 1s, 3rd after 2s, cap 5s.
		const maxRetries = 3
		const maxBackoff = 5 * time.Second
		backoff := time.Second

		var err error
		var char *pbChar.LogInCharacter
		var socket sockets.Socket

		for i := 0; i < maxRetries; i++ {
			if i > 0 {
				select {
				case <-time.After(backoff):
				case <-s.ctx.Done():
					return
				}
				if backoff < maxBackoff {
					backoff *= 2
					if backoff > maxBackoff {
						backoff = maxBackoff
					}
				}
			}

			char, socket, err = s.connectToGameServer(s.ctx, snap.GUID, nil, func(_ sockets.Socket) {
				_, saveErr := s.charServiceClient.SavePlayerPosition(s.ctx, &pbChar.SavePlayerPositionRequest{
					Api:      root.SupportedCharServiceVer,
					RealmID:  s.realmID,
					CharGUID: snap.GUID,
					MapID:    snap.Map,
					X:        snap.PositionX,
					Y:        snap.PositionY,
					Z:        snap.PositionZ,
					O:        snap.PositionO,
				})
				if saveErr != nil {
					s.logger.Error().Err(saveErr).Msg("can't save player position")
				}
			})
			if err == nil {
				break
			}
			if s.ctx.Err() != nil {
				return
			}
			s.logger.Error().Err(err).Int("attempt", i+1).Int("max", maxRetries).Msg("reconnect attempt failed")
		}

		if err != nil {
			s.SendSysMessage("Failed to recover. Returning to character screen.")
			resp := packet.NewWriterWithSize(packet.SMsgCharacterLoginFailed, 1)
			resp.Uint8(uint8(packet.LoginErrorCodeWorldServerIsDown))
			s.gameSocket.Send(resp)
			return
		}

		// Tell the client to load the new world.
		resp := packet.NewWriterWithSize(packet.SMsgNewWorld, 0)
		resp.Uint32(char.Map)
		resp.Float32(snap.PositionX)
		resp.Float32(snap.PositionY)
		resp.Float32(snap.PositionZ)
		resp.Float32(0.0)
		s.gameSocket.Send(resp)

		// Update session-owned state on the session goroutine. Drop the socket
		// if the session is shutting down (B14: no orphan resources).
		select {
		case s.sessionSafeFuChan <- func(session *GameSession) {
			if session.character != nil {
				session.worldSocket = socket
			}
			if session.showGameserverConnChangeToClient {
				session.SendSysMessage(fmt.Sprintf("Connection recovered! New gameserver: %s. Sorry for inconvenience.", socket.Address()))
			} else {
				session.SendSysMessage("Connection recovered! Sorry for inconvenience.")
			}
		}:
		case <-s.ctx.Done():
			socket.Close()
			return
		}
	}()
}

func (s *GameSession) onLoggedOut() {
	if s.character == nil {
		return
	}

	err := s.eventsProducer.CharacterLoggedOut(&events.GWEventCharacterLoggedOutPayload{
		RealmID:     s.realmID,
		GatewayID:   s.gatewayID,
		CharGUID:    s.character.GUID,
		CharName:    s.character.Name,
		CharGuildID: s.character.GuildID,
		AccountID:   s.character.AccountID,
	})
	if err != nil {
		s.logger.Err(err).Msg("can't send logout event")
	}

	s.eventsBroadcaster.UnregisterCharacter(s.character.GUID)
	s.chatChannelsEventsBroadcaster.DisconnectPlayer(s.character.GUID)
	s.channelMembership.events = nil

	s.character = nil
}

var WorldSocketCreator = worldsocket.NewWorldSocketWithAddress

// PacketSendingControl contains flags to track sending of some packets
// that needs to be sent only once or similar to that.
type PacketSendingControl struct {
	motdSent                    bool
	accountDataTimesGlobalSent  bool
	accountDataTimesPerCharSent bool
}

// LoggedInCharacter represents a character that is logged in and bound to the session.
// Some values are cached values and can be not actual values from gameserver.
type LoggedInCharacter struct {
	GUID        uint64
	Name        string
	Race        uint8
	Class       uint8
	Gender      uint8
	Skin        uint8
	Face        uint8
	HairStyle   uint8
	HairColor   uint8
	FacialStyle uint8
	Level       uint8
	Zone        uint32
	Map         uint32
	PositionX   float32
	PositionY   float32
	PositionZ   float32
	PositionO   float32
	GuildID     uint32
	PlayerFlags uint32
	AtLogin     uint32
	PetEntry    uint32
	PetModelID  uint32
	PetLevel    uint32
	Banned      bool
	AccountID   uint32

	// GroupMangedByGameServer tracks cases when player joined e.g. battleground
	// and the group is managed by game server but not group server.
	GroupMangedByGameServer bool

	ignoreNextInterceptToNewMap *uint32

	// bgInviteOrderingFix handles race conditions between Invite and JoinToQueue events
	// for battleground queuing. It contains state to ensure correct event ordering:
	//   - waitingJoinToQueue: indicates if we're waiting for a JoinToQueue event
	//   - pendingInvitePacket: stores an Invite packet that arrived before JoinToQueue
	// This prevents issues where a player might receive an Invite before their
	// JoinToQueue event is processed, which results on not displaying invite on client side.
	bgInviteOrderingFix struct {
		waitingJoinToQueue  bool
		pendingInvitePacket *packet.Packet
	}
}
