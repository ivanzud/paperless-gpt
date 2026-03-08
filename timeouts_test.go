package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestGetPaperlessHTTPTimeout(t *testing.T) {
	t.Setenv("PAPERLESS_HTTP_TIMEOUT", "")
	assert.Equal(t, 5*time.Minute, getPaperlessHTTPTimeout())

	t.Setenv("PAPERLESS_HTTP_TIMEOUT", "45s")
	assert.Equal(t, 45*time.Second, getPaperlessHTTPTimeout())

	t.Setenv("PAPERLESS_HTTP_TIMEOUT", "0")
	assert.Equal(t, time.Duration(0), getPaperlessHTTPTimeout())

	t.Setenv("PAPERLESS_HTTP_TIMEOUT", "-1s")
	assert.Equal(t, 5*time.Minute, getPaperlessHTTPTimeout())

	t.Setenv("PAPERLESS_HTTP_TIMEOUT", "not-a-duration")
	assert.Equal(t, 5*time.Minute, getPaperlessHTTPTimeout())
}

func TestNewPaperlessClient_UsesConfiguredTimeout(t *testing.T) {
	t.Setenv("PAPERLESS_HTTP_TIMEOUT", "30s")

	client := NewPaperlessClient("http://example.com", "token")

	assert.Equal(t, 30*time.Second, client.HTTPClient.Timeout)
}

func TestGetBackgroundDocumentTimeout(t *testing.T) {
	t.Setenv("BACKGROUND_DOCUMENT_TIMEOUT", "")
	assert.Equal(t, 15*time.Minute, getBackgroundDocumentTimeout())

	t.Setenv("BACKGROUND_DOCUMENT_TIMEOUT", "120")
	assert.Equal(t, 120*time.Second, getBackgroundDocumentTimeout())

	t.Setenv("BACKGROUND_DOCUMENT_TIMEOUT", "2m30s")
	assert.Equal(t, 150*time.Second, getBackgroundDocumentTimeout())

	t.Setenv("BACKGROUND_DOCUMENT_TIMEOUT", "0")
	assert.Equal(t, time.Duration(0), getBackgroundDocumentTimeout())

	t.Setenv("BACKGROUND_DOCUMENT_TIMEOUT", "-30")
	assert.Equal(t, time.Duration(0), getBackgroundDocumentTimeout())

	t.Setenv("BACKGROUND_DOCUMENT_TIMEOUT", "-30s")
	assert.Equal(t, 15*time.Minute, getBackgroundDocumentTimeout())

	t.Setenv("BACKGROUND_DOCUMENT_TIMEOUT", "bad-value")
	assert.Equal(t, 15*time.Minute, getBackgroundDocumentTimeout())
}

func TestWithBackgroundDocumentTimeout(t *testing.T) {
	parent := context.Background()

	t.Setenv("BACKGROUND_DOCUMENT_TIMEOUT", "50ms")
	ctx, cancel := withBackgroundDocumentTimeout(parent)
	defer cancel()
	deadline, ok := ctx.Deadline()
	assert.True(t, ok)
	assert.WithinDuration(t, time.Now().Add(50*time.Millisecond), deadline, 100*time.Millisecond)

	t.Setenv("BACKGROUND_DOCUMENT_TIMEOUT", "0")
	ctx, cancel = withBackgroundDocumentTimeout(parent)
	defer cancel()
	_, ok = ctx.Deadline()
	assert.False(t, ok)
	assert.Equal(t, parent, ctx)
}
