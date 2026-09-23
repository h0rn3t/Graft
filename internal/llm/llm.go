// Package llm is a minimal provider-neutral chat transport for forced tool
// calls, speaking the OpenAI-compatible Chat Completions API or the native
// Anthropic Messages API.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"math/rand/v2"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Provider names a wire format, not a vendor.
type Provider string

// Supported wire formats.
const (
	ProviderOpenAI     Provider = "openai"
	ProviderAnthropic  Provider = "anthropic"
	ProviderLiteLLM    Provider = "litellm"
	ProviderOrcaRouter Provider = "orcarouter"
)

const (
	openRouterBaseURL = "https://openrouter.ai/api/v1"
	orcaRouterBaseURL = "https://api.orcarouter.ai/v1"
	liteLLMBaseURL    = "http://localhost:4000"
	openAIBaseURL     = "https://api.openai.com/v1"
	anthropicBaseURL  = "https://api.anthropic.com"
	anthropicVersion  = "2023-06-01"
	defaultMaxTokens  = 4096
	requestTimeout    = 10 * time.Minute
)

var defaultModels = map[Provider]string{
	ProviderOpenAI:     "openai/gpt-4o-mini",
	ProviderAnthropic:  "claude-sonnet-5",
	ProviderLiteLLM:    "openai/gpt-4o-mini",
	ProviderOrcaRouter: "openai/gpt-4o-mini",
}

// Config is the resolved provider configuration.
type Config struct {
	Provider Provider
	APIKey   string
	Model    string
	BaseURL  string
	Headers  map[string]string
}

// ResolveConfig reads the provider configuration from the environment with the
// same precedence and defaults as the TypeScript resolveConfig.
func ResolveConfig() Config {
	provider := ProviderOpenAI
	if value, ok := os.LookupEnv("GRAFT_PROVIDER"); ok {
		provider = Provider(value)
	}
	explicitKey, hasExplicit := os.LookupEnv("GRAFT_API_KEY")
	legacyKey, hasLegacy := os.LookupEnv("OPENROUTER_API_KEY")
	apiKey := explicitKey
	if !hasExplicit {
		apiKey = legacyKey
		if !hasLegacy {
			apiKey = os.Getenv("ORCAROUTER_API_KEY")
		}
	}
	usedLegacy := explicitKey == "" && legacyKey != ""
	model := firstEnv("GRAFT_MODEL", "GRAFT_OPENROUTER_MODEL", "ORCAROUTER_MODEL")
	if model == "" {
		model = defaultModels[provider]
	}
	baseURL := firstEnv("GRAFT_BASE_URL", "OPENROUTER_BASE_URL", "ORCAROUTER_BASE_URL")
	if baseURL == "" && provider == ProviderOpenAI && usedLegacy {
		baseURL = openRouterBaseURL
	}
	if baseURL == "" && provider == ProviderOrcaRouter {
		baseURL = orcaRouterBaseURL
	}
	var headers map[string]string
	if provider == ProviderOpenAI && strings.Contains(baseURL, "openrouter.ai") {
		headers = map[string]string{"X-Title": "graft"}
	}
	return Config{Provider: provider, APIKey: apiKey, Model: model, BaseURL: baseURL, Headers: headers}
}

// firstEnv returns the first set variable, treating a set empty value as set,
// like JavaScript's ?? operator over process.env.
func firstEnv(names ...string) string {
	for _, name := range names {
		if value, ok := os.LookupEnv(name); ok {
			return value
		}
	}
	return ""
}

// Tool is a tool the model is forced to call; Parameters is a JSON Schema.
type Tool struct {
	Name        string
	Description string
	Parameters  any
}

// Request is one forced-tool call.
type Request struct {
	System      string
	User        string
	Tool        Tool
	Temperature *float64
	MaxTokens   int
}

// Client sends forced-tool requests to one configured model.
type Client struct {
	cfg     Config
	http    *http.Client
	retries int
	sleep   func(context.Context, time.Duration) error
}

// New returns a client for cfg, or an error for an unknown provider.
func New(cfg Config) (*Client, error) {
	switch cfg.Provider {
	case ProviderOpenAI, ProviderAnthropic, ProviderOrcaRouter:
	case ProviderLiteLLM:
		if cfg.BaseURL == "" {
			cfg.BaseURL = liteLLMBaseURL
		}
	default:
		return nil, fmt.Errorf("unknown provider: %s", cfg.Provider)
	}
	return &Client{cfg: cfg, http: &http.Client{Timeout: requestTimeout}, retries: transportRetries(), sleep: sleepContext}, nil
}

