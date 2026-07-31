package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// envReadPatterns cover direct environment reads and the typed helpers that
// accept an environment key. Dynamically-composed keys (for example,
// os.Getenv(prefix+"MAX_RETRIES")) remain registered explicitly.
var envReadPatterns = []*regexp.Regexp{
	regexp.MustCompile(`os\.(?:Getenv|LookupEnv)\s*\(\s*"([A-Z0-9_]+)"\s*\)`),
	regexp.MustCompile(`(?:parseOptionalBoolEnv|parseBoolEnv)\s*\(\s*"([A-Z0-9_]+)"\s*\)`),
}

func findLiteralEnvReads(source string) []string {
	var reads []string
	for _, pattern := range envReadPatterns {
		for _, match := range pattern.FindAllStringSubmatch(source, -1) {
			reads = append(reads, match[1])
		}
	}
	return reads
}

// TestEnvRegistryCoversAllReads is the drift guard: every environment variable
// the code reads with a literal key must be documented in envRegistry. Adding a
// new os.Getenv("NEW_VAR") without a registry entry fails this test, so the
// /api/config diagnostics view can never silently miss a setting.
func TestEnvRegistryCoversAllReads(t *testing.T) {
	registered := make(map[string]bool, len(envRegistry))
	for _, e := range envRegistry {
		registered[e.Name] = true
	}

	readInCode := map[string][]string{} // var -> files that read it

	roots := []string{".", "ocr", "sanitize", "internal"}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "node_modules" || d.Name() == "web-app" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, name := range findLiteralEnvReads(string(data)) {
				readInCode[name] = append(readInCode[name], path)
			}
			return nil
		})
		require.NoError(t, err)
	}

	require.NotEmpty(t, readInCode, "expected to find env reads in the source tree")

	var missing []string
	for name, files := range readInCode {
		if !registered[name] {
			missing = append(missing, name+" (read in "+strings.Join(files, ", ")+")")
		}
	}
	assert.Empty(t, missing, "these env vars are read in code but missing from envRegistry — add them so the /config view documents them:\n%s", strings.Join(missing, "\n"))
}

func TestFindLiteralEnvReadsIncludesTypedHelpers(t *testing.T) {
	source := `
		os.Getenv("DIRECT_VALUE")
		os.LookupEnv( "LOOKED_UP_VALUE" )
		parseBoolEnv("BOOLEAN_VALUE")
		parseOptionalBoolEnv("OPTIONAL_BOOLEAN_VALUE")
	`

	assert.ElementsMatch(t, []string{
		"DIRECT_VALUE",
		"LOOKED_UP_VALUE",
		"BOOLEAN_VALUE",
		"OPTIONAL_BOOLEAN_VALUE",
	}, findLiteralEnvReads(source))
}

// TestEnvRegistryWellFormed checks the registry's own invariants.
func TestEnvRegistryWellFormed(t *testing.T) {
	validCategory := make(map[string]bool, len(envCategoryOrder))
	for _, c := range envCategoryOrder {
		validCategory[c] = true
	}
	seen := make(map[string]bool, len(envRegistry))
	for _, e := range envRegistry {
		assert.NotEmpty(t, e.Name, "registry entry with empty name")
		assert.Falsef(t, seen[e.Name], "duplicate registry entry: %s", e.Name)
		seen[e.Name] = true
		assert.Truef(t, validCategory[e.Category], "%s has unknown category %q", e.Name, e.Category)
		assert.NotEmptyf(t, e.Description, "%s has no description", e.Name)
	}
}
