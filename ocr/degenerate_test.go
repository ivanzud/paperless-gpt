package ocr

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tmc/langchaingo/llms"
)

// syntheticStatement builds a long numeric statement: many rows that look
// alike but are all different.
func syntheticStatement(rows int) string {
	var b strings.Builder
	b.WriteString("Example Bank - Statement for Jane Doe\n\n| Date | Description | Amount | Balance |\n| --- | --- | --- | --- |\n")
	for i := 0; i < rows; i++ {
		fmt.Fprintf(&b, "| 01/%02d | Card purchase %04d | %d.%02d | %d.00 |\n", i%28+1, 1000+i, 10+i, i%100, 5000-i*7)
	}
	return b.String()
}

func TestDegenerateOCRReason(t *testing.T) {
	loopLine := "Payment received, thank you.\n"
	tests := []struct {
		name            string
		text            string
		commentaryChars int
		limitHit        bool
		want            string
	}{
		// Degenerate output.
		{name: "non-empty text that stopped at the token cap", text: "Invoice 1234\nTotal 12.00", limitHit: true, want: DegenerateReasonTokenLimit},
		{name: "one line repeated until it dominates the page", text: "Acme Corp\nInvoice 1234\n" + strings.Repeat(loopLine, 60), want: DegenerateReasonRepetition},
		{name: "short unit looping inside a single line", text: "Total due " + strings.Repeat("1. ", 400), want: DegenerateReasonRepetition},
		{name: "one character looping", text: "Ref: " + strings.Repeat("0", 900), want: DegenerateReasonRepetition},
		{name: "table row looping", text: "| Item | Qty |\n| --- | --- |\n" + strings.Repeat("| Widget | 2 |\n", 80), want: DegenerateReasonRepetition},
		{name: "mixed symbol noise", text: "Job cost summary\nMaterials 120.00\n" + strings.Repeat("*+**+*+++*oe5-", 30), want: DegenerateReasonNoise},
		{name: "short page that is mostly symbol noise", text: "Subtotal | -0-0-0-.0000-++++*+*+*+-|-|+*+*+**++ | 12", want: DegenerateReasonNoise},
		{name: "commentary outweighs the transcription", text: "Page 2", commentaryChars: 120, want: DegenerateReasonCommentary},
		{name: "nothing but commentary", text: "", commentaryChars: 80, want: DegenerateReasonCommentary},
		{name: "description of the picture instead of its text", text: "The image shows a scanned letter with a company logo in the top left corner and several paragraphs of text.", commentaryChars: 90, want: DegenerateReasonCommentary},

		// Legitimate output that must pass.
		{name: "empty page", text: ""},
		{name: "empty text at the token cap stays a blank page", text: "", limitHit: true},
		{name: "ordinary letter", text: "Dear Jane Doe,\n\nThank you for your payment of 120.00 to Acme.\n\nSincerely,\nJohn Doe"},
		{name: "long numeric statement with distinct rows", text: syntheticStatement(120)},
		{name: "form with many identical empty rows", text: "Mileage log\n\n| Date | From | To | Miles |\n| --- | --- | --- | --- |\n" + strings.Repeat("|  |  |  |  |\n", 60)},
		{name: "form with many identical checkbox rows", text: "Checklist\n\n" + strings.Repeat("| [ ] | [ ] | [ ] |\n", 70)},
		{name: "identical filled rows next to other content", text: syntheticStatement(60) + strings.Repeat("| Monthly fee | 0.00 | 0.00 |\n", 30)},
		{name: "ruler and dot-leader lines", text: "Contents\n" + strings.Repeat("Chapter ........................................ 12\n", 3) + strings.Repeat("-", 700) + "\n" + strings.Repeat("_", 700)},
		{name: "repeated value in a short list", text: strings.Repeat("N/A\n", 25) + "Signed: Jane Doe, 1234 Maple Court"},
		{name: "masked account digits and markdown emphasis", text: "**Account:** ************1234\n**Total:** $***.** (see ***note***)\nDue: --/--/----"},
		{name: "several copies of the same form on one page", text: strings.Repeat(syntheticStatement(8)+"See notice on the back of copy B.\n", 4)},
		{name: "some commentary next to a real transcription", text: syntheticStatement(10), commentaryChars: 60},
		{name: "wide table separator", text: "| a | b | c | d | e | f | g | h |\n|:---:|:---:|:---:|:---:|:---:|:---:|:---:|:---:|\n| 1 | 2 | 3 | 4 | 5 | 6 | 7 | 8 |"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reason, detail := degenerateOCRReason(tc.text, tc.commentaryChars, tc.limitHit)
			assert.Equal(t, tc.want, reason, detail)
			if reason != "" {
				assert.NotEmpty(t, detail)
			}
		})
	}
}

