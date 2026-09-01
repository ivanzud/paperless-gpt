package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCorrespondentPromptLimit(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    int
		wantErr bool
	}{
		{name: "unset", raw: "", want: 0},
		{name: "disabled", raw: "0", want: 0},
		{name: "positive", raw: "25", want: 25},
		{name: "negative", raw: "-1", wantErr: true},
		{name: "not an integer", raw: "twenty-five", wantErr: true},
		{name: "whitespace is invalid", raw: " 25 ", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseCorrespondentPromptLimit(test.raw)
			if test.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "CORRESPONDENT_PROMPT_LIMIT")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}
