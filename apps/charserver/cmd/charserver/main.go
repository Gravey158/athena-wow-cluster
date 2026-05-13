package main

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"

	"github.com/walkline/ToCloud9/apps/charserver"
	"github.com/walkline/ToCloud9/apps/charserver/config"
	"github.com/walkline/ToCloud9/apps/charserver/repo"
	"github.com/walkline/ToCloud9/apps/charserver/server"
	"github.com/walkline/ToCloud9/apps/charserver/service"
	"github.com/walkline/ToCloud9/gen/characters/pb"
	"github.com/walkline/ToCloud9/shared/events"
	shrepo "github.com/walkline/ToCloud9/shared/repo"
)

func main() {
	conf, err := config.LoadConfig()
	if err != nil {
		panic(err)
	}

	log.Logger = conf.Logger()

	// B59: process-lifetime context for ctx-aware shutdown of NATS
	// listener callbacks. Previously each listener used context.TODO()
	// for its repo writes.
	mainContext, mainCancel := context.WithCancel(context.Background())
	defer mainCancel()

	// nats setup
	nc, err := nats.Connect(
		conf.NatsURL,
		nats.PingInterval(20*time.Second),
		nats.MaxPingsOutstanding(5),
		nats.Timeout(10*time.Second),
		nats.Name("charserver"),
	)
	if err != nil {
		log.Fatal().Err(err).Msg("can't connect to the Nats")
	}
	defer nc.Close()

	lis, err := net.Listen("tcp4", ":"+conf.Port)
	if err != nil {
		log.Fatal().Err(err).Msg("can't start listening")
	}

	charDB := shrepo.NewCharactersDB()
	for realmID, connStr := range conf.CharDBConnection {
		cdb, err := sql.Open("mysql", connStr)
		if err != nil {
			log.Fatal().Err(err).Uint32("realmID", realmID).Msg("can't connect to char db")
		}
		configureDBConn(cdb)
		charDB.SetDBForRealm(realmID, cdb)
	}

	wdb, err := sql.Open("mysql", conf.WorldDBConnection)
	if err != nil {
		log.Fatal().Err(err).Msg("can't connect to world db")
	}

	configureDBConn(wdb)

	itemsTemplate, err := repo.NewItemsTemplateCache(wdb)
	if err != nil {
		panic(err)
	}

	charRepo := repo.NewCharactersMYSQL(charDB)

	onlineCharsRepo := repo.NewCharactersOnlineInMem()

	// Friends initialization
	friendsOnlineCache := service.NewOnlinePlayersCache()
	friendsEventsProducer := events.NewFriendsServiceProducerNatsJSON(nc, charserver.Ver)
	friendsService := service.NewFriendsService(charRepo, friendsOnlineCache, friendsEventsProducer)
	friendsOnlineCache.SetFriendsService(friendsService)

	// Composite handlers to call both onlineCharsRepo and friendsOnlineCache
	compositeLoggedInHandler := &compositeLoggedInHandler{
		handlers: []events.GWCharacterLoggedInHandler{onlineCharsRepo, friendsOnlineCache},
	}
	compositeLoggedOutHandler := &compositeLoggedOutHandler{
		handlers: []events.GWCharacterLoggedOutHandler{onlineCharsRepo, friendsOnlineCache},
	}
	compositeUpdatesHandler := &compositeCharsUpdatesHandler{
		handlers: []events.GWCharactersUpdatesHandler{onlineCharsRepo, friendsOnlineCache},
	}

	gwEventsConsumer := events.NewGatewayConsumer(
		nc,
		events.WithGWConsumerLoggedInHandler(compositeLoggedInHandler),
		events.WithGWConsumerLoggedOutHandler(compositeLoggedOutHandler),
		events.WithGWConsumerCharsUpdatesHandler(compositeUpdatesHandler),
	)
	err = gwEventsConsumer.Listen()
	if err != nil {
		log.Fatal().Err(err).Msg("can't listen to gateway updates")
	}
	defer gwEventsConsumer.Stop()

	srHandler := service.NewServersRegistryListener(mainContext, onlineCharsRepo, events.NewCharactersServiceProducerNatsJSON(nc, charserver.Ver), nc)
	err = srHandler.Listen()
	if err != nil {
		log.Fatal().Err(err).Msg("can't listen to servers registry updates")
	}
	defer srHandler.Stop()

	grpcServer := grpc.NewServer()
	pb.RegisterCharactersServiceServer(grpcServer, server.NewCharServer(charRepo, onlineCharsRepo, onlineCharsRepo, itemsTemplate, friendsService))

	// B56: previously charserver had no SIGTERM handler -- the container
	// runtime SIGKILL'd in-flight gRPC writes. Now gracefully stops the
	// gRPC server, NATS subscribers (also B52-protected for partial-listen
	// rollback) and closes the conn.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		sig := <-sigCh
		fmt.Println("")
		log.Info().Msgf("🧨 Got signal %v, attempting graceful shutdown...", sig)
		mainCancel()
		grpcServer.GracefulStop()
	}()

	log.Info().Str("address", lis.Addr().String()).Msg("🚀 Characters Server Started!")

	if err := grpcServer.Serve(lis); err != nil {
		log.Fatal().Err(err).Msg("couldn't serve")
	}

	wg.Wait()

	log.Info().Msg("👍 Server successfully stopped.")
}

func configureDBConn(db *sql.DB) {
	db.SetMaxIdleConns(5)
	db.SetMaxOpenConns(10)
	db.SetConnMaxLifetime(time.Minute * 4)
	db.SetConnMaxIdleTime(time.Minute * 8)
}

// Composite handlers to call multiple event handlers
// Needed because GatewayConsumer only supports one handler per event type

type compositeLoggedInHandler struct {
	handlers []events.GWCharacterLoggedInHandler
}

func (c *compositeLoggedInHandler) HandleCharacterLoggedIn(payload events.GWEventCharacterLoggedInPayload) error {
	for _, h := range c.handlers {
		if err := h.HandleCharacterLoggedIn(payload); err != nil {
			log.Error().Err(err).Msg("composite handler: error in HandleCharacterLoggedIn")
		}
	}
	return nil
}

type compositeLoggedOutHandler struct {
	handlers []events.GWCharacterLoggedOutHandler
}

func (c *compositeLoggedOutHandler) HandleCharacterLoggedOut(payload events.GWEventCharacterLoggedOutPayload) error {
	for _, h := range c.handlers {
		if err := h.HandleCharacterLoggedOut(payload); err != nil {
			log.Error().Err(err).Msg("composite handler: error in HandleCharacterLoggedOut")
		}
	}
	return nil
}

type compositeCharsUpdatesHandler struct {
	handlers []events.GWCharactersUpdatesHandler
}

func (c *compositeCharsUpdatesHandler) HandleCharactersUpdates(payload events.GWEventCharactersUpdatesPayload) error {
	for _, h := range c.handlers {
		if err := h.HandleCharactersUpdates(payload); err != nil {
			log.Error().Err(err).Msg("composite handler: error in HandleCharactersUpdates")
		}
	}
	return nil
}