// transportRetries mirrors GRAFT_LLM_RETRIES with a default of four.
func transportRetries() int {
	raw, ok := os.LookupEnv("GRAFT_LLM_RETRIES")
	if !ok {
		return 4
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if strings.TrimSpace(raw) == "" {
		value, err = 0, nil
	}
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 4
	}
	return int(math.Floor(value))
}

// CallTool sends req and returns the arguments of the first tool call, or nil
// when the model made none.
func (c *Client) CallTool(ctx context.Context, req Request) (json.RawMessage, error) {
	if c.cfg.Provider == ProviderAnthropic {
		return c.callAnthropic(ctx, req)
	}
	return c.callOpenAI(ctx, req)
}

// connectionError is a transport failure, reported with the SDKs' wording.
type connectionError struct {
	cause error
}

func (err connectionError) Error() string {
	return "Connection error."
}

func (err connectionError) Unwrap() error {
	return err.cause
}

// APIError is a non-success HTTP response, formatted like the provider SDKs.
type APIError struct {
	Status  int
	Message string
}

func (err *APIError) Error() string {
	return err.Message
}

func (c *Client) callOpenAI(ctx context.Context, req Request) (json.RawMessage, error) {
	params := map[string]any{
		"model": c.cfg.Model,
		"messages": []map[string]any{
			{"role": "system", "content": req.System},
			{"role": "user", "content": req.User},
		},
		"tools": []map[string]any{{"type": "function", "function": map[string]any{
			"name": req.Tool.Name, "description": req.Tool.Description, "parameters": req.Tool.Parameters,
		}}},
		"tool_choice": map[string]any{"type": "function", "function": map[string]any{"name": req.Tool.Name}},
	}
	if req.Temperature != nil {
		params["temperature"] = *req.Temperature
	}
	if req.MaxTokens > 0 {
		params["max_tokens"] = req.MaxTokens
	}
	baseURL := c.cfg.BaseURL
	if baseURL == "" {
		baseURL = openAIBaseURL
	}
	headers := map[string]string{"Authorization": "Bearer " + c.cfg.APIKey}
	maps.Copy(headers, c.cfg.Headers)
	var body []byte
	var err error
	// Bounded like the TypeScript adapter: one retry per known 400 fallback.
	for fallback := 0; ; fallback++ {
		body, err = c.post(ctx, strings.TrimRight(baseURL, "/")+"/chat/completions", headers, params, openAIMessage)
		if fallback == 4 || !adjustOpenAIParams(params, err) {
			break
		}
	}
	if err != nil {
		return nil, err
	}
	var resp struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					Type     string `json:"type"`
					Function struct {
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode chat completion: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, nil
	}
	for _, call := range resp.Choices[0].Message.ToolCalls {
		if call.Type != "function" {
			continue
		}
		args := call.Function.Arguments
		if args == "" {
			args = "{}"
		}
		if !json.Valid([]byte(args)) {
			return json.RawMessage("{}"), nil
		}
		return json.RawMessage(args), nil
	}
	return nil, nil
}

var (
	rejectedReasoning  = regexp.MustCompile(`(?i)function tools with reasoning_effort|thinking mode does not support this tool_choice`)
	rejectedToolChoice = regexp.MustCompile(`(?i)tool_choice`)
	rejectedMaxTokens  = regexp.MustCompile(`(?i)max_tokens.*not supported.*max_completion_tokens`)
	rejectedTemp       = regexp.MustCompile(`(?i)temperature.*does not support`)
)

// adjustOpenAIParams applies one known 400 fallback to params and reports
// whether the request should be retried.
func adjustOpenAIParams(params map[string]any, err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		return false
	}
	message := apiErr.Message
	if _, set := params["reasoning_effort"]; rejectedReasoning.MatchString(message) && !set {
		params["reasoning_effort"] = "none"
		return true
	}
	if _, object := params["tool_choice"].(map[string]any); rejectedToolChoice.MatchString(message) && object {
		params["tool_choice"] = "required"
		return true
	}
	if value, set := params["max_tokens"]; rejectedMaxTokens.MatchString(message) && set {
		delete(params, "max_tokens")
		params["max_completion_tokens"] = value
		return true
	}
	if _, set := params["temperature"]; rejectedTemp.MatchString(message) && set {
		delete(params, "temperature")
		return true
	}
	return false
}

