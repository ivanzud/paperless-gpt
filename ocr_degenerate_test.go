package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"

	"paperless-gpt/ocr"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// degenerateOnPageProvider transcribes every page except one, for which it
// reports degenerate output the way the LLM provider does.
type degenerateOnPageProvider struct {
	degeneratePage int
	// noText makes the degenerate page one the model found no text on.
	noText bool
	// everyPage makes every page degenerate, not only degeneratePage.
	everyPage bool
	pages     []int
}

func (p *degenerateOnPageProvider) ProcessImage(_ context.Context, _ []byte, pageNumber int) (*ocr.OCRResult, error) {
	p.pages = append(p.pages, pageNumber)
	if p.everyPage {
		pageNumber = p.degeneratePage
	}
	if pageNumber == p.degeneratePage && p.noText {
		return nil, &ocr.DegenerateOutputError{
			Reason: ocr.DegenerateReasonCommentary,
			Detail: "134 commentary characters against 0 transcribed",
			Result: &ocr.OCRResult{},
			NoText: true,
		}
	}
	if pageNumber == p.degeneratePage {
		return nil, &ocr.DegenerateOutputError{
			Reason: ocr.DegenerateReasonRepetition,
			Detail: "a 12-byte unit repeats 400 times",
			Result: &ocr.OCRResult{Text: fmt.Sprintf("page %d rejected model output", pageNumber)},
		}
	}
	return &ocr.OCRResult{Text: fmt.Sprintf("page %d scanned document text", pageNumber)}, nil
}

func TestParseDegeneratePageAction(t *testing.T) {
	assert.Equal(t, degeneratePageActionFail, parseDegeneratePageAction(""))
	assert.Equal(t, degeneratePageActionFail, parseDegeneratePageAction("fail"))
	assert.Equal(t, degeneratePageActionFail, parseDegeneratePageAction("nonsense"))
	assert.Equal(t, degeneratePageActionSkip, parseDegeneratePageAction(" Skip "))
	assert.Equal(t, degeneratePageActionAccept, parseDegeneratePageAction("ACCEPT"))
}

func TestProcessDocumentOCR_DegeneratePagePolicy(t *testing.T) {
	pdfContent, err := os.ReadFile("tests/pdf/five-pager.pdf")
	require.NoError(t, err)

	prevSkip, prevAction := ocrSkipFailedPages, ocrDegeneratePageAction
	t.Cleanup(func() { ocrSkipFailedPages, ocrDegeneratePageAction = prevSkip, prevAction })

	noTextPage, everyPage := false, false
	run := func(t *testing.T, mode string) (*ProcessedDocument, *degenerateOnPageProvider, error) {
		env := newTestEnv(t)
		t.Cleanup(env.teardown)
		require.NoError(t, env.db.AutoMigrate(&OCRPageResult{}))
		const documentID = 2042
		env.setMockResponse(fmt.Sprintf("/api/documents/%d/download/", documentID), func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(pdfContent)
		})
		env.client.CacheFolder = t.TempDir()

		provider := &degenerateOnPageProvider{degeneratePage: 3, noText: noTextPage, everyPage: everyPage}
		app := &App{Client: env.client, Database: env.db, ocrProvider: provider, ocrProcessMode: mode}
		processed, err := app.ProcessDocumentOCR(context.Background(), documentID, OCROptions{
			LimitPages:     0,
			ProcessMode:    mode,
			PromptOverride: "OCR",
		}, "")
		return processed, provider, err
	}

	for _, mode := range []string{"image", "pdf"} {
		t.Run(mode+": fail is the default even when failed pages are skipped", func(t *testing.T) {
			// OCR_SKIP_FAILED_PAGES exists for pages without text (blank
			// backs of duplex scans). It must not wave garbage through.
			ocrSkipFailedPages = true
			ocrDegeneratePageAction = parseDegeneratePageAction("")

			processed, provider, err := run(t, mode)
			require.Error(t, err)
			assert.Nil(t, processed)
			assert.NotNil(t, ocr.AsDegenerateOutput(err), "callers must be able to recognize the failure: %v", err)
			assert.Equal(t, []int{1, 2, 3}, provider.pages, "the document fails at the degenerate page")
		})

		t.Run(mode+": skip leaves the page out", func(t *testing.T) {
			ocrSkipFailedPages = false
			ocrDegeneratePageAction = degeneratePageActionSkip

			processed, provider, err := run(t, mode)
			require.NoError(t, err)
			assert.Equal(t, []int{1, 2, 3, 4, 5}, provider.pages)
			assert.Contains(t, processed.Text, "page 2 scanned document text")
			assert.Contains(t, processed.Text, "page 4 scanned document text")
			assert.NotContains(t, processed.Text, "rejected model output")
		})

		t.Run(mode+": accept stores the output as before", func(t *testing.T) {
			ocrSkipFailedPages = false
			ocrDegeneratePageAction = degeneratePageActionAccept

			processed, _, err := run(t, mode)
			require.NoError(t, err)
			assert.Contains(t, processed.Text, "page 3 rejected model output")
		})

		// A page the model found no text on is a page without text, not a
		// page with garbage: OCR_SKIP_FAILED_PAGES leaves it out.
		for _, tc := range []struct {
			name      string
			skipPages bool
			action    string
			wantOK    bool
		}{
			{name: "fails the document by default", action: degeneratePageActionFail},
			{name: "is left out when failed pages are skipped", skipPages: true, action: degeneratePageActionFail, wantOK: true},
			{name: "is left out with action skip", action: degeneratePageActionSkip, wantOK: true},
			{name: "is left out with action skip and skipped failed pages", skipPages: true, action: degeneratePageActionSkip, wantOK: true},
			{name: "fails the document with action accept unless failed pages are skipped", action: degeneratePageActionAccept},
			{name: "is left out with action accept and skipped failed pages", skipPages: true, action: degeneratePageActionAccept, wantOK: true},
		} {
			t.Run(mode+": a page without text "+tc.name, func(t *testing.T) {
				noTextPage = true
				t.Cleanup(func() { noTextPage = false })
				ocrSkipFailedPages = tc.skipPages
				ocrDegeneratePageAction = tc.action

				processed, provider, err := run(t, mode)
				if !tc.wantOK {
					require.Error(t, err)
					assert.Nil(t, processed)
					assert.Equal(t, []int{1, 2, 3}, provider.pages)
					return
				}
				require.NoError(t, err)
				assert.Equal(t, []int{1, 2, 3, 4, 5}, provider.pages)
				assert.Contains(t, processed.Text, "page 2 scanned document text")
				assert.Contains(t, processed.Text, "page 4 scanned document text")
				assert.NotContains(t, processed.Text, "page 3")
			})
		}

		// A document with nothing but such pages has no text at all. It fails,
		// and the failure stays degenerate output, so that it is given up on
		// at once instead of being run again.
		t.Run(mode+": a document of pages without text fails as degenerate", func(t *testing.T) {
			noTextPage, everyPage = true, true
			t.Cleanup(func() { noTextPage, everyPage = false, false })
			ocrSkipFailedPages = true
			ocrDegeneratePageAction = degeneratePageActionFail

			processed, provider, err := run(t, mode)
			require.Error(t, err)
			assert.Nil(t, processed)
			assert.Equal(t, []int{1, 2, 3, 4, 5}, provider.pages)
			degenerate := ocr.AsDegenerateOutput(err)
			require.NotNil(t, degenerate)
			assert.True(t, degenerate.NoText)
			reason, giveUp := ocrGiveUpReason(err, 1)
			assert.True(t, giveUp)
			assert.Equal(t, "OCR produced degenerate output", reason)
		})

		t.Run(mode+": a document of garbage pages that are skipped fails as before", func(t *testing.T) {
			everyPage = true
			t.Cleanup(func() { everyPage = false })
			ocrSkipFailedPages = true
			ocrDegeneratePageAction = degeneratePageActionSkip

			processed, provider, err := run(t, mode)
			require.Error(t, err)
			assert.Nil(t, processed)
			assert.Equal(t, []int{1, 2, 3, 4, 5}, provider.pages)
			assert.Nil(t, ocr.AsDegenerateOutput(err))
		})
	}
}

