package session

import (
	"context"
	"fmt"
	"time"

	root "github.com/walkline/ToCloud9/apps/gateway"
	"github.com/walkline/ToCloud9/apps/gateway/packet"
	pbChar "github.com/walkline/ToCloud9/gen/characters/pb"
	pbServ "github.com/walkline/ToCloud9/gen/servers-registry/pb"
	"github.com/walkline/ToCloud9/apps/gateway/sockets"
)

func (s *GameSession) CharactersList(ctx context.Context, p *packet.Packet) error {
	if s.worldSocket != nil {
		socket := s.worldSocket
		s.worldSocket = nil
		socket.Close()
	}

	if s.character != nil {
		s.onLoggedOut()
	}

	r, err := s.charServiceClient.CharactersToLoginForAccount(ctx, &pbChar.CharactersToLoginForAccountRequest{
		Api:       root.SupportedCharServiceVer,
		AccountID: s.accountID,
		RealmID:   s.realmID,
	})
	if err != nil {
		return err
	}

	resp := packet.NewWriterWithSize(packet.SMsgCharEnum, 0)
	resp.Uint8(uint8(len(r.Characters)))
	for _, character := range r.Characters {
		resp.Uint64(character.GUID)
		resp.String(character.Name)
		resp.Uint8(uint8(character.Race))
		resp.Uint8(uint8(character.Class))
		resp.Uint8(uint8(character.Gender))

		resp.Uint8(uint8(character.Skin))
		resp.Uint8(uint8(character.Face))
		resp.Uint8(uint8(character.HairStyle))
		resp.Uint8(uint8(character.HairColor))
		resp.Uint8(uint8(character.FacialStyle))

		resp.Uint8(uint8(character.Level))
		resp.Uint32(character.Zone)
		resp.Uint32(character.Map)

		resp.Float32(character.PositionX)
		resp.Float32(character.PositionY)
		resp.Float32(character.PositionZ)

		resp.Uint32(character.GuildID)

		// TODO: provide correct value
		resp.Uint32(33554432) // character flags

		resp.Uint32(0) // CHAR_CUSTOMIZE_FLAG_NONE

		// TODO: provide correct value
		resp.Uint8(0) // First login

		resp.Uint32(character.PetModelID)
		resp.Uint32(character.PetLevel)
		resp.Uint32(0) // petFamily

		for _, equipment := range character.Equipments {
			resp.Uint32(equipment.DisplayInfoID)
			resp.Uint8(uint8(equipment.InventoryType))
			resp.Uint32(equipment.EnchantmentID)
		}
	}

	s.gameSocket.Send(resp)
	return nil
}

func (s *GameSession) CreateCharacter(ctx context.Context, p *packet.Packet) error {
	sendCreateFailed := func() {
		const createFailedCode = uint8(0x31)
		resp := packet.NewWriterWithSize(packet.SMsgCharCreate, 1)
		resp.Uint8(createFailedCode)
		s.gameSocket.Send(resp)
	}

	serverResult, err := s.serversRegistryClient.RandomGameServerForRealm(ctx, &pbServ.RandomGameServerForRealmRequest{
		Api:     root.SupportedServerRegistryVer,
		RealmID: s.realmID,
	})
	if err != nil {
		sendCreateFailed()
		return err
	}

	if serverResult.GameServer == nil {
		sendCreateFailed()
		return fmt.Errorf("no available game servers to handle 0x%X packet", uint16(p.Opcode))
	}

	socket, err := WorldSocketCreator(s.logger, serverResult.GameServer.Address)
	if err != nil {
		sendCreateFailed()
		return fmt.Errorf("can't connect to the world server, err: %w", err)
	}

	go socket.ListenAndProcess(s.ctx)
	newCtx, cancel := context.WithTimeout(s.ctx, time.Second*20)
	defer cancel()

	// B71 (regression fix for B24): cluster-mode AC does NOT send
	// SMSG_AUTH_RESPONSE -- WorldSession.cpp:1505 gates it on
	// !ClusterModeEnabled(). Wait for the initial SMsgAuthChallenge instead
	// (the only packet AC emits on connection in cluster mode), then short
	// sleep for the InitializeSessionCallback DB roundtrip.
	authReady := make(chan struct{}, 1)
	waitDone := make(chan struct{})
	go func() {
		defer func() { waitDone <- struct{}{} }()
		signalled := false
		for {
			select {
			case p, open := <-socket.ReadChannel():
				if !open {
					return
				}
				if !signalled && (p.Opcode == packet.SMsgAuthChallenge || p.Opcode == packet.SMsgAuthResponse) {
					signalled = true
					select {
					case authReady <- struct{}{}:
					default:
					}
					continue
				}
				// B9: select-wrapped so a slow/dead gamesocket doesn't block.
				sockets.SendOrCancel(s.ctx, s.gameSocket.WriteChannel(), p)
				if p.Opcode == packet.SMsgCharCreate {
					socket.Close()
					return
				}

			case <-newCtx.Done():
				// B23 (same as B7): close the one-off `socket` created in this
				// function, NOT the player's main worldserver connection
				// (s.worldSocket). Previously a timeout in char-create or
				// char-delete kicked the player off their actual world session.
				socket.Close()
				return
			}
		}
	}()

	socket.SendPacket(s.authPacket)

	select {
	case <-authReady:
	case <-newCtx.Done():
		sendCreateFailed()
		return fmt.Errorf("timeout waiting for worldserver initial packet in CreateCharacter")
	}
	// Short sleep approximating WorldSession::InitializeSession's async
	// character-DB query holder load -- no deterministic ack in cluster mode.
	select {
	case <-time.After(200 * time.Millisecond):
	case <-newCtx.Done():
		sendCreateFailed()
		return newCtx.Err()
	}

	socket.SendPacket(p)

	<-waitDone

	select {
	case <-newCtx.Done():
		sendCreateFailed()
		return fmt.Errorf("character creation timeouted, gameserver: %s", serverResult.GameServer.Address)
	default:
	}

	return nil
}

