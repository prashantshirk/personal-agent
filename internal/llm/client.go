package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type Message struct {
	Role         string          `json:"role"`
	Content      string          `json:"content,omitempty"`
	ToolCallID   string          `json:"tool_call_id,omitempty"`
	ToolCalls    []ToolCall      `json:"tool_calls,omitempty"`
	ExtraContent json.RawMessage `json:"extra_content,omitempty"`
}

type ToolCall struct {
	ID           string          `json:"id"`
	Type         string          `json:"type"`
	Function     FunctionCall    `json:"function"`
	ExtraContent json.RawMessage `json:"extra_content,omitempty"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type Tool struct {
	Type     string   `json:"type"`
	Function Function `json:"function"`
}

type Function struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type Completion struct {
	Message                        Message
	PromptTokens, CompletionTokens int
}

type Provider struct {
	Name, BaseURL, APIKey, Model string
}

type Client struct {
	Providers []Provider
	HTTP      *http.Client
}

func (c *Client) Complete(ctx context.Context, messages []Message, tools []Tool) (Completion, error) {
	completion, _, err := c.CompleteFrom(ctx, messages, tools, 0)
	return completion, err
}

func (c *Client) CompleteFrom(ctx context.Context, messages []Message, tools []Tool, start int) (Completion, int, error) {
	var failures []error
	if start < 0 {
		start = 0
	}
	for index := start; index < len(c.Providers); index++ {
		provider := c.Providers[index]
		if provider.APIKey == "" {
			failures = append(failures, fmt.Errorf("%s (%s): API key is not configured", provider.Name, provider.Model))
			continue
		}

		completion, err := c.completeWith(ctx, provider, messages, tools)
		if err == nil {
			if index > 0 {
				slog.Info("LLM fallback provider succeeded", "provider", provider.Name, "model", provider.Model)
			}
			return completion, index, nil
		}
		if ctx.Err() != nil {
			return Completion{}, index, ctx.Err()
		}
		slog.Warn("LLM provider failed; trying next provider", "provider", provider.Name, "model", provider.Model, "error", err)
		failures = append(failures, fmt.Errorf("%s (%s): %w", provider.Name, provider.Model, err))
	}
	if len(failures) == 0 {
		return Completion{}, start, errors.New("no LLM providers are configured")
	}
	return Completion{}, start, fmt.Errorf("all configured LLM providers failed: %w", errors.Join(failures...))
}

func (c *Client) completeWith(ctx context.Context, provider Provider, messages []Message, tools []Tool) (Completion, error) {
	endpoint := strings.TrimRight(provider.BaseURL, "/")
	if !strings.HasSuffix(endpoint, "/chat/completions") {
		endpoint += "/chat/completions"
	}
	body, err := json.Marshal(map[string]any{
		"model": provider.Model, "messages": messages, "tools": tools,
		"tool_choice": "auto", "max_tokens": 1200,
	})
	if err != nil {
		return Completion{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Completion{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+provider.APIKey)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Completion{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return Completion{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body := strings.TrimSpace(string(data))
		if len(body) > 1200 {
			body = body[:1200] + "…"
		}
		if body == "" {
			return Completion{}, fmt.Errorf("LLM returned HTTP %d", resp.StatusCode)
		}
		return Completion{}, fmt.Errorf("LLM returned HTTP %d: %s", resp.StatusCode, body)
	}
	var result struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
		Usage struct {
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err = json.Unmarshal(data, &result); err != nil {
		return Completion{}, fmt.Errorf("decode LLM response: %w", err)
	}
	if len(result.Choices) == 0 {
		return Completion{}, errors.New("LLM returned no choices")
	}
	return Completion{
		Message:          result.Choices[0].Message,
		PromptTokens:     result.Usage.Prompt,
		CompletionTokens: result.Usage.Completion,
	}, nil
}

func NewClient(base, key, model string, timeout time.Duration) *Client {
	return NewClientWithFallbacks(Provider{Name: "primary", BaseURL: base, APIKey: key, Model: model}, nil, timeout)
}

func NewClientWithFallbacks(primary Provider, fallbacks []Provider, timeout time.Duration) *Client {
	providers := make([]Provider, 0, len(fallbacks)+1)
	providers = append(providers, primary)
	providers = append(providers, fallbacks...)
	return &Client{Providers: providers, HTTP: &http.Client{Timeout: timeout}}
}
