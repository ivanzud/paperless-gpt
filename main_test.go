package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
)

type TestCase struct {
	name      string
	documents []TestDocument
}

func TestRunHTTPServerReturnsUnexpectedListenError(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()

	backgroundDone := make(chan struct{})
	close(backgroundDone)
	stopped := make(chan struct{})

	err = runHTTPServer(
		context.Background(),
		func() { close(stopped) },
		http.NotFoundHandler(),
		listener.Addr().String(),
		backgroundDone,
	)

	require.ErrorContains(t, err, "HTTP server failed")
	select {
	case <-stopped:
	default:
		t.Fatal("expected server failure to stop the application context")
	}
}

// TestDocument containing extra parameters for testing
type TestDocument struct {
	ID         int
	Title      string
	Tags       []string
	FailUpdate bool // simulate update failure
}

// Test our HTTP-Client
func TestCreateCustomHTTPClient(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify custom header
		assert.Equal(t, "paperless-gpt", r.Header.Get("X-Title"), "Expected X-Title header")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Get custom client
	client := createCustomHTTPClient()
	require.NotNil(t, client, "HTTP client should not be nil")

	// Make a request
	resp, err := client.Get(server.URL)
	require.NoError(t, err, "Request should not fail")
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode, "Expected 200 OK response")
}

// TestCreateLLMWithOpenAICompatible tests that OpenAI-compatible services work without API keys
func TestCreateLLMWithOpenAICompatible(t *testing.T) {
	// Save original env vars and restore after test
	origProvider := llmProvider
	origModel := llmModel
	origAPIKey := openaiAPIKey
	defer func() {
		llmProvider = origProvider
		llmModel = origModel
		openaiAPIKey = origAPIKey
		t.Setenv("OPENAI_API_KEY", "")
		t.Setenv("OPENAI_BASE_URL", "")
	}()

	tests := []struct {
		name        string
		apiKey      string
		baseURL     string
		shouldError bool
	}{
		{
			name:        "OpenAI-compatible with base URL and no API key",
			apiKey:      "",
			baseURL:     "http://localhost:1234/v1",
			shouldError: false,
		},
		{
			name:        "OpenAI-compatible with base URL and API key",
			apiKey:      "test-key",
			baseURL:     "http://localhost:1234/v1",
			shouldError: false,
		},
		{
			name:        "Standard OpenAI with API key and no base URL",
			apiKey:      "sk-test-key",
			baseURL:     "",
			shouldError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set environment variables
			t.Setenv("OPENAI_API_KEY", tt.apiKey)
			t.Setenv("OPENAI_BASE_URL", tt.baseURL)

			// Update global vars
			llmProvider = "openai"
			llmModel = "test-model"
			openaiAPIKey = tt.apiKey

			// Create LLM client
			llm, err := createLLM()

			if tt.shouldError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, llm)
			}
		})
	}
}

// TestCreateVisionLLMWithOpenAICompatible tests that OpenAI-compatible services work without API keys for vision models
func TestCreateVisionLLMWithOpenAICompatible(t *testing.T) {
	// Save original env vars and restore after test
	origProvider := visionLlmProvider
	origModel := visionLlmModel
	origAPIKey := openaiAPIKey
	defer func() {
		visionLlmProvider = origProvider
		visionLlmModel = origModel
		openaiAPIKey = origAPIKey
		t.Setenv("OPENAI_API_KEY", "")
		t.Setenv("OPENAI_BASE_URL", "")
	}()

	tests := []struct {
		name        string
		apiKey      string
		baseURL     string
		shouldError bool
	}{
		{
			name:        "OpenAI-compatible vision with base URL and no API key",
			apiKey:      "",
			baseURL:     "http://localhost:1234/v1",
			shouldError: false,
		},
		{
			name:        "OpenAI-compatible vision with base URL and API key",
			apiKey:      "test-key",
			baseURL:     "http://localhost:1234/v1",
			shouldError: false,
		},
		{
			name:        "Standard OpenAI vision with API key and no base URL",
			apiKey:      "sk-test-key",
			baseURL:     "",
			shouldError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set environment variables
			t.Setenv("OPENAI_API_KEY", tt.apiKey)
			t.Setenv("OPENAI_BASE_URL", tt.baseURL)

			// Update global vars
			visionLlmProvider = "openai"
			visionLlmModel = "test-vision-model"
			openaiAPIKey = tt.apiKey

			// Create Vision LLM client
			llm, err := createVisionLLM()

			if tt.shouldError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, llm)
			}
		})
	}
}

