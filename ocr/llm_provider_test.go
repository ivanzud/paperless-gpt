package ocr

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCreateOpenAIClientWithOpenAICompatible tests that OpenAI-compatible services work without API keys
func TestCreateOpenAIClientWithOpenAICompatible(t *testing.T) {
	tests := []struct {
		name        string
		apiKey      string
		baseURL     string
		model       string
		shouldError bool
	}{
		{
			name:        "OpenAI-compatible with base URL and no API key",
			apiKey:      "",
			baseURL:     "http://localhost:1234/v1",
			model:       "test-model",
			shouldError: false,
		},
		{
			name:        "OpenAI-compatible with base URL and API key",
			apiKey:      "test-key",
			baseURL:     "http://localhost:1234/v1",
			model:       "test-model",
			shouldError: false,
		},
		{
			name:        "Standard OpenAI with API key and no base URL",
			apiKey:      "sk-test-key",
			baseURL:     "",
			model:       "test-model",
			shouldError: false,
		},
		{
			name:        "No API key and no base URL",
			apiKey:      "",
			baseURL:     "",
			model:       "test-model",
			shouldError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set environment variables
			t.Setenv("OPENAI_API_KEY", tt.apiKey)
			t.Setenv("OPENAI_BASE_URL", tt.baseURL)

			// Create config
			config := Config{
				VisionLLMProvider: "openai",
				VisionLLMModel:    tt.model,
			}

			// Create OpenAI client
			client, err := createOpenAIClient(config)

			if tt.shouldError {
				assert.Error(t, err)
				assert.Nil(t, client)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, client)
			}
		})
	}
}

// TestCreateOpenAIClientWithAzure tests Azure OpenAI configuration
func TestCreateOpenAIClientWithAzure(t *testing.T) {
	// Set Azure environment variables
	t.Setenv("OPENAI_API_KEY", "azure-test-key")
	t.Setenv("OPENAI_BASE_URL", "https://test.openai.azure.com")
	t.Setenv("OPENAI_API_TYPE", "azure")

	config := Config{
		VisionLLMProvider: "openai",
		VisionLLMModel:    "test-deployment",
	}

	// Create OpenAI client
	client, err := createOpenAIClient(config)

	assert.NoError(t, err)
	assert.NotNil(t, client)
}

func TestResolveVisionOllamaHost(t *testing.T) {
	tests := []struct {
		name              string
		configHost        string
		visionOllamaHost  string
		visionLLMHost     string
		ollamaHost        string
		expectedHostValue string
	}{
		{
			name:              "prefers config host",
			configHost:        "http://config-host:11434",
			visionOllamaHost:  "http://vision-ollama:11434",
			visionLLMHost:     "http://vision-llm-host:11434",
			ollamaHost:        "http://default-ollama:11434",
			expectedHostValue: "http://config-host:11434",
		},
		{
			name:              "prefers VISION_OLLAMA_HOST",
			configHost:        "",
			visionOllamaHost:  "http://vision-ollama:11434",
			visionLLMHost:     "http://vision-llm-host:11434",
			ollamaHost:        "http://default-ollama:11434",
			expectedHostValue: "http://vision-ollama:11434",
		},
		{
			name:              "falls back to VISION_LLM_HOST",
			configHost:        "",
			visionOllamaHost:  "",
			visionLLMHost:     "http://vision-llm-host:11434",
			ollamaHost:        "http://default-ollama:11434",
			expectedHostValue: "http://vision-llm-host:11434",
		},
		{
			name:              "falls back to OLLAMA_HOST",
			configHost:        "",
			visionOllamaHost:  "",
			visionLLMHost:     "",
			ollamaHost:        "http://default-ollama:11434",
			expectedHostValue: "http://default-ollama:11434",
		},
		{
			name:              "uses built in default",
			configHost:        "",
			visionOllamaHost:  "",
			visionLLMHost:     "",
			ollamaHost:        "",
			expectedHostValue: "http://127.0.0.1:11434",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("VISION_OLLAMA_HOST", tt.visionOllamaHost)
			t.Setenv("VISION_LLM_HOST", tt.visionLLMHost)
			t.Setenv("OLLAMA_HOST", tt.ollamaHost)

			config := Config{VisionLLMHost: tt.configHost}
			assert.Equal(t, tt.expectedHostValue, resolveVisionOllamaHost(config))
		})
	}
}

func TestIsGLMOCRModel(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		expected bool
	}{
		{name: "exact glm model", model: "glm-ocr", expected: true},
		{name: "prefixed glm model", model: "THUDM/glm-ocr:latest", expected: true},
		{name: "non glm model", model: "qwen2.5-vl", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, isGLMOCRModel(tt.model))
		})
	}
}

func TestEffectiveOCRPrompt(t *testing.T) {
	assert.Equal(t, "Text Recognition:", effectiveOCRPrompt("glm-ocr", "custom prompt"))
	assert.Equal(t, "custom prompt", effectiveOCRPrompt("qwen2.5-vl", "custom prompt"))
}

