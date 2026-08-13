package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Message es un mensaje del historial de chat (estilo OpenAI).
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Tool es la definición de una herramienta que el modelo puede invocar.
type Tool struct {
	Type     string   `json:"type"`
	Function Function `json:"function"`
}

type Function struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type choice struct {
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

type respType struct {
	Choices []choice `json:"choices"`
	Error   *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Client habla con la API de chat de OpenRouter.
type Client struct {
	apiKey   string
	models   []string // lista ordenada de modelos con failover
	http     *http.Client
	chatHTTP *http.Client // timeout corto para detectar modelos colgados
	baseURL  string

	mu       sync.Mutex
	cooldown map[string]time.Time // modelo -> hasta cuándo se salta por fallar

	logger *log.Logger
}

// NewClient recibe el model: una string (p.ej. "openrouter/auto"), una lista
// separada por comas o un JSON array de modelos. La lista se prueba en orden:
// si uno falla (timeout, error HTTP, respuesta vacía), se pasa al siguiente.
func NewClient(apiKey, model string) *Client {
	c := &Client{
		apiKey:   apiKey,
		http:     &http.Client{Timeout: 120 * time.Second},
		chatHTTP: &http.Client{Timeout: 20 * time.Second},
		baseURL:  "https://openrouter.ai/api/v1/chat/completions",
		cooldown: map[string]time.Time{},
		logger:   log.New(io.Discard, "", 0),
	}
	c.models = parseModelList(model)
	return c
}

// SetLogger activa el registro de fallos/failover del cliente.
func (c *Client) SetLogger(l *log.Logger) {
	if l != nil {
		c.logger = l
	}
}

// parseModelList interpreta el valor de OPENROUTER_MODEL: puede ser un JSON
// array, una lista separada por comas o un solo modelo.
func parseModelList(model string) []string {
	model = strings.TrimSpace(model)
	if model == "" {
		return []string{"openrouter/auto"}
	}
	var out []string
	if strings.HasPrefix(model, "[") {
		var list []string
		if err := json.Unmarshal([]byte(model), &list); err == nil && len(list) > 0 {
			return appendAuto(list)
		}
	}
	for _, m := range strings.Split(model, ",") {
		if m = strings.TrimSpace(m); m != "" {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		out = []string{"openrouter/auto"}
	}
	return appendAuto(out)
}

// appendAuto agrega openrouter/auto al final como red de seguridad si no está
// explícito en la lista.
func appendAuto(list []string) []string {
	found := false
	for _, m := range list {
		if m == "openrouter/auto" {
			found = true
		}
	}
	if !found {
		list = append(list, "openrouter/auto")
	}
	return list
}

// Models devuelve la lista de modelos configurada (para logs/diagnóstico).
func (c *Client) Models() []string {
	return c.models
}

// Chat envía mensajes (con tools opcionales) probando los modelos en orden
// hasta obtener una respuesta no vacía. Si un modelo falla entra en "enfriamiento"
// y se salta durante un rato para no re-intentarlo en cada mensaje.
func (c *Client) Chat(ctx context.Context, messages []Message, tools []Tool) (Message, error) {
	body := func(model any) ([]byte, error) {
		b := map[string]any{"model": model, "messages": messages}
		if len(tools) > 0 {
			b["tools"] = tools
		}
		return json.Marshal(b)
	}

	for _, m := range c.nextCandidates() {
		// Si el contexto global ya expiró, no tiene sentido seguir.
		if dl, ok := ctx.Deadline(); ok && time.Until(dl) < 2*time.Second {
			break
		}
		b, err := body(m)
		if err != nil {
			return Message{}, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(b))
		if err != nil {
			return Message{}, err
		}
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		req.Header.Set("Content-Type", "application/json")

		msg, err := c.doOnce(req, m)
		if err != nil {
			c.markFailed(m)
			c.logger.Printf("LLM modelo %s falló: %v", m, err)
			continue
		}
		// Respuesta vacía sin tool calls = fallo del modelo, probar el siguiente.
		if msg.Content == "" && len(msg.ToolCalls) == 0 {
			c.markFailed(m)
			c.logger.Printf("LLM modelo %s devolvió respuesta vacía", m)
			continue
		}
		return msg, nil
	}
	return Message{}, fmt.Errorf("todos los modelos fallaron")
}

// nextCandidates devuelve los modelos a probar, saltando los que están en
// enfriamiento. Si todos están en enfriamiento, los reactiva todos.
func (c *Client) nextCandidates() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	var out []string
	for _, m := range c.models {
		if until, ok := c.cooldown[m]; ok && now.Before(until) {
			continue
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		c.cooldown = map[string]time.Time{}
		return append([]string(nil), c.models...)
	}
	return out
}

// markFailed pone un modelo en enfriamiento tras un fallo.
func (c *Client) markFailed(model string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cooldown[model] = time.Now().Add(60 * time.Second)
}

// doOnce ejecuta una petición de chat contra un modelo concreto.
func (c *Client) doOnce(req *http.Request, model string) (Message, error) {
	resp, err := c.chatHTTP.Do(req)
	if err != nil {
		return Message{}, fmt.Errorf("openrouter %s: %w", model, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return Message{}, err
	}
	if resp.StatusCode >= 300 {
		return Message{}, fmt.Errorf("openrouter %d: %s", resp.StatusCode, truncate(string(data)))
	}
	var r respType
	if err := json.Unmarshal(data, &r); err != nil {
		return Message{}, fmt.Errorf("openrouter: %w: %s", err, truncate(string(data)))
	}
	if r.Error != nil {
		return Message{}, fmt.Errorf("openrouter error: %s", r.Error.Message)
	}
	if len(r.Choices) == 0 {
		return Message{}, fmt.Errorf("openrouter: sin choices")
	}
	return r.Choices[0].Message, nil
}

// Transcribe envía audio a un modelo (p. ej. Gemini vía OpenRouter) y devuelve
// el texto transcrito. `format` puede ser "ogg", "wav", "mp3", etc.
func (c *Client) Transcribe(ctx context.Context, model string, audio []byte, format string) (string, error) {
	audioContent := []map[string]any{
		{"type": "input_audio", "input_audio": map[string]any{
			"data":   base64.StdEncoding.EncodeToString(audio),
			"format": format,
		}},
	}
	body := map[string]any{
		"model": model,
		"messages": []map[string]any{
			{"role": "system", "content": "Transcribe el audio a texto en español. Devuelve únicamente el texto transcrito, sin comentarios ni aclaraciones."},
			{"role": "user", "content": audioContent},
		},
	}
	b, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("openrouter transcribe %d: %s", resp.StatusCode, truncate(string(data)))
	}
	var r respType
	if err := json.Unmarshal(data, &r); err != nil {
		return "", fmt.Errorf("openrouter transcribe: %w", err)
	}
	if len(r.Choices) == 0 {
		return "", fmt.Errorf("openrouter transcribe: sin choices")
	}
	return r.Choices[0].Message.Content, nil
}

func truncate(s string) string {
	if len(s) > 400 {
		return s[:400]
	}
	return s
}
