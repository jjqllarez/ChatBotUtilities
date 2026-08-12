package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"bot/internal/bot"
	"bot/internal/config"
	"bot/internal/llm"
	"bot/internal/supabase"
)

var logger = log.New(os.Stderr, "", log.LstdFlags)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error de configuración:", err)
		os.Exit(1)
	}

	supa := supabase.NewClient(cfg.SupabaseURL, cfg.SupabaseServiceKey)
	container := supabase.NewContainer(supa, nil)

	var llmClient *llm.Client
	if cfg.OpenRouterKey != "" {
		llmClient = llm.NewClient(cfg.OpenRouterKey, cfg.OpenRouterModel)
		logger.Printf("LLM habilitado (modelo: %s)", cfg.OpenRouterModel)
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

	if err := b.Run(stop); err != nil {
		fmt.Fprintln(os.Stderr, "Bot terminó con error:", err)
		os.Exit(1)
	}
}
