package bot

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"go.mau.fi/whatsmeow"
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"

	"google.golang.org/protobuf/proto"
)

// outWorker consume la cola de salida respetando el ritmo anti-baneo.
func (b *Bot) outWorker() {
	for {
		select {
		case <-b.stop:
			return
		case m := <-b.out:
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)

			var sendErr error
			if quotaErr := b.guard.checkQuota(m.jid.User); quotaErr != nil {
				sendErr = quotaErr
			} else if m.media != nil {
				sendErr = b.sendMedia(ctx, m.jid, m.media, m.mime, m.filename)
			} else if m.text != "" {
				sendErr = b.guard.waitGap(ctx)
				if sendErr == nil {
					sendErr = b.sendTextDirect(ctx, m.jid, m.text)
				}
			}
			if sendErr != nil {
				b.log.Printf("Enviando a %s: %v", m.jid, sendErr)
			} else {
				b.guard.markSent(m.jid.User)
				// Persistir todo lo que el bot envía para que el asistente tenga
				// el contexto completo de la conversación.
				if m.text != "" {
					_ = b.history.Append(ctx, m.jid.User, "assistant", m.text)
				} else if m.media != nil && m.filename != "" {
					_ = b.history.Append(ctx, m.jid.User, "assistant", "[Adjunto enviado: "+m.filename+"]")
				}
			}
			cancel()
		}
	}
}

// sendMediaQueued encola un adjunto (imagen o documento).
func (b *Bot) sendMediaQueued(jid types.JID, data []byte, mime, filename string, isImage bool) {
	if len(data) == 0 {
		return
	}
	kind := "document"
	if isImage {
		kind = "image"
	}
	b.out <- outMsg{jid: jid, media: &mediaPayload{data: data, kind: kind}, mime: mime, filename: filename}
}

// sendTextDirect envía un mensaje de texto inmediatamente.
func (b *Bot) sendTextDirect(ctx context.Context, to types.JID, text string) error {
	msg := &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String(text)}}
	_, err := b.client.SendMessage(ctx, to, msg)
	return err
}

// sendMedia sube un adjunto y lo envía.
func (b *Bot) sendMedia(ctx context.Context, to types.JID, med *mediaPayload, mime, filename string) error {
	if med == nil {
		return fmt.Errorf("adjunto vacío")
	}
	if err := b.guard.waitGap(ctx); err != nil {
		return err
	}

	switch med.kind {
	case "image":
		resp, err := b.client.Upload(ctx, med.data, whatsmeow.MediaImage)
		if err != nil {
			return err
		}
		img := &waE2E.ImageMessage{
			Caption:       proto.String(""),
			Mimetype:      proto.String(mime),
			URL:           &resp.URL,
			DirectPath:    &resp.DirectPath,
			MediaKey:      resp.MediaKey,
			FileEncSHA256: resp.FileEncSHA256,
			FileSHA256:    resp.FileSHA256,
			FileLength:    &resp.FileLength,
		}
		_, err = b.client.SendMessage(ctx, to, &waE2E.Message{ImageMessage: img})
		return err

	default: // document
		resp, err := b.client.Upload(ctx, med.data, whatsmeow.MediaDocument)
		if err != nil {
			return err
		}
		doc := &waE2E.DocumentMessage{
			Mimetype:      proto.String(mime),
			FileName:      proto.String(filename),
			Title:         proto.String(filename),
			URL:           &resp.URL,
			DirectPath:    &resp.DirectPath,
			MediaKey:      resp.MediaKey,
			FileEncSHA256: resp.FileEncSHA256,
			FileSHA256:    resp.FileSHA256,
			FileLength:    &resp.FileLength,
		}
		_, err = b.client.SendMessage(ctx, to, &waE2E.Message{DocumentMessage: doc})
		return err
	}
}

// dailyCleanup borra el historial viejo una vez al día (3:00 AM hora Venezuela).
func (b *Bot) dailyCleanup() {
	retainH := 24 * 30 // 30 días por defecto
	cleanHour := 3
	if v, err := strconv.Atoi(os.Getenv("HISTORY_RETAIN_HOURS")); err == nil && v > 0 {
		retainH = v
	}
	if v, err := strconv.Atoi(os.Getenv("HISTORY_CLEAN_HOUR")); err == nil && v >= 0 && v < 24 {
		cleanHour = v
	}

	loc, err := time.LoadLocation("America/Caracas")
	if err != nil {
		loc = time.Local
	}
	cleaned := ""
	for {
		now := time.Now().In(loc)
		if now.Format("2006-01-02") != cleaned && now.Hour() == cleanHour {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			if cerr := b.history.Clean(ctx, retainH); cerr != nil {
				b.log.Printf("Limpiando historial: %v", cerr)
			}
			cancel()
			cleaned = now.Format("2006-01-02")
		}
		// Despertar cada 30 min.
		time.Sleep(30 * time.Minute)
	}
}