func TestResolveVisionOllamaHost(t *testing.T) {
	tests := []struct {
		name              string
		visionOllamaHost  string
		visionLLMHost     string
		ollamaHost        string
		expectedHostValue string
	}{
		{
			name:              "prefers VISION_OLLAMA_HOST",
			visionOllamaHost:  "http://vision-ollama:11434",
			visionLLMHost:     "http://vision-llm-host:11434",
			ollamaHost:        "http://default-ollama:11434",
			expectedHostValue: "http://vision-ollama:11434",
		},
		{
			name:              "falls back to VISION_LLM_HOST",
			visionOllamaHost:  "",
			visionLLMHost:     "http://vision-llm-host:11434",
			ollamaHost:        "http://default-ollama:11434",
			expectedHostValue: "http://vision-llm-host:11434",
		},
		{
			name:              "falls back to OLLAMA_HOST",
			visionOllamaHost:  "",
			visionLLMHost:     "",
			ollamaHost:        "http://default-ollama:11434",
			expectedHostValue: "http://default-ollama:11434",
		},
		{
			name:              "uses built in default",
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

			assert.Equal(t, tt.expectedHostValue, resolveVisionOllamaHost())
		})
	}
}

// OPENAI_HEADERS can carry gateway credentials. They must reach the
// OpenAI-compatible endpoint and nothing else: before, the custom client was
// http.DefaultClient itself, so the headers leaked into every other request
// made through it (Anthropic, tiktoken downloads, Ollama metadata).
func TestOpenAIHeadersStayOnTheOpenAIClient(t *testing.T) {
	t.Setenv("OPENAI_HEADERS", "Authorization=Bearer gateway-secret, X-Team = docs,broken")

	var got http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := createCustomHTTPClient()
	require.NotSame(t, http.DefaultClient, client)
	resp, err := client.Get(server.URL)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, "Bearer gateway-secret", got.Get("Authorization"))
	assert.Equal(t, "docs", got.Get("X-Team"), "spaces around key and value are dropped")
	assert.Equal(t, "paperless-gpt", got.Get("X-Title"))

	resp, err = http.DefaultClient.Get(server.URL)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Empty(t, got.Get("Authorization"), "http.DefaultClient must not carry OPENAI_HEADERS")
	assert.Empty(t, got.Get("X-Title"))
}

// The metadata Google AI client used to be returned bare. Suggestion
// generation then ignored LLM_REQUESTS_PER_MINUTE and LLM_MAX_RETRIES,
// while the vision Google AI client and every other provider were wrapped.
func TestCreateLLMGoogleAIIsRateLimited(t *testing.T) {
	t.Setenv("GOOGLEAI_API_KEY", "test-key")
	t.Setenv("LLM_REQUESTS_PER_MINUTE", "8")

	previousProvider, previousModel := llmProvider, llmModel
	llmProvider, llmModel = "googleai", "gemini-test"
	t.Cleanup(func() { llmProvider, llmModel = previousProvider, previousModel })

	model, err := createLLM()
	require.NoError(t, err)

	wrapped, ok := model.(*RateLimitedLLM)
	require.True(t, ok, "Google AI metadata calls must go through the same rate-limit wrapper as the other providers")
	_, isGoogle := wrapped.llm.(*GoogleAIProvider)
	require.True(t, isGoogle)
	require.NotNil(t, wrapped.rateLimiter)
	assert.Equal(t, rate.Limit(8.0/60.0), wrapped.rateLimiter.Limit())
}
