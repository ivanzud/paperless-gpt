package ocr

import "github.com/tmc/langchaingo/llms"

func ollamaThinkingCallOptions(think *bool) []llms.CallOption {
	if think == nil {
		return nil
	}

	mode := llms.ThinkingModeNone
	if *think {
		mode = llms.ThinkingModeAuto
	}

	return []llms.CallOption{llms.WithThinkingMode(mode)}
}
