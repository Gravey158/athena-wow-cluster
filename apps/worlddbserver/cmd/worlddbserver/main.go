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
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"

	"github.com/walkline/ToCloud9/apps/worlddbserver/config"
	"github.com/walkline/ToCloud9/apps/worlddbserver/repo"
	"github.com/walkline/ToCloud9/apps/worlddbserver/server"
	"github.com/walkline/ToCloud9/gen/worlddb/pb"
)

func main() {
	cfg, err := config.LoadConfig()
	if err != nil {
		panic(err)
	}

	log.Logger = cfg.Logger()

	mainContext, mainCancel := context.WithCancel(context.Background())
	defer mainCancel()

	wdb, err := sql.Open("mysql", cfg.WorldDBConnection)
	if err != nil {
		log.Fatal().Err(err).Msg("can't connect to world db")
	}
	defer wdb.Close()
	configureDBConn(wdb)

	srv := server.NewWorldDBServer()

	// Load creature_template into process memory before the listener starts.
	// Worldservers that wake up before the load finishes get
	// "creature_template not yet loaded" -- they retry; we don't want to
	// serve partial state.
	loadCtx, loadCancel := context.WithTimeout(mainContext, time.Minute*5)
	if err := srv.LoadCreatureTemplates(loadCtx, repo.NewCreatureTemplateMySQLRepo(wdb)); err != nil {
		loadCancel()
		log.Fatal().Err(err).Msg("can't load creature_template")
	}
	loadCancel()

	lis, err := net.Listen("tcp4", ":"+cfg.Port)
	if err != nil {
		log.Fatal().Err(err).Msg("can't listen for incoming connections")
	}

	grpcServer := grpc.NewServer()
	pb.RegisterWorldDBServiceServer(grpcServer, srv)

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

	log.Info().Str("address", lis.Addr().String()).Msg("🚀 WorldDB Service started!")

	if err := grpcServer.Serve(lis); err != nil {
		log.Fatal().Err(err).Msg("couldn't serve")
	}

	wg.Wait()

	log.Info().Msg("👍 Server successfully stopped.")
}

func configureDBConn(db *sql.DB) {
	// world_db is read-mostly; we keep a small pool. The hot path is in-process
	// snapshot lookup, MySQL is only touched at boot + refresh.
	db.SetMaxIdleConns(2)
	db.SetMaxOpenConns(5)
	db.SetConnMaxLifetime(time.Minute * 4)
	db.SetConnMaxIdleTime(time.Minute * 8)
}
