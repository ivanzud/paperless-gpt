package main

import (
	"fmt"
	"os"
	"strconv"
)

func parseCorrespondentPromptLimit(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}

	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, fmt.Errorf(
			"invalid CORRESPONDENT_PROMPT_LIMIT value %q (must be a non-negative integer; 0 sends the full list)",
			raw,
		)
	}
	return value, nil
}

func loadCorrespondentPromptLimit() int {
	value, err := parseCorrespondentPromptLimit(os.Getenv("CORRESPONDENT_PROMPT_LIMIT"))
	if err != nil {
		log.Fatal(err)
	}
	return value
}
