package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	apiKey  string
	model   any // string o []string (router/lista)
	http    *http.Client
	baseURL string
}

// NewClient recibe el model: una string (p.ej. "openrouter/auto") o un JSON
// array de modelos.
func NewClient(apiKey, model string) *Client {
	c := &Client{
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 120 * time.Second},
		baseURL: "https://openrouter.ai/api/v1/chat/completions",
	}
	if len(model) > 0 && model[0] == '[' {
		var list []string
		if err := json.Unmarshal([]byte(model), &list); err == nil && len(list) > 0 {
			c.model = list
		}
	}
	if c.model == nil {
		c.model = model
	}
	return c
}

// Chat envía mensajes (con tools opcionales) y devuelve la elección del modelo.
func (c *Client) Chat(ctx context.Context, messages []Message, tools []Tool) (Message, error) {
	body := map[string]any{
		"model":    c.model,
		"messages": messages,
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	b, err := json.Marshal(body)
	if err != nil {
		return Message{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(b))
	if err != nil {
		return Message{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return Message{}, err
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
