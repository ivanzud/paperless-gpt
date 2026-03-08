package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/tmc/langchaingo/llms"
)

func TestOllamaThinkingCallOptions_Disabled(t *testing.T) {
	enabled := false
	opts := ollamaThinkingCallOptions("ollama", &enabled)
	assert.Len(t, opts, 1)

	var callOpts llms.CallOptions
	opts[0](&callOpts)

	cfg := llms.GetThinkingConfig(&callOpts)
	if assert.NotNil(t, cfg) {
		assert.Equal(t, llms.ThinkingModeNone, cfg.Mode)
	}
}

func TestOllamaThinkingCallOptions_NonOllama(t *testing.T) {
	enabled := false
	assert.Nil(t, ollamaThinkingCallOptions("openai", &enabled))
}