func TestAllPagesFailedError(t *testing.T) {
	noTextErr := fmt.Errorf("page 1: %w", &ocr.DegenerateOutputError{Reason: ocr.DegenerateReasonCommentary, NoText: true})
	garbageErr := &ocr.DegenerateOutputError{Reason: ocr.DegenerateReasonRepetition}

	var noText noTextPages
	noText.note(garbageErr, false)
	noText.note(fmt.Errorf("timeout"), false)
	noText.note(noTextErr, true)
	assert.Zero(t, noText.count, "only pages without text that OCR_SKIP_FAILED_PAGES left out count")
	assert.Nil(t, ocr.AsDegenerateOutput(allPagesFailedError(7, []int{1, 2, 3}, noText)))

	noText.note(noTextErr, false)
	noText.note(noTextErr, false)
	assert.Equal(t, 2, noText.count)
	assert.NotNil(t, ocr.AsDegenerateOutput(allPagesFailedError(7, []int{1, 2}, noText)))
	assert.Nil(t, ocr.AsDegenerateOutput(allPagesFailedError(7, []int{1, 2, 3}, noText)), "a page that failed for another reason may succeed on a retry")
}

func TestParseVisionLLMStopSequences(t *testing.T) {
	for _, raw := range []string{"", "   ", ",", " , ,"} {
		assert.Empty(t, parseVisionLLMStopSequences(raw), "%q must not set the parameter", raw)
	}
	assert.Equal(t, []string{"<|im_end|>"}, parseVisionLLMStopSequences("<|im_end|>"))
	assert.Equal(t, []string{"<|im_end|>", "END"}, parseVisionLLMStopSequences(" <|im_end|> , END ,, END"))
	assert.Equal(t, []string{"a", "b", "c", "d"}, parseVisionLLMStopSequences("a,b,c,d,e"), "the API takes at most four")
}

func TestParseVisionLLMFrequencyPenalty(t *testing.T) {
	for _, raw := range []string{"", "   ", "0", "0.0", "abc", "NaN", "2.5", "-3", "Inf"} {
		assert.Nil(t, parseVisionLLMFrequencyPenalty(raw), "%q must not set the parameter", raw)
	}
	for raw, want := range map[string]float64{"0.3": 0.3, " 1 ": 1, "-0.5": -0.5, "2": 2} {
		got := parseVisionLLMFrequencyPenalty(raw)
		require.NotNil(t, got, raw)
		assert.InDelta(t, want, *got, 1e-9)
	}
}
