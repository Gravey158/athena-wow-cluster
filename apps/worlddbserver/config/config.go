package config

import (
	"github.com/walkline/ToCloud9/shared/config"
)

// Config is config of application
type Config struct {
	config.Logging `yaml:"logging"`

	// Port is the port the gRPC server listens on.
	Port string `yaml:"port" env:"PORT" env-default:"8993"`

	// WorldDBConnection is the MySQL connection string for acore_world.
	// World DB is read-only from this service's perspective; ToCloud9
	// services that mutate world_db are out of scope for the POC.
	WorldDBConnection string `yaml:"worldDB" env:"WORLD_DB_CONNECTION" env-default:"trinity:trinity@tcp(127.0.0.1:3306)/acore_world"`

	// NatsURL is nats connection url -- reserved for Phase 3 invalidation
	// broadcast. Not used in Phase 1.
	NatsURL string `yaml:"natsUrl" env:"NATS_URL" env-default:"nats://nats:4222"`
}

// LoadConfig loads config from env variables.
func LoadConfig() (*Config, error) {
	var c struct {
		Root Config `yaml:"worlddb"`
	}

	err := config.LoadConfig(&c)
	if err != nil {
		return nil, err
	}

	return &c.Root, nil
}