func TestSanitizeOCRTextStripsBracketedCommentary(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		expected       string
		wantCommentary bool
	}{
		{
			name:           "legibility commentary line is removed",
			input:          "Acme Corp\n[The body of the document is extremely faint and mostly illegible. It appears to be a formal letter.]\nPage 1 of 2",
			expected:       "Acme Corp\nPage 1 of 2",
			wantCommentary: true,
		},
		{
			name:     "short illegible marker is kept",
			input:    "Signed: [illegible]\n[Signature: illegible]\nDate: 01/02/2020",
			expected: "Signed: [illegible]\n[Signature: illegible]\nDate: 01/02/2020",
		},
		{
			name:     "bracketed document text is kept",
			input:    "[ THIS PAGE INTENTIONALLY LEFT BLANK ]\n[Calls may be monitored and/or recorded for quality assurance purposes.]",
			expected: "[ THIS PAGE INTENTIONALLY LEFT BLANK ]\n[Calls may be monitored and/or recorded for quality assurance purposes.]",
		},
		{
			name:     "brackets inside a sentence are not commentary",
			input:    "The copy may be faint [see the note on page 2 about illegible carbon copies] but remains valid.",
			expected: "The copy may be faint [see the note on page 2 about illegible carbon copies] but remains valid.",
		},
		{
			name:           "picture description is counted but kept",
			input:          "This image shows a utility bill.\nAcme Power\nAmount due 12.00",
			expected:       "This image shows a utility bill.\nAcme Power\nAmount due 12.00",
			wantCommentary: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			text, commentary := sanitizeOCRTextWithCommentary(tc.input)
			assert.Equal(t, tc.expected, text)
			assert.Equal(t, tc.wantCommentary, commentary > 0)
			// The sanitizer runs again on stored text; it must be stable.
			assert.Equal(t, text, sanitizeOCRText(text))
		})
	}
}

func TestIsLengthStopReason(t *testing.T) {
	for _, reason := range []string{"length", "LENGTH", "max_tokens", "MAX_TOKENS", "FinishReasonMaxTokens"} {
		assert.True(t, isLengthStopReason(reason), reason)
	}
	for _, reason := range []string{"", "stop", "end_turn", "content_filter", "tool_calls"} {
		assert.False(t, isLengthStopReason(reason), reason)
	}
}

func TestAsDegenerateOutput(t *testing.T) {
	inner := &DegenerateOutputError{Reason: DegenerateReasonNoise, Detail: "10 of 20 characters are symbol noise"}
	wrapped := fmt.Errorf("error performing OCR for document 1, page 2: %w", inner)
	assert.Same(t, inner, AsDegenerateOutput(wrapped))
	assert.Nil(t, AsDegenerateOutput(errors.New("status code: 500")))
	assert.Nil(t, AsDegenerateOutput(nil))
	assert.Contains(t, inner.Error(), DegenerateReasonNoise)
}

// scriptedVisionLLM answers each call with the next queued choice and records
// the temperature it was called with.
type scriptedVisionLLM struct {
	choices      []*llms.ContentChoice
	calls        int
	temperatures []float64
}

func (s *scriptedVisionLLM) GenerateContent(_ context.Context, _ []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
	opts := llms.CallOptions{Temperature: -1}
	for _, option := range options {
		option(&opts)
	}
	s.temperatures = append(s.temperatures, opts.Temperature)
	i := s.calls
	s.calls++
	if i >= len(s.choices) {
		i = len(s.choices) - 1
	}
	return &llms.ContentResponse{Choices: []*llms.ContentChoice{s.choices[i]}}, nil
}

func (s *scriptedVisionLLM) Call(context.Context, string, ...llms.CallOption) (string, error) {
	return "", errors.New("not used")
}

func usage(completionTokens, totalTokens int) map[string]any {
	return map[string]any{"CompletionTokens": completionTokens, "PromptTokens": totalTokens - completionTokens, "TotalTokens": totalTokens}
}

