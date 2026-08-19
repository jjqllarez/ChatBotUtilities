// simchat: driver de pruebas del bot SIN cliente de WhatsApp.
//
// Inyecta mensajes directamente en el pipeline real (handleMessageV2) del
// proceso: ruteo determinista, flujos (catálogo/cotización/cliente), LLM de
// OpenRouter y las tablas reales de Supabase (empleados, bot_chat_history,
// bot_chat_state, bot_flows, cotizaciones...). El bot NO abre socket ni envía
// nada por WhatsApp: las respuestas se registran en consola ([SIM] -> ...) y
// los adjuntos (card PNG, PDF) se guardan en ./sim_out/.
//
// Uso:
//
//	simchat -phone 584248821071 "Catalogo" "8" "1" "Id: 24"
//	echo -e "Catalogo\n8\n1" | simchat -phone 584248821071
//
// Requiere el .env con SUPABASE_URL/SERVICE_ROLE_KEY/OPENROUTER_* (igual que
// el bot). No compite con el bot de producción: nunca conecta whatsmeow.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"bot/internal/bot"
	"bot/internal/config"
	"bot/internal/empleados"
	"bot/internal/llm"
	"bot/internal/supabase"
)

func main() {
	var phone string
	flag.StringVar(&phone, "phone", "584248821071", "número de teléfono del empleado (dígitos, con país)")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error de configuración:", err)
		os.Exit(1)
	}

	supa := supabase.NewClient(cfg.SupabaseURL, cfg.SupabaseServiceKey)

	var llmClient *llm.Client
	if cfg.OpenRouterKey != "" {
		llmClient = llm.NewClient(cfg.OpenRouterKey, cfg.OpenRouterModel)
		llmClient.SetLogger(log.New(os.Stderr, "[simchat llm] ", log.LstdFlags))
	}

	// Mismo container de sesión que el bot (solo lectura; nunca se conecta).
	container := supabase.NewContainer(supa, nil)

	b, err := bot.New(container, supa, "8099", llmClient)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error inicializando bot:", err)
		os.Exit(1)
	}
	b.ForceSim(true)
	b.RegisterFlow(bot.NewCotizacionFlow(b))
	b.RegisterFlow(bot.NewCatalogoFlow(b))
	b.RegisterFlow(bot.NewRegistrarClienteFlow(b))

	msgs := flag.Args()
	if len(msgs) == 0 {
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			t := strings.TrimSpace(sc.Text())
			if t != "" {
				msgs = append(msgs, t)
			}
		}
	}

	ctx := context.Background()
	emp, err := empleados.LookupByPhone(ctx, supa, phone)
	if err != nil {
		fmt.Fprintln(os.Stderr, "empleados:", err)
		os.Exit(1)
	}
	if emp == nil {
		fmt.Fprintf(os.Stderr, "No hay empleado activo con teléfono %s\n", phone)
		os.Exit(1)
	}
	fmt.Printf("== simchat (sin WhatsApp) ==\nempleado: %s (socio %d)\n\n", emp.NombreCompleto, emp.SocioComercial)

	for i, msg := range msgs {
		fmt.Printf("[%d] >>> %s\n", i+1, msg)
		b.SimulateMessage(phone, msg)
		// Dejar que el outbox drene y el LLM termine.
		time.Sleep(1 * time.Second)
	}
	fmt.Println("\n== fin. Respuestas en [SIM] -> ... y adjuntos en ./sim_out/ ==")
	time.Sleep(2 * time.Second)
}
