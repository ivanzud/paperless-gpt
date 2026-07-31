package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/gen2brain/go-fitz"
)

// SearchDocuments finds documents for the Playground picker. An empty query
// returns the most recently added documents; otherwise paperless-ngx's
// full-text search is used.
func (client *PaperlessClient) SearchDocuments(ctx context.Context, query string, pageSize int) ([]Document, error) {
	var path string
	if strings.TrimSpace(query) == "" {
		path = fmt.Sprintf("api/documents/?ordering=-added&page_size=%d", pageSize)
	} else {
		path = fmt.Sprintf("api/documents/?query=%s&page_size=%d", url.QueryEscape(query), pageSize)
	}

	resp, err := client.Do(ctx, "GET", path, nil)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed in SearchDocuments: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("error searching documents: status=%d, body=%s", resp.StatusCode, string(bodyBytes))
	}

	var documentsResponse GetDocumentsApiResponse
	if err := json.Unmarshal(bodyBytes, &documentsResponse); err != nil {
		return nil, fmt.Errorf("failed to parse JSON response: %w", err)
	}

	allTags, err := client.GetAllTags(ctx)
	if err != nil {
		return nil, err
	}
	allCorrespondents, err := client.GetAllCorrespondents(ctx)
	if err != nil {
		return nil, err
	}

	documents := make([]Document, 0, len(documentsResponse.Results))
	for _, result := range documentsResponse.Results {
		tagNames := make([]string, len(result.Tags))
		for i, resultTagID := range result.Tags {
			for tagName, tagID := range allTags {
				if resultTagID == tagID {
					tagNames[i] = tagName
					break
				}
			}
		}

		correspondentName := ""
		if result.Correspondent != 0 {
			for name, id := range allCorrespondents {
				if result.Correspondent == id {
					correspondentName = name
					break
				}
			}
		}

		documents = append(documents, Document{
			ID:            result.ID,
			Title:         result.Title,
			Content:       result.Content,
			Correspondent: correspondentName,
			Tags:          tagNames,
			CreatedDate:   result.CreatedDate,
		})
	}

	return documents, nil
}

// GetDocumentPageImage renders one page of a document as a JPEG for the
// Playground's scan-next-to-text view. Rendered pages are cached on disk
// (separately from the OCR pipeline's temporary page images, which get
// deleted after each run).
func (client *PaperlessClient) GetDocumentPageImage(ctx context.Context, documentID int, pageIndex int) ([]byte, error) {
	if pageIndex < 0 {
		return nil, fmt.Errorf("page index must not be negative")
	}

	cacheFolder, err := filepath.Abs(client.GetCacheFolder())
	if err != nil {
		return nil, fmt.Errorf("resolve cache folder: %w", err)
	}
	docDir, err := pathWithinBase(cacheFolder, fmt.Sprintf("document-%d", documentID))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(docDir, 0750); err != nil {
		return nil, err
	}
	previewPath, err := pathWithinBase(docDir, fmt.Sprintf("preview-page%03d.jpg", pageIndex))
	if err != nil {
		return nil, err
	}
	// #nosec G304 -- pathWithinBase confines this integer-derived name to the cache directory.
	if data, err := os.ReadFile(previewPath); err == nil {
		return data, nil
	}

	// Download the PDF and render the requested page.
	path := fmt.Sprintf("api/documents/%d/download/", documentID)
	resp, err := client.Do(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("error downloading document %d: %d, %s", documentID, resp.StatusCode, string(bodyBytes))
	}
	pdfData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	tmpFile, err := os.CreateTemp("", "document-preview-*.pdf")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmpFile.Name())
	_, writeErr := tmpFile.Write(pdfData)
	closeErr := tmpFile.Close()
	if writeErr != nil {
		if closeErr != nil {
			return nil, fmt.Errorf("write preview PDF: %w (close also failed: %v)", writeErr, closeErr)
		}
		return nil, writeErr
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close preview PDF: %w", closeErr)
	}

	doc, err := fitz.New(tmpFile.Name())
	if err != nil {
		return nil, err
	}
	defer doc.Close()

	if pageIndex >= doc.NumPage() {
		return nil, fmt.Errorf("page %d out of range: document has %d pages", pageIndex+1, doc.NumPage())
	}

	// Render at a DPI that keeps the preview readable but bounded in size.
	rect, err := doc.Bound(pageIndex)
	if err != nil {
		return nil, err
	}
	const targetWidth = 1200.0
	dpi := math.Min(150, math.Max(72, targetWidth/(float64(rect.Dx())/72.0)))
	img, err := doc.ImageDPI(pageIndex, dpi)
	if err != nil {
		return nil, err
	}

	buf := new(bytes.Buffer)
	if err := jpeg.Encode(buf, img, &jpeg.Options{Quality: 80}); err != nil {
		return nil, err
	}

	if err := os.WriteFile(previewPath, buf.Bytes(), 0600); err != nil {
		log.Warnf("Failed to cache page preview for document %d page %d: %v", documentID, pageIndex, err)
	}
	return buf.Bytes(), nil
}

func pathWithinBase(base string, parts ...string) (string, error) {
	basePath, err := filepath.Abs(base)
	if err != nil {
		return "", fmt.Errorf("resolve base path: %w", err)
	}
	path := filepath.Join(append([]string{basePath}, parts...)...)
	path, err = filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve candidate path: %w", err)
	}
	relative, err := filepath.Rel(basePath, path)
	if err != nil {
		return "", fmt.Errorf("compare candidate path with base: %w", err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes base directory %q", path, basePath)
	}
	return path, nil
}
