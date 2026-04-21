package config

import (
	"fmt"
	"time"

	"github.com/caarlos0/env/v11"
)

type Config struct {
	TelegramBotToken   string        `env:"TELEGRAM_BOT_TOKEN,required"`
	NationalRailToken  string        `env:"NATIONAL_RAIL_TOKEN,required"`
	DatabaseURL        string        `env:"DATABASE_URL,required"`
	RedisURL           string        `env:"REDIS_URL,required"`
	LogLevel           string        `env:"LOG_LEVEL" envDefault:"info"`
	DaemonTickInterval time.Duration `env:"DAEMON_TICK_INTERVAL" envDefault:"2m"`
	RTTToken           string        `env:"RTT_TOKEN,required"`
}

var validLogLevels = map[string]bool{
	"debug": true,
	"info":  true,
	"warn":  true,
	"error": true,
}

func Load() (*Config, error) {
	cfg, err := env.ParseAs[Config]()
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}

	if !validLogLevels[cfg.LogLevel] {
		return nil, fmt.Errorf("invalid LOG_LEVEL %q: must be one of debug, info, warn, error", cfg.LogLevel)
	}

	return &cfg, nil
}
