package service

import (
	"context"

	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"

	"github.com/walkline/ToCloud9/shared/events"
)

type CharactersListener struct {
	// ctx is the process-lifetime context. Used inside NATS subscription
	// callbacks (which have no caller-supplied ctx) so a SIGTERM cancels
	// any in-flight PlayerBecomeOffline calls. Previously: context.Background(). (B41)
	ctx  context.Context
	bg   BattleGroundService
	nc   *nats.Conn
	subs []*nats.Subscription
}

func NewCharactersListener(ctx context.Context, bgService BattleGroundService, nc *nats.Conn) *CharactersListener {
	return &CharactersListener{
		ctx: ctx,
		bg:  bgService,
		nc:  nc,
	}
}

func (c *CharactersListener) Listen() error {
	sb, err := c.nc.Subscribe(events.GWEventCharacterLoggedOut.SubjectName(), func(msg *nats.Msg) {
		loggedOutP := events.GWEventCharacterLoggedOutPayload{}
		_, err := events.Unmarshal(msg.Data, &loggedOutP)
		if err != nil {
			log.Error().Err(err).Msg("can't read GWEventCharacterLoggedOut (payload part) event")
			return
		}

		err = c.bg.PlayerBecomeOffline(c.ctx, loggedOutP.CharGUID, loggedOutP.RealmID)
		if err != nil {
			log.Error().Err(err).Msg("can't remove character in GWEventCharacterLoggedOut event")
			return
		}
	})
	if err != nil {
		c.unsubscribe()
		return err
	}

	c.subs = append(c.subs, sb)

	sb, err = c.nc.Subscribe(events.CharEventCharsDisconnectedUnhealthyGW.SubjectName(), func(msg *nats.Msg) {
		payload := events.CharEventCharsDisconnectedUnhealthyGWPayload{}
		_, err := events.Unmarshal(msg.Data, &payload)
		if err != nil {
			log.Error().Err(err).Msg("can't read GWEventCharacterLoggedOut (payload part) event")
			return
		}

		for _, char := range payload.CharactersGUID {
			err = c.bg.PlayerBecomeOffline(c.ctx, char, payload.RealmID)
			if err != nil {
				log.Error().Err(err).Msg("can't remove character in GWEventCharacterLoggedOut event")
			}
		}
	})
	if err != nil {
		c.unsubscribe()
		return err
	}

	c.subs = append(c.subs, sb)

	return nil
}

func (c *CharactersListener) Stop() error {
	return c.unsubscribe()
}

func (c *CharactersListener) unsubscribe() error {
	for _, sub := range c.subs {
		if err := sub.Unsubscribe(); err != nil {
			return err
		}
	}

	return nil
}