func (s *GameSession) DeleteCharacter(ctx context.Context, p *packet.Packet) error {
	sendDelFailed := func() {
		const deleteFailedCode = uint8(0x48)
		resp := packet.NewWriterWithSize(packet.SMsgCharDelete, 1)
		resp.Uint8(deleteFailedCode)
		s.gameSocket.Send(resp)
	}

	serverResult, err := s.serversRegistryClient.RandomGameServerForRealm(ctx, &pbServ.RandomGameServerForRealmRequest{
		Api:     root.SupportedServerRegistryVer,
		RealmID: s.realmID,
	})
	if err != nil {
		sendDelFailed()
		return err
	}

	if serverResult.GameServer == nil {
		sendDelFailed()
		return fmt.Errorf("no available game servers to handle 0x%X packet", uint16(p.Opcode))
	}

	socket, err := WorldSocketCreator(s.logger, serverResult.GameServer.Address)
	if err != nil {
		sendDelFailed()
		return fmt.Errorf("can't connect to the world server, err: %w", err)
	}

	go socket.ListenAndProcess(s.ctx)
	newCtx, cancel := context.WithTimeout(s.ctx, time.Second*5)
	defer cancel()

	// B71-cousin (regression fix): see CreateCharacter for full rationale --
	// cluster-mode AC suppresses SMsgAuthResponse, so we wait for the
	// initial SMsgAuthChallenge and short-sleep instead.
	authReady := make(chan struct{}, 1)
	waitDone := make(chan struct{})
	go func() {
		defer func() { waitDone <- struct{}{} }()
		signalled := false
		for {
			select {
			case p, open := <-socket.ReadChannel():
				if !open {
					return
				}
				if !signalled && (p.Opcode == packet.SMsgAuthChallenge || p.Opcode == packet.SMsgAuthResponse) {
					signalled = true
					select {
					case authReady <- struct{}{}:
					default:
					}
					continue
				}
				// B9: select-wrapped so a slow/dead gamesocket doesn't block.
				sockets.SendOrCancel(s.ctx, s.gameSocket.WriteChannel(), p)
				if p.Opcode == packet.SMsgCharDelete {
					socket.Close()
					return
				}

			case <-newCtx.Done():
				// B23 (same as B7): close the one-off `socket` created in this
				// function, NOT the player's main worldserver connection
				// (s.worldSocket). Previously a timeout in char-create or
				// char-delete kicked the player off their actual world session.
				socket.Close()
				return
			}
		}
	}()

	socket.SendPacket(s.authPacket)

	select {
	case <-authReady:
	case <-newCtx.Done():
		sendDelFailed()
		return fmt.Errorf("timeout waiting for worldserver initial packet in DeleteCharacter")
	}
	// Same short-sleep approximation as CreateCharacter.
	select {
	case <-time.After(200 * time.Millisecond):
	case <-newCtx.Done():
		sendDelFailed()
		return newCtx.Err()
	}

	socket.SendPacket(p)

	<-waitDone

	select {
	case <-newCtx.Done():
		sendDelFailed()
		return fmt.Errorf("character deletion timeouted, gameserver: %s", serverResult.GameServer.Address)
	default:
	}

	// B25 (deferred): the "delete command may take some time on worldserver
	// side" sleep is a race-by-sleep waiting for AC to finish the DB
	// transaction. Different shape from B10/B24 -- no specific opcode signals
	// completion, AC just runs the delete-character SQL async. A proper fix
	// would need either an AC-side ack opcode (cross-language) or polling
	// the characters DB for absence. Defer.
	time.Sleep(time.Second * 1)

	return nil
}
