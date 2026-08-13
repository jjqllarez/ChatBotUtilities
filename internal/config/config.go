package config

import (
	"fmt"
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	SupabaseURL        string
	SupabaseServiceKey string
	SupabaseAnonKey    string
	StorageBucket      string
	QRPort             string

	OpenRouterKey   string
	OpenRouterModel string

	WhatsmeowDB string

	BotRole     string
	ProbeTarget string
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	cfg := &Config{
		SupabaseURL:        os.Getenv("SUPABASE_URL"),
		SupabaseServiceKey: os.Getenv("SUPABASE_SERVICE_ROLE_KEY"),
		SupabaseAnonKey:    os.Getenv("SUPABASE_ANON_KEY"),
		StorageBucket:      os.Getenv("SUPABASE_STORAGE_BUCKET"),
		QRPort:             os.Getenv("QR_PORT"),
		OpenRouterKey:      os.Getenv("OPENROUTER_API_KEY"),
		OpenRouterModel:    os.Getenv("OPENROUTER_MODEL"),
		WhatsmeowDB:        os.Getenv("WHATSMEOW_DB"),
		BotRole:            os.Getenv("BOT_ROLE"),
		ProbeTarget:        os.Getenv("PROBE_TARGET"),
	}
	if cfg.QRPort == "" {
		cfg.QRPort = "8080"
	}
	if cfg.OpenRouterModel == "" {
		cfg.OpenRouterModel = "openrouter/auto"
	}

	if cfg.BotRole != "probe" {
		if cfg.SupabaseURL == "" {
			return nil, fmt.Errorf("SUPABASE_URL no está definido en .env")
		}
		if cfg.SupabaseServiceKey == "" {
			return nil, fmt.Errorf("SUPABASE_SERVICE_ROLE_KEY no está definido en .env")
		}
	}
	return cfg, nil
}
