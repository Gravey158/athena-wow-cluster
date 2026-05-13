package service

import (
	"context"
	"sync"
	"time"

	"github.com/walkline/ToCloud9/shared/events"
)

//go:generate mockery --name=OnlinePlayersCache
type OnlinePlayersCache interface {
	PlayerLoggedIn(playerGUID uint64, level, class, area uint32)
	PlayerLoggedOut(playerGUID uint64)
	GetOnlineInfo(playerGUID uint64) (OnlinePlayerInfo, bool)
	SetFriendsService(friendsService FriendsService)

	HandleCharacterLoggedIn(payload events.GWEventCharacterLoggedInPayload) error
	HandleCharacterLoggedOut(payload events.GWEventCharacterLoggedOutPayload) error
	HandleCharactersUpdates(payload events.GWEventCharactersUpdatesPayload) error
}

type onlinePlayersCacheImpl struct {
	// cacheMutex guards onlineInfoByGUID map AND friendsService.
	// friendsService is set once at startup but the NATS subscriber
	// could (in principle) fire HandleCharacterLoggedIn before main()
	// gets to SetFriendsService, so the access is synchronized. (B57)
	cacheMutex sync.RWMutex

	// onlineInfoByGUID maps player GUID to their online info
	onlineInfoByGUID map[uint64]*OnlinePlayerInfo

	// friendsService is used to notify friends about status changes
	friendsService FriendsService
}

func NewOnlinePlayersCache() OnlinePlayersCache {
	return &onlinePlayersCacheImpl{
		onlineInfoByGUID: make(map[uint64]*OnlinePlayerInfo),
	}
}

func (o *onlinePlayersCacheImpl) SetFriendsService(friendsService FriendsService) {
	o.cacheMutex.Lock()
	o.friendsService = friendsService
	o.cacheMutex.Unlock()
}

func (o *onlinePlayersCacheImpl) getFriendsService() FriendsService {
	o.cacheMutex.RLock()
	defer o.cacheMutex.RUnlock()
	return o.friendsService
}

func (o *onlinePlayersCacheImpl) PlayerLoggedIn(playerGUID uint64, level, class, area uint32) {
	o.cacheMutex.Lock()
	defer o.cacheMutex.Unlock()

	o.onlineInfoByGUID[playerGUID] = &OnlinePlayerInfo{
		GUID:   playerGUID,
		Level:  level,
		Class:  class,
		Area:   area,
		Status: 1, // online
	}
}

func (o *onlinePlayersCacheImpl) PlayerLoggedOut(playerGUID uint64) {
	o.cacheMutex.Lock()
	defer o.cacheMutex.Unlock()

	delete(o.onlineInfoByGUID, playerGUID)
}

func (o *onlinePlayersCacheImpl) GetOnlineInfo(playerGUID uint64) (OnlinePlayerInfo, bool) {
	o.cacheMutex.RLock()
	defer o.cacheMutex.RUnlock()

	info, ok := o.onlineInfoByGUID[playerGUID]
	if !ok {
		return OnlinePlayerInfo{}, false
	}
	return *info, true
}

// HandleCharacterLoggedIn handles character login event from gateway
func (o *onlinePlayersCacheImpl) HandleCharacterLoggedIn(payload events.GWEventCharacterLoggedInPayload) error {
	o.PlayerLoggedIn(payload.CharGUID, uint32(payload.CharLevel), uint32(payload.CharClass), payload.CharZone)

	// Notify friends service about login
	if fs := o.getFriendsService(); fs != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return fs.NotifyStatusChange(
			ctx,
			payload.RealmID,
			payload.CharGUID,
			1, // online
			payload.CharZone,
			uint32(payload.CharLevel),
			uint32(payload.CharClass),
		)
	}

	return nil
}

// HandleCharacterLoggedOut handles character logout event from gateway
func (o *onlinePlayersCacheImpl) HandleCharacterLoggedOut(payload events.GWEventCharacterLoggedOutPayload) error {
	o.PlayerLoggedOut(payload.CharGUID)

	// Notify friends service about logout
	if fs := o.getFriendsService(); fs != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return fs.NotifyStatusChange(
			ctx,
			payload.RealmID,
			payload.CharGUID,
			0, // offline
			0, 0, 0,
		)
	}

	return nil
}

// HandleCharactersUpdates handles character updates (zone/level/area changes) from gateway
func (o *onlinePlayersCacheImpl) HandleCharactersUpdates(payload events.GWEventCharactersUpdatesPayload) error {
	o.cacheMutex.Lock()
	defer o.cacheMutex.Unlock()

	for _, update := range payload.Updates {
		info, exists := o.onlineInfoByGUID[update.ID]
		if !exists {
			continue
		}

		// Update level
		if update.Lvl != nil {
			info.Level = uint32(*update.Lvl)
		}

		// Update area/zone (use Zone for Area field since that's what friends see)
		if update.Zone != nil {
			info.Area = *update.Zone
		} else if update.Area != nil {
			info.Area = *update.Area
		}
	}

	return nil
}