func (c *Client) callAnthropic(ctx context.Context, req Request) (json.RawMessage, error) {
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	params := map[string]any{
		"model":       c.cfg.Model,
		"max_tokens":  maxTokens,
		"messages":    []map[string]any{{"role": "user", "content": []map[string]any{{"type": "text", "text": req.User}}}},
		"system":      []map[string]any{{"type": "text", "text": req.System}},
		"tools":       []map[string]any{{"name": req.Tool.Name, "description": req.Tool.Description, "input_schema": req.Tool.Parameters}},
		"tool_choice": map[string]any{"type": "tool", "name": req.Tool.Name},
	}
	baseURL := c.cfg.BaseURL
	if baseURL == "" {
		baseURL = anthropicBaseURL
	}
	headers := map[string]string{"x-api-key": c.cfg.APIKey, "anthropic-version": anthropicVersion}
	body, err := c.post(ctx, strings.TrimRight(baseURL, "/")+"/v1/messages", headers, params, anthropicMessage)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Content []struct {
			Type  string          `json:"type"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode message: %w", err)
	}
	for _, block := range resp.Content {
		if block.Type == "tool_use" {
			return block.Input, nil
		}
	}
	return nil, nil
}

// post sends one JSON request with the SDKs' retry policy: connection errors,
// 408, 409, 429, and 5xx retry with capped exponential backoff and Retry-After.
func (c *Client) post(ctx context.Context, url string, headers map[string]string, params any, format func(int, []byte) string) ([]byte, error) {
	payload, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	for attempt := 0; ; attempt++ {
		body, status, retryAfter, err := c.send(ctx, url, headers, payload)
		if err == nil && status >= 200 && status < 300 {
			return body, nil
		}
		retryable := err != nil || status == http.StatusRequestTimeout || status == http.StatusConflict ||
			status == http.StatusTooManyRequests || status >= 500
		if !retryable || attempt >= c.retries || ctx.Err() != nil {
			if err != nil {
				return nil, connectionError{cause: err}
			}
			return nil, &APIError{Status: status, Message: format(status, body)}
		}
		if sleepErr := c.sleep(ctx, backoff(attempt, retryAfter)); sleepErr != nil {
			return nil, sleepErr
		}
	}
}

func (c *Client) send(ctx context.Context, url string, headers map[string]string, payload []byte) ([]byte, int, time.Duration, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, 0, 0, err
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, 0, 0, err
	}
	return body, response.StatusCode, retryAfter(response.Header), nil
}

func retryAfter(header http.Header) time.Duration {
	if ms, err := strconv.ParseFloat(header.Get("retry-after-ms"), 64); err == nil && ms >= 0 {
		return time.Duration(ms * float64(time.Millisecond))
	}
	raw := header.Get("retry-after")
	if seconds, err := strconv.ParseFloat(raw, 64); err == nil && seconds >= 0 {
		return time.Duration(seconds * float64(time.Second))
	}
	if at, err := http.ParseTime(raw); err == nil {
		return time.Until(at)
	}
	return -1
}

func backoff(attempt int, hinted time.Duration) time.Duration {
	if hinted >= 0 && hinted < time.Minute {
		return hinted
	}
	seconds := math.Min(0.5*math.Pow(2, float64(attempt)), 8)
	jitter := 1 - rand.Float64()*0.25
	return time.Duration(seconds * jitter * float64(time.Second))
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// openAIMessage formats an error like the openai SDK: "<status> <error.message>".
func openAIMessage(status int, body []byte) string {
	var payload struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &payload) != nil || len(payload.Error) == 0 || string(payload.Error) == "null" {
		if len(bytes.TrimSpace(body)) == 0 {
			return fmt.Sprintf("%d status code (no body)", status)
		}
		return fmt.Sprintf("%d %s", status, compactJSON(body))
	}
	return fmt.Sprintf("%d %s", status, sdkMessage(payload.Error))
}

// anthropicMessage formats an error like the Anthropic SDK, which reports the
// whole error body.
func anthropicMessage(status int, body []byte) string {
	if len(bytes.TrimSpace(body)) == 0 {
		return fmt.Sprintf("%d status code (no body)", status)
	}
	return fmt.Sprintf("%d %s", status, sdkMessage(body))
}

func sdkMessage(raw json.RawMessage) string {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) == nil {
		if message, ok := object["message"]; ok {
			var text string
			if json.Unmarshal(message, &text) == nil {
				return text
			}
			return compactJSON(message)
		}
	}
	return compactJSON(raw)
}

func compactJSON(raw []byte) string {
	var out bytes.Buffer
	if json.Compact(&out, raw) != nil {
		return string(raw)
	}
	return out.String()
}
