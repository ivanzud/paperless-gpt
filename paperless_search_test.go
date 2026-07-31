package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPathWithinBase(t *testing.T) {
	base := t.TempDir()

	path, err := pathWithinBase(base, "document-42", "preview-page001.jpg")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(base, "document-42", "preview-page001.jpg"), path)

	_, err = pathWithinBase(base, "..", "outside")
	require.Error(t, err)
}
