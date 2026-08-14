package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"bot/internal/bot"
	"bot/internal/config"
	"bot/internal/llm"
	"bot/internal/supabase"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"
	_ "modernc.org/sqlite"
)

var logger = log.New(os.Stderr, "", log.LstdFlags)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error de configuración:", err)
		os.Exit(1)
	}

	if cfg.BotRole == "probe" {
		if err := runProbe(cfg); err != nil {
			fmt.Fprintln(os.Stderr, "Probe:", err)
			os.Exit(1)
		}
		return
	}

	supa := supabase.NewClient(cfg.SupabaseURL, cfg.SupabaseServiceKey)

	var container bot.DeviceContainer
	if cfg.WhatsmeowDB != "" {
		sqlc, err := sqlstore.New(context.Background(), "sqlite",
			"file:"+cfg.WhatsmeowDB+"?_pragma=foreign_keys(1)",
			waLog.Stdout("sqlstore", "WARN", true))
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error abriendo SQLite:", err)
			os.Exit(1)
		}
		container = sqlc
		logger.Printf("Almacén de sesión: SQLite (%s)", cfg.WhatsmeowDB)
	} else {
		container = supabase.NewContainer(supa, nil)
		logger.Printf("Almacén de sesión: Supabase")
	}

	var llmClient *llm.Client
	if cfg.OpenRouterKey != "" {
		llmClient = llm.NewClient(cfg.OpenRouterKey, cfg.OpenRouterModel)
		llmClient.SetLogger(logger)
		logger.Printf("LLM habilitado (modelos: %s)", strings.Join(llmClient.Models(), ", "))
	} else {
		logger.Printf("LLM deshabilitado (sin OPENROUTER_API_KEY)")
	}

	b, err := bot.New(container, supa, cfg.QRPort, llmClient)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error inicializando bot:", err)
		os.Exit(1)
	}

	stop := make(chan struct{})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		close(stop)
	}()

	// Registrar flujos en el registry (agregar nuevos flujos aqui).
	b.RegisterFlow(bot.NewCotizacionFlow(b))
	b.RegisterFlow(bot.NewCatalogoFlow(b))
	b.RegisterFlow(bot.NewRegistrarClienteFlow(b))

	if err := b.Run(stop); err != nil {
		fmt.Fprintln(os.Stderr, "Bot terminó con error:", err)
		os.Exit(1)
	}
}
