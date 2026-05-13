package service

import (
	"encoding/json"

	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"

	eBroadcaster "github.com/walkline/ToCloud9/apps/gateway/events-broadcaster"
	"github.com/walkline/ToCloud9/shared/events"
)

type friendsNatsListener struct {
	nc          *nats.Conn
	subs        []*nats.Subscription
	broadcaster eBroadcaster.Broadcaster
}

func NewFriendsNatsListener(nc *nats.Conn, broadcaster eBroadcaster.Broadcaster) Listener {
	return &friendsNatsListener{
		nc:          nc,
		broadcaster: broadcaster,
	}
}

func (f *friendsNatsListener) Listen() error {
	err := f.newSubscribe(events.FriendEventStatusChange, func() (interface{}, func()) {
		d := &events.FriendEventStatusChangePayload{}
		return d, func() {
			f.broadcaster.NewFriendStatusChangeEvent(d)
		}
	})
	if err != nil {
		_ = f.Stop() // B52: roll back partial subscriptions.
		return err
	}

	err = f.newSubscribe(events.FriendEventAdded, func() (interface{}, func()) {
		d := &events.FriendEventAddedPayload{}
		return d, func() {
			f.broadcaster.NewFriendAddedEvent(d)
		}
	})
	if err != nil {
		_ = f.Stop()
		return err
	}

	err = f.newSubscribe(events.FriendEventRemoved, func() (interface{}, func()) {
		d := &events.FriendEventRemovedPayload{}
		return d, func() {
			f.broadcaster.NewFriendRemovedEvent(d)
		}
	})
	if err != nil {
		_ = f.Stop()
		return err
	}

	err = f.newSubscribe(events.FriendEventNoteUpdate, func() (interface{}, func()) {
		d := &events.FriendEventNoteUpdatePayload{}
		return d, func() {
			f.broadcaster.NewFriendNoteUpdateEvent(d)
		}
	})
	if err != nil {
		_ = f.Stop()
		return err
	}

	return nil
}

func (f *friendsNatsListener) Stop() error {
	for _, sub := range f.subs {
		if err := sub.Unsubscribe(); err != nil {
			return err
		}
	}
	return nil
}

func (f *friendsNatsListener) newSubscribe(event events.FriendsServiceEvent, payloadAndHandler func() (interface{}, func())) error {
	sb, err := f.nc.Subscribe(event.SubjectName(), func(msg *nats.Msg) {
		p := events.EventToReadGenericPayload{}
		err := json.Unmarshal(msg.Data, &p)
		if err != nil {
			log.Error().Err(err).Msgf("can't read %v event", event)
			return
		}

		payload, handler := payloadAndHandler()
		err = json.Unmarshal(p.Payload, payload)
		if err != nil {
			log.Error().Err(err).Msgf("can't read %d (payload part) event", event)
			return
		}

		handler()
	})
	if err != nil {
		return err
	}

	f.subs = append(f.subs, sb)
	return nil
}
