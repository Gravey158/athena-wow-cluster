package service

import (
	"context"

	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"

	"github.com/walkline/ToCloud9/shared/events"
)

type ChannelsListener struct {
	// B58: process-lifetime ctx for sync-join/leave callback DB writes.
	// Previously the NATS callbacks used context.TODO() so SIGTERM could
	// not abort an in-flight GetOrCreateChannel (which does several DB
	// reads + an INSERT) or LeaveChannel (which writes through to the
	// member table).
	ctx        context.Context
	serviceID  string
	channelMgr *ChannelManager
	nc         *nats.Conn
	subs       []*nats.Subscription
}

func NewChannelsListener(ctx context.Context, serviceID string, channelMgr *ChannelManager, nc *nats.Conn) *ChannelsListener {
	return &ChannelsListener{
		ctx:        ctx,
		serviceID:  serviceID,
		channelMgr: channelMgr,
		nc:         nc,
	}
}

func (c *ChannelsListener) Listen() error {
	sb, err := c.nc.Subscribe(events.ChatEventChannelJoined.SubjectName("ALL"), func(msg *nats.Msg) {
		payload := events.ChatEventChannelJoinedPayload{}
		if _, err := events.Unmarshal(msg.Data, &payload); err != nil {
			log.Error().Err(err).Msg("can't read ChatEventChannelJoined event")
			return
		}

		if payload.ServiceID == c.serviceID {
			return
		}

		ch, err := c.channelMgr.GetOrCreateChannel(
			c.ctx, payload.RealmID, payload.ChannelName,
			payload.ChannelID, 0, "", ChannelFlagCustom,
		)
		if err != nil {
			log.Error().Err(err).Str("channel", payload.ChannelName).Msg("sync: failed to get/create channel")
			return
		}

		if err := ch.JoinChannel(c.ctx, c.channelMgr, payload.RealmID, payload.PlayerGUID, payload.PlayerName, ""); err != nil {
			log.Debug().Err(err).Str("channel", payload.ChannelName).Uint64("player", payload.PlayerGUID).Msg("sync: join skipped")
		}
	})
	if err != nil {
		return err
	}
	c.subs = append(c.subs, sb)

	sb, err = c.nc.Subscribe(events.ChatEventChannelLeft.SubjectName("ALL"), func(msg *nats.Msg) {
		payload := events.ChatEventChannelLeftPayload{}
		if _, err := events.Unmarshal(msg.Data, &payload); err != nil {
			log.Error().Err(err).Msg("can't read ChatEventChannelLeft event")
			return
		}

		if payload.ServiceID == c.serviceID {
			return
		}

		ch := c.channelMgr.GetChannel(payload.RealmID, payload.ChannelName, 0)
		if ch == nil {
			return
		}

		_, err = ch.LeaveChannel(c.ctx, c.channelMgr, payload.RealmID, payload.PlayerGUID)
		if err != nil {
			log.Debug().Err(err).Str("channel", payload.ChannelName).Uint64("player", payload.PlayerGUID).Msg("sync: leave skipped")
		}
	})
	if err != nil {
		c.unsubscribe()
		return err
	}
	c.subs = append(c.subs, sb)

	return nil
}

func (c *ChannelsListener) Stop() error {
	return c.unsubscribe()
}

func (c *ChannelsListener) unsubscribe() error {
	for _, sub := range c.subs {
		if err := sub.Unsubscribe(); err != nil {
			return err
		}
	}
	return nil
}