func TestParseRetryAfterHTTPDateRoundsUp(t *testing.T) {
	now := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	delay, ok := parseRetryAfter("Sat, 10 Oct 2026 00:00:02 GMT", now.Add(500*time.Millisecond))
	require.True(t, ok)
	assert.Equal(t, 1500*time.Millisecond, delay)
}

func TestParseRetryAfterHTTPDateRoundingDoesNotOverflow(t *testing.T) {
	now := time.Unix(0, 0).UTC()
	when := now.Add(time.Duration(1<<63 - 1))
	delay, ok := parseRetryAfter(when.Format(http.TimeFormat), now)
	require.True(t, ok)
	assert.Greater(t, delay, time.Duration(0))
	assert.LessOrEqual(t, delay, time.Duration(1<<63-1))
}

func TestRetryAfterDelayRejectsOverflow(t *testing.T) {
	backoff := 5 * time.Millisecond
	assert.Equal(t, backoff, retryAfterDelay(errors.New("retry-after-ms:9223372036854775807"), backoff))
	assert.Equal(t, time.Second, retryAfterDelay(errors.New("retry-after-ms:1000"), backoff))
}

func TestParseHeaderList(t *testing.T) {
	got := ParseHeaderList(" A=1, B = two words ,C=x=y,broken,=nokey,D=")
	assert.Equal(t, map[string]string{"A": "1", "B": "two words", "C": "x=y", "D": ""}, got)
	assert.Empty(t, ParseHeaderList(""))
}

func TestParseHeaderListCanonicalizesNames(t *testing.T) {
	got := ParseHeaderList("x-title=a, X-TITLE=b")
	assert.Equal(t, map[string]string{"X-Title": "b"}, got, "names differing only in case are one header")
}

func TestHeaderClientDoesNotFollowRedirectsToOtherHosts(t *testing.T) {
	var leaked string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("Authorization")
	}))
	defer other.Close()
	var sameHostHits int
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/moved":
			http.Redirect(w, r, "/v1", http.StatusFound) // same host: fine
		case "/v1":
			sameHostHits++
			assert.Equal(t, "Bearer secret", r.Header.Get("Authorization"))
		default:
			http.Redirect(w, r, other.URL, http.StatusFound) // another host
		}
	}))
	defer gateway.Close()

	client := headerClient(map[string]string{"Authorization": "Bearer secret"})

	resp, err := client.Get(gateway.URL + "/moved")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, 1, sameHostHits, "redirects on the same host are followed")

	_, err = client.Get(gateway.URL + "/elsewhere")
	assert.ErrorContains(t, err, "refusing to follow a redirect")
	assert.Empty(t, leaked, "the credentials must not reach another host")
}

func TestHeaderClientPreservesRetryAfterOn429(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	resp, err := headerClient(nil).Get(server.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, readErr := io.ReadAll(resp.Body)
	require.NoError(t, readErr)
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	assert.Contains(t, string(body), "retry-after-ms:2000")
}

type recordingRoundTripper struct {
	body   []byte
	header http.Header
}

func (r *recordingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r.header = req.Header.Clone()
	if req.Body != nil {
		r.body, _ = io.ReadAll(req.Body)
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`)), Header: http.Header{}, Request: req}, nil
}

// The shared OpenAI-compatible client (text LLM, vision LLM, LLM OCR) adds
// both the configured headers and, with OPENROUTER_ENFORCE_ZDR, the
// zero-data-retention preference for requests to OpenRouter.
func TestOpenAIHTTPClientEnforcesOpenRouterZDR(t *testing.T) {
	t.Setenv("OPENROUTER_ENFORCE_ZDR", "true")
	t.Setenv("OPENAI_HEADERS", "X-Team=docs")
	rec := &recordingRoundTripper{}
	previous := http.DefaultTransport
	http.DefaultTransport = rec
	t.Cleanup(func() { http.DefaultTransport = previous })

	client := OpenAIHTTPClient()
	resp, err := client.Post("https://openrouter.ai/api/v1/chat/completions", "application/json", strings.NewReader(`{"model":"m"}`))
	require.NoError(t, err)
	resp.Body.Close()

	var sent map[string]any
	require.NoError(t, json.Unmarshal(rec.body, &sent))
	provider, ok := sent["provider"].(map[string]any)
	require.True(t, ok, "the provider preference is added")
	assert.Equal(t, true, provider["zdr"])
	assert.Equal(t, "deny", provider["data_collection"])
	assert.Equal(t, "docs", rec.header.Get("X-Team"))
	assert.Equal(t, "paperless-gpt", rec.header.Get("X-Title"))

	// Other hosts are left alone.
	resp, err = client.Post("https://api.openai.com/v1/chat/completions", "application/json", strings.NewReader(`{"model":"m"}`))
	require.NoError(t, err)
	resp.Body.Close()
	assert.JSONEq(t, `{"model":"m"}`, string(rec.body))
}
