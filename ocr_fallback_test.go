package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"text/template"

	"paperless-gpt/ocr"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type wholePDFFallbackClient struct {
	pdfCalls []struct {
		documentID int
		limitPages int
		split      bool
	}
}

func (c *wholePDFFallbackClient) GetDocumentsByTag(ctx context.Context, tag string, pageSize int) ([]Document, error) {
	return nil, nil
}

func (c *wholePDFFallbackClient) GetDocumentCountByTag(ctx context.Context, tag string) (int, error) {
	return 0, nil
}

func (c *wholePDFFallbackClient) UpdateDocuments(ctx context.Context, documents []DocumentSuggestion, db *gorm.DB, isUndo bool) error {
	return nil
}

func (c *wholePDFFallbackClient) GetDocument(ctx context.Context, documentID int) (Document, error) {
	return Document{ID: documentID}, nil
}

func (c *wholePDFFallbackClient) GetSimilarDocuments(ctx context.Context, documentID int, count int) ([]Document, error) {
	return nil, nil
}

func (c *wholePDFFallbackClient) GetAllTags(ctx context.Context) (map[string]int, error) {
	return map[string]int{}, nil
}

func (c *wholePDFFallbackClient) GetAllCorrespondents(ctx context.Context) (map[string]int, error) {
	return map[string]int{}, nil
}

func (c *wholePDFFallbackClient) GetAllDocumentTypes(ctx context.Context) ([]DocumentType, error) {
	return nil, nil
}

func (c *wholePDFFallbackClient) GetCustomFields(ctx context.Context) ([]CustomField, error) {
	return nil, nil
}

func (c *wholePDFFallbackClient) CreateTag(ctx context.Context, tagName string) (int, error) {
	return 0, nil
}

func (c *wholePDFFallbackClient) DownloadDocumentAsImages(ctx context.Context, documentID int, pageLimit int) ([]string, int, error) {
	return nil, 0, fmt.Errorf("unexpected image download")
}

func (c *wholePDFFallbackClient) DownloadDocumentAsPDF(ctx context.Context, documentID int, limitPages int, split bool) ([]string, []byte, int, error) {
	c.pdfCalls = append(c.pdfCalls, struct {
		documentID int
		limitPages int
		split      bool
	}{documentID: documentID, limitPages: limitPages, split: split})

	if !split {
		return nil, []byte("%PDF-1.7 whole"), 2, nil
	}

	tempDir, err := os.MkdirTemp("", "paperless-gpt-pdf-pages-*")
	if err != nil {
		return nil, nil, 0, err
	}

	page1 := filepath.Join(tempDir, "original_001.pdf")
	page2 := filepath.Join(tempDir, "original_002.pdf")
	if err := os.WriteFile(page1, []byte("%PDF-1.7 page1"), 0644); err != nil {
		return nil, nil, 0, err
	}
	if err := os.WriteFile(page2, []byte("%PDF-1.7 page2"), 0644); err != nil {
		return nil, nil, 0, err
	}

	return []string{page1, page2}, []byte("%PDF-1.7 whole"), 2, nil
}

func (c *wholePDFFallbackClient) UploadDocument(ctx context.Context, data []byte, filename string, metadata map[string]interface{}) (string, error) {
	return "", nil
}

func (c *wholePDFFallbackClient) GetTaskStatus(ctx context.Context, taskID string) (map[string]interface{}, error) {
	return nil, nil
}

func (c *wholePDFFallbackClient) DeleteDocument(ctx context.Context, documentID int) error {
	return nil
}

type wholePDFFallbackProvider struct {
	pagesSeen []int
}

func (p *wholePDFFallbackProvider) ProcessImage(ctx context.Context, imageContent []byte, pageNumber int) (*ocr.OCRResult, error) {
	p.pagesSeen = append(p.pagesSeen, pageNumber)
	if pageNumber == 0 {
		return nil, fmt.Errorf("provider supports up to 1 page per request")
	}
	return &ocr.OCRResult{
		Text: fmt.Sprintf("page-%d", pageNumber),
	}, nil
}

func TestShouldFallbackWholePDF(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "page limit", err: fmt.Errorf("provider supports up to 8 pages"), want: true},
		{name: "payload too large", err: fmt.Errorf("status 413 payload too large"), want: true},
		{name: "generic error", err: fmt.Errorf("temporary network failure"), want: false},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, shouldFallbackWholePDF(tc.err))
		})
	}
}

func TestProcessDocumentOCR_WholePDFFallsBackToPDFMode(t *testing.T) {
	ocrTemplate = template.Must(template.New("ocr").Parse(""))

	client := &wholePDFFallbackClient{}
	provider := &wholePDFFallbackProvider{}
	app := &App{
		Client:             client,
		ocrProvider:        provider,
		ocrProcessMode:     "whole_pdf",
		pdfSkipExistingOCR: false,
	}

	doc, err := app.ProcessDocumentOCR(context.Background(), 42, OCROptions{ProcessMode: "whole_pdf"}, "")
	require.NoError(t, err)
	require.NotNil(t, doc)
	assert.Equal(t, "page-1\n\npage-2", doc.Text)
	assert.Equal(t, []int{0, 1, 2}, provider.pagesSeen)
	require.Len(t, client.pdfCalls, 2)
	assert.False(t, client.pdfCalls[0].split)
	assert.True(t, client.pdfCalls[1].split)
}