func TestProcessImage_LimitHitUsesCompletionTokens(t *testing.T) {
	const page = "Invoice 1234\nJane Doe\nTotal due 12.00"
	tests := []struct {
		name         string
		maxTokens    int
		choice       *llms.ContentChoice
		wantLimitHit bool
	}{
		{
			// A page image costs thousands of prompt tokens: total tokens above
			// the cap say nothing about the completion.
			name:      "large prompt with a small completion is not a limit hit",
			maxTokens: 4096,
			choice:    &llms.ContentChoice{Content: page, StopReason: "stop", GenerationInfo: usage(300, 9000)},
		},
		{
			name:         "completion tokens at the cap",
			maxTokens:    4096,
			choice:       &llms.ContentChoice{Content: page, StopReason: "stop", GenerationInfo: usage(4096, 9000)},
			wantLimitHit: true,
		},
		{
			name:         "finish reason length without usage data and without a configured cap",
			maxTokens:    0,
			choice:       &llms.ContentChoice{Content: page, StopReason: "length"},
			wantLimitHit: true,
		},
		{
			name:      "normal stop without usage data",
			maxTokens: 4096,
			choice:    &llms.ContentChoice{Content: page, StopReason: "stop"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OCR_DEGENERATE_RETRIES", "0")
			provider := retryTestProvider(&scriptedVisionLLM{choices: []*llms.ContentChoice{tc.choice}})
			provider.maxTokens = tc.maxTokens

			result, err := provider.ProcessImage(context.Background(), testJPEG(t), 1)
			if !tc.wantLimitHit {
				require.NoError(t, err)
				assert.False(t, result.OcrLimitHit)
				assert.Equal(t, page, result.Text)
				return
			}
			degenerate := AsDegenerateOutput(err)
			require.NotNil(t, degenerate, "a page cut off at the cap must be rejected, got result %v and error %v", result, err)
			assert.Nil(t, result)
			assert.Equal(t, DegenerateReasonTokenLimit, degenerate.Reason)
			require.NotNil(t, degenerate.Result)
			assert.True(t, degenerate.Result.OcrLimitHit)
			assert.NotContains(t, err.Error(), "Jane Doe", "the error must not carry document text")
		})
	}
}

func TestProcessImage_RegeneratesDegenerateOutput(t *testing.T) {
	loop := &llms.ContentChoice{Content: "Acme\n" + strings.Repeat("Payment received, thank you.\n", 80), StopReason: "stop"}
	good := &llms.ContentChoice{Content: "Acme\nPayment received, thank you.\nBalance 0.00", StopReason: "stop"}
	zero := 0.0

	t.Run("one regeneration by default, then the good answer is used", func(t *testing.T) {
		llm := &scriptedVisionLLM{choices: []*llms.ContentChoice{loop, good}}
		provider := retryTestProvider(llm)
		provider.temperature = &zero

		result, err := provider.ProcessImage(context.Background(), testJPEG(t), 1)
		require.NoError(t, err)
		assert.Equal(t, "Acme\nPayment received, thank you.\nBalance 0.00", result.Text)
		assert.Equal(t, 2, llm.calls)
		assert.Equal(t, []float64{0, 0}, llm.temperatures, "the regeneration repeats the request unchanged by default")
	})

	t.Run("still degenerate after the regeneration fails the page", func(t *testing.T) {
		llm := &scriptedVisionLLM{choices: []*llms.ContentChoice{loop}}
		result, err := retryTestProvider(llm).ProcessImage(context.Background(), testJPEG(t), 1)

		assert.Nil(t, result)
		degenerate := AsDegenerateOutput(err)
		require.NotNil(t, degenerate)
		assert.Equal(t, DegenerateReasonRepetition, degenerate.Reason)
		assert.Equal(t, 2, llm.calls)
	})

	t.Run("regeneration can be disabled", func(t *testing.T) {
		t.Setenv("OCR_DEGENERATE_RETRIES", "0")
		llm := &scriptedVisionLLM{choices: []*llms.ContentChoice{loop, good}}
		_, err := retryTestProvider(llm).ProcessImage(context.Background(), testJPEG(t), 1)

		require.NotNil(t, AsDegenerateOutput(err))
		assert.Equal(t, 1, llm.calls)
	})

	t.Run("regeneration temperature applies to the retry only", func(t *testing.T) {
		t.Setenv("OCR_DEGENERATE_RETRY_TEMPERATURE", "0.3")
		llm := &scriptedVisionLLM{choices: []*llms.ContentChoice{loop, good}}
		provider := retryTestProvider(llm)
		provider.temperature = &zero

		_, err := provider.ProcessImage(context.Background(), testJPEG(t), 1)
		require.NoError(t, err)
		assert.Equal(t, []float64{0, 0.3}, llm.temperatures)
	})

	t.Run("commentary instead of a transcription is rejected", func(t *testing.T) {
		commentary := &llms.ContentChoice{Content: "[The text on this page is extremely faint and largely illegible. It appears to be a letter.]\n<|im_end|>\n|im_end|>", StopReason: "stop"}
		llm := &scriptedVisionLLM{choices: []*llms.ContentChoice{commentary}}
		_, err := retryTestProvider(llm).ProcessImage(context.Background(), testJPEG(t), 1)

		degenerate := AsDegenerateOutput(err)
		require.NotNil(t, degenerate)
		assert.Equal(t, DegenerateReasonCommentary, degenerate.Reason)
	})
}
