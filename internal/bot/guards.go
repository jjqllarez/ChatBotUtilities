package bot

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"
)

// Guard coordina el anti-baneo: separación mínima entre envíos y cuota diaria
// por número de remitente/chat.
type Guard struct {
	mu        sync.Mutex
	lastSent  time.Time
	day       string
	perPhone  map[string]int
	minGap    time.Duration
	maxDaily  int
}

func newGuard() *Guard {
	g := &Guard{perPhone: make(map[string]int), maxDaily: 1500}
	if v, err := strconv.Atoi(os.Getenv("WA_MIN_GAP_MS")); err == nil && v > 0 {
		g.minGap = time.Duration(v) * time.Millisecond
	} else {
		g.minGap = 1200 * time.Millisecond
	}
	if v, err := strconv.Atoi(os.Getenv("WA_MAX_DAILY_MSGS")); err == nil && v > 0 {
		g.maxDaily = v
	}
	return g
}

// waitGap duerme lo necesario para respetar la separación mínima entre mensajes.
func (g *Guard) waitGap(ctx context.Context) error {
	for {
		g.mu.Lock()
		wait := time.Until(g.lastSent.Add(g.minGap))
		g.mu.Unlock()
		if wait <= 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// markSent registra un envío para el número dado.
func (g *Guard) markSent(phone string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.rollDayLocked()
	g.lastSent = time.Now()
	g.perPhone[phone]++
}

// checkQuota devuelve error si se superó la cuota diaria para un número.
func (g *Guard) checkQuota(phone string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.rollDayLocked()
	if g.perPhone[phone] >= g.maxDaily {
		return fmt.Errorf("cuota diaria superada para %s", phone)
	}
	return nil
}

func (g *Guard) rollDayLocked() {
	today := time.Now().Format("2006-01-02")
	if today != g.day {
		g.day = today
		g.perPhone = make(map[string]int)
	}
}