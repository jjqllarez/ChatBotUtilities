package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"bot/internal/config"

	"github.com/skip2/go-qrcode"
	"go.mau.fi/whatsmeow"
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

// runProbe conecta whatsmeow con TU número (sesión local en SQLite) y actúa
// como cliente de pruebas: /send envía mensajes al bot del VPS y las respuestas
// recibidas se imprimen en el log para depurar en vivo.
func runProbe(cfg *config.Config) error {
	plog, closeLog, err := probeLogger(cfg)
	if err != nil {
		return err
	}
	defer closeLog()

	ctx := context.Background()

	sqlc, err := sqlstore.New(ctx, "sqlite", "file:"+cfg.WhatsmeowDB+"?_pragma=foreign_keys(1)",
		waLog.Stdout("sqlstore", "WARN", true))
	if err != nil {
		return fmt.Errorf("abriendo SQLite: %w", err)
	}
	devices, err := sqlc.GetAllDevices(ctx)
	if err != nil {
		return fmt.Errorf("cargando dispositivos: %w", err)
	}
	var device *store.Device
	if len(devices) > 0 {
		device = devices[0]
		plog.Printf("Sesión recuperada: %s", device.ID)
	} else {
		device = sqlc.NewDevice()
		plog.Printf("Sin sesión; se generará un QR para vincular")
	}

	cl := whatsmeow.NewClient(device, waLogAdapter{plog})

	var qrMu sync.Mutex
	var latestQR string
	disconnected := make(chan struct{}, 1)

	cl.AddEventHandler(func(ev interface{}) {
		switch v := ev.(type) {
		case *events.QR:
			if len(v.Codes) == 0 {
				return
			}
			qrMu.Lock()
			latestQR = v.Codes[0]
			qrMu.Unlock()
			plog.Printf("QR listo: http://localhost:%s/qr (escanea con tu teléfono)", cfg.QRPort)
		case *events.PairSuccess:
			plog.Printf("Vinculación exitosa")
		case *events.Connected:
			plog.Printf("Conectado como %s", cl.Store.ID.String())
		case *events.Disconnected:
			plog.Printf("Desconectado")
			select {
			case disconnected <- struct{}{}:
			default:
			}
		case *events.LoggedOut:
			plog.Printf("Sesión cerrada desde WhatsApp")
		case *events.Receipt:
			plog.Printf("RECIBO %s de %s para mensajes %v", v.Type, v.Sender.String(), v.MessageIDs)
		case *events.Message:
			from := v.Info.Sender.String()
			plog.Printf("MENSAJE de %s (desdeMi=%v): %s", from, v.Info.IsFromMe, probeText(v.Message))
		}
	})

	go func() {
		for {
			if err := cl.Connect(); err != nil {
				plog.Printf("Error conectando: %v (reintento en 5s)", err)
				time.Sleep(5 * time.Second)
				continue
			}
			<-disconnected
			plog.Printf("Reconectando...")
		}
	}()

	mux := http.NewServeMux()
	var sendMu sync.Mutex
	var lastSend time.Time
	mux.HandleFunc("/qr", func(w http.ResponseWriter, r *http.Request) {
		qrMu.Lock()
		code := latestQR
		qrMu.Unlock()
		if code == "" {
			http.Error(w, "sin QR", http.StatusNoContent)
			return
		}
		png, err := qrcode.Encode(code, qrcode.Medium, 340)
		if err != nil {
			http.Error(w, "error generando QR", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(png)
	})
	mux.HandleFunc("/send", func(w http.ResponseWriter, r *http.Request) {
		sendMu.Lock()
		defer sendMu.Unlock()
		if wait := 3*time.Second - time.Since(lastSend); wait > 0 {
			time.Sleep(wait)
		}
		msg := r.URL.Query().Get("msg")
		to := r.URL.Query().Get("to")
		if to == "" {
			to = cfg.ProbeTarget
		}
		if msg == "" || to == "" {
			http.Error(w, "faltan msg y/o to", http.StatusBadRequest)
			return
		}
		jid, err := types.ParseJID(to)
		if err != nil {
			http.Error(w, "JID inválido: "+err.Error(), http.StatusBadRequest)
			return
		}
		sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err = cl.SendMessage(sctx, jid, &waE2E.Message{Conversation: proto.String(msg)}); err != nil {
			http.Error(w, "error enviando: "+err.Error(), http.StatusInternalServerError)
			return
		}
		plog.Printf("ENVIADO a %s: %s", jid.String(), msg)
		lastSend = time.Now()
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "probe whatsmeow — /qr (PNG) · /send?msg=...&to=JID")
	})
	plog.Printf("Probe HTTP en :%s", cfg.QRPort)
	return http.ListenAndServe(":"+cfg.QRPort, mux)
}

func probeText(m *waE2E.Message) string {
	if m == nil {
		return ""
	}
	if t := m.GetConversation(); t != "" {
		return t
	}
	if e := m.GetExtendedTextMessage(); e != nil {
		return e.GetText()
	}
	return "(sin texto)"
}

// waLogAdapter adapta un *log.Logger de Go a la interfaz waLog.Logger de
// whatsmeow para que todos los logs internos queden en probe.log.
type waLogAdapter struct {
	l *log.Logger
}

func (a waLogAdapter) Debugf(msg string, args ...interface{}) { a.l.Printf("DBG "+msg, args...) }
func (a waLogAdapter) Infof(msg string, args ...interface{})  { a.l.Printf(msg, args...) }
func (a waLogAdapter) Warnf(msg string, args ...interface{})  { a.l.Printf("WARN "+msg, args...) }
func (a waLogAdapter) Errorf(msg string, args ...interface{}) { a.l.Printf("ERR "+msg, args...) }
func (a waLogAdapter) Sub(module string) waLog.Logger         { return a }

// probeLogger registra en stderr y en probe.log (para verlo aunque corra
// desacoplado de la terminal).
func probeLogger(cfg *config.Config) (*log.Logger, func(), error) {
	var sinks []io.Writer
	sinks = append(sinks, os.Stderr)
	path := filepath.Join(filepath.Dir(cfg.WhatsmeowDB), "probe.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, nil, fmt.Errorf("abriendo probe.log: %w", err)
	}
	sinks = append(sinks, f)
	return log.New(io.MultiWriter(sinks...), "[probe] ", log.LstdFlags),
		func() { _ = f.Close() }, nil
}
