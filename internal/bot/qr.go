package bot

import (
	"net/http"

	"github.com/skip2/go-qrcode"
)

// startQRServer expone el QR actual como imagen PNG en /qr.
func (b *Bot) startQRServer() {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/qr" {
			http.NotFound(w, r)
			return
		}
		b.qrMu.Lock()
		code := b.latestQR
		b.qrMu.Unlock()
		if code == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		png, err := qrcode.Encode(code, qrcode.Medium, 340)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(png)
	})
	_ = http.ListenAndServe(":"+b.qrPort, mux)
}