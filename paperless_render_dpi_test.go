package main

import (
	"context"
	"fmt"
	"image"
	_ "image/jpeg"
	"math"
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPageRenderDPI(t *testing.T) {
	const (
		letterW = 612.0
		letterH = 792.0
	)

	tests := []struct {
		name         string
		wPts, hPts   float64
		maxDimension int
		maxPixels    int
		maxDPI       int
		expected     float64
	}{
		{name: "letter page limited by the longest side", wPts: letterW, hPts: letterH, maxDimension: 2000, maxPixels: 40_000_000, maxDPI: 600, expected: 181},
		{name: "letter page limited by the DPI ceiling", wPts: letterW, hPts: letterH, maxDimension: 10_000, maxPixels: 40_000_000, maxDPI: 300, expected: 300},
		{name: "letter page limited by total pixels", wPts: letterW, hPts: letterH, maxDimension: 10_000, maxPixels: 1_000_000, maxDPI: 600, expected: 103},
		{name: "oversized page goes below 72 DPI to fit", wPts: 3400, hPts: 1700, maxDimension: 2000, maxPixels: 40_000_000, maxDPI: 600, expected: 42},
		{name: "landscape and portrait are treated alike", wPts: 1700, hPts: 3400, maxDimension: 2000, maxPixels: 40_000_000, maxDPI: 600, expected: 42},
		{name: "enormous page never drops to zero", wPts: 14_400, hPts: 14_400, maxDimension: 10, maxPixels: 40_000_000, maxDPI: 600, expected: 1},
		{name: "disabled limits fall back to the DPI ceiling", wPts: letterW, hPts: letterH, maxDimension: 0, maxPixels: 0, maxDPI: 150, expected: 150},
		{name: "nothing configured", wPts: letterW, hPts: letterH, expected: 72},
		{name: "unknown page size uses the DPI ceiling", wPts: 0, hPts: 0, maxDimension: 2000, maxPixels: 40_000_000, maxDPI: 200, expected: 200},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dpi := pageRenderDPI(tt.wPts, tt.hPts, tt.maxDimension, tt.maxPixels, tt.maxDPI)
			assert.Equal(t, tt.expected, dpi)
			assert.Equal(t, math.Trunc(dpi), dpi, "the renderer takes whole DPI values")

			if tt.wPts <= 0 || dpi <= 1 {
				return
			}
			w := tt.wPts * dpi / 72
			h := tt.hPts * dpi / 72
			if tt.maxDimension > 0 {
				assert.LessOrEqual(t, math.Max(w, h), float64(tt.maxDimension), "longest side within the limit")
			}
			if tt.maxPixels > 0 {
				assert.LessOrEqual(t, w*h, float64(tt.maxPixels), "pixel count within the limit")
			}
		})
	}
}

// A page needs less than 72 DPI when its longest side must fit into fewer
// pixels than it has points. The rendered image must still respect the limit.
func TestDownloadDocumentAsImages_PixelLimitBeatsMinimumDPI(t *testing.T) {
	env := newTestEnv(t)
	defer env.teardown()

	prevDimension, prevPixels, prevDPI := imageMaxPixelDimension, imageMaxTotalPixels, imageMaxRenderDPI
	t.Cleanup(func() {
		imageMaxPixelDimension, imageMaxTotalPixels, imageMaxRenderDPI = prevDimension, prevPixels, prevDPI
	})
	imageMaxPixelDimension = 400
	imageMaxTotalPixels = 40_000_000
	imageMaxRenderDPI = 600

	pdfContent, err := os.ReadFile("tests/pdf/sample.pdf")
	require.NoError(t, err)

	const documentID = 456
	env.setMockResponse(fmt.Sprintf("/api/documents/%d/download/", documentID), func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(pdfContent)
	})
	env.client.CacheFolder = t.TempDir()

	imagePaths, _, err := env.client.DownloadDocumentAsImages(context.Background(), documentID, 0)
	require.NoError(t, err)
	require.Len(t, imagePaths, 1)

	f, err := os.Open(imagePaths[0])
	require.NoError(t, err)
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	require.NoError(t, err)

	longest := cfg.Width
	if cfg.Height > longest {
		longest = cfg.Height
	}
	assert.LessOrEqual(t, longest, 400, "rendered page is %dx%d", cfg.Width, cfg.Height)
	assert.Greater(t, longest, 300, "the page should use the allowed size, got %dx%d", cfg.Width, cfg.Height)
}
