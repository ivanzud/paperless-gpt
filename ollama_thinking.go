package main

import (
	"os"
	"strconv"
	"strings"

	"github.com/tmc/langchaingo/llms"
)

func parseOptionalBoolEnv(key string) *bool {
	value, ok := os.LookupEnv(key)
	if !ok {
		return nil
	}

	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}

	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return nil
	}

	return &parsed
}

func resolveVisionOllamaThink() *bool {
	if visionOllamaThink != nil {
		return visionOllamaThink
	}
	return ollamaThink
}

func ollamaThinkingCallOptions(provider string, think *bool) []llms.CallOption {
	if strings.ToLower(provider) != "ollama" || think == nil {
		return nil
	}

	mode := llms.ThinkingModeNone
	if *think {
		mode = llms.ThinkingModeAuto
	}

	return []llms.CallOption{llms.WithThinkingMode(mode)}
}

func mainLLMCallOptions() []llms.CallOption {
	return ollamaThinkingCallOptions(llmProvider, ollamaThink)
}
