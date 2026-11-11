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
	pages          []int
}

func (p *degenerateOnPageProvider) ProcessImage(_ context.Context, _ []byte, pageNumber int) (*ocr.OCRResult, error) {
	p.pages = append(p.pages, pageNumber)
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

		provider := &degenerateOnPageProvider{degeneratePage: 3}
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
	}
}
