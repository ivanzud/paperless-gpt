package ocr

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
