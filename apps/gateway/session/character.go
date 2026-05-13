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

	// B24 fix: signal channel for SMsgAuthResponse so we can replace the
	// blind 300ms sleep with an explicit wait for AC's WorldSession-ready
	// signal. Same approach as B10 in connectToGameServerWithAddress.
	authReady := make(chan struct{}, 1)
	waitDone := make(chan struct{})
	go func() {
		defer func() { waitDone <- struct{}{} }()

		for {
			select {
			case p, open := <-socket.ReadChannel():
				if !open {
					return
				}
				if p.Opcode == packet.SMsgAuthResponse {
					// Signal main flow that worldserver is ready for CMsgCharCreate.
					// Don't forward to client -- gateway sent its own auth response
					// during the earlier client-side handshake.
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

	// B24: wait for SMsgAuthResponse instead of magic 300ms sleep.
	// AC sends this from WorldSession::InitializeSessionCallback after the
	// WorldSession has been fully added to the session manager.
	select {
	case <-authReady:
	case <-newCtx.Done():
		sendCreateFailed()
		return fmt.Errorf("timeout waiting for worldserver auth response in CreateCharacter")
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

	// B24-cousin: same signal-channel pattern as CreateCharacter to replace
	// the magic 300ms sleep with an explicit SMsgAuthResponse wait.
	authReady := make(chan struct{}, 1)
	waitDone := make(chan struct{})
	go func() {
		defer func() { waitDone <- struct{}{} }()

		for {
			select {
			case p, open := <-socket.ReadChannel():
				if !open {
					return
				}
				if p.Opcode == packet.SMsgAuthResponse {
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

	// B24-cousin: wait for SMsgAuthResponse instead of magic 300ms sleep.
	select {
	case <-authReady:
	case <-newCtx.Done():
		sendDelFailed()
		return fmt.Errorf("timeout waiting for worldserver auth response in DeleteCharacter")
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
