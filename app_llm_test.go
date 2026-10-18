package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/textsplitter"
	"gorm.io/gorm"
)

// Mock LLM for testing
type mockLLM struct {
	lastPrompt string
	Response   string
	Error      error
}

func (m *mockLLM) CreateEmbedding(_ context.Context, texts []string) ([][]float32, error) {
	return nil, nil // Not used in these tests
}

func (m *mockLLM) Call(ctx context.Context, prompt string, options ...llms.CallOption) (string, error) {
	m.lastPrompt = prompt
	resp, err := m.GenerateContent(ctx, []llms.MessageContent{
		{Role: llms.ChatMessageTypeHuman, Parts: []llms.ContentPart{llms.TextContent{Text: prompt}}}},
		options...)
	if err != nil {
		return "", err
	}
	return resp.Choices[0].Content, nil
}

func (m *mockLLM) GenerateContent(ctx context.Context, messages []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
	if len(messages) > 0 && len(messages[0].Parts) > 0 {
		if textContent, ok := messages[0].Parts[0].(llms.TextContent); ok {
			m.lastPrompt = textContent.Text
		}
	}

	if m.Error != nil {
		return nil, m.Error
	}

	content := "test response"
	if m.Response != "" {
		content = m.Response
	}

	return &llms.ContentResponse{
		Choices: []*llms.ContentChoice{
			{
				Content: content,
			},
		},
	}, nil
}

// Mock templates for testing
const (
	testTitleTemplate = `
Language: {{.Language}}
Title: {{.Title}}
Content: {{.Content}}
`
	testTagTemplate = `
Language: {{.Language}}
Tags: {{.AvailableTags}}
CreateNewTags: {{.CreateNewTags}}
Content: {{.Content}}
`
	testCorrespondentTemplate = `
Language: {{.Language}}
Content: {{.Content}}
`
	testCreatedDateContentTemplate = `
Language: {{.Language}}
Content: {{.Content}}
`
)

func TestPromptTokenLimits(t *testing.T) {
	originalTokenLimit := tokenLimit
	defer func() { tokenLimit = originalTokenLimit }()

	testLogger := logrus.WithField("test", "test")

	// Initialize test templates
	var err error
	titleTemplate, err = template.New("title").Parse(testTitleTemplate)
	require.NoError(t, err)
	tagTemplate, err = template.New("tag").Parse(testTagTemplate)
	require.NoError(t, err)
	correspondentTemplate, err = template.New("correspondent").Parse(testCorrespondentTemplate)
	require.NoError(t, err)
	createdDateTemplate, err = template.New("created_date").Parse(testCreatedDateContentTemplate)
	require.NoError(t, err)

	// Save current env and restore after test
	originalLimit := os.Getenv("TOKEN_LIMIT")
	defer os.Setenv("TOKEN_LIMIT", originalLimit)

	// Create a test app with mock LLM
	mockLLM := &mockLLM{}
	app := &App{
		LLM: mockLLM,
	}

	// Set up test template
	testTemplate := template.Must(template.New("test").Parse(`
Language: {{.Language}}
Content: {{.Content}}
`))

	tests := []struct {
		name       string
		tokenLimit int
		content    string
	}{
		{
			name:       "no limit",
			tokenLimit: 0,
			content:    "This is the original content that should not be truncated.",
		},
		{
			name:       "content within limit",
			tokenLimit: 100,
			content:    "Short content",
		},
		{
			name:       "content exceeds limit",
			tokenLimit: 50,
			content:    "This is a much longer content that should definitely be truncated to fit within token limits",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Set token limit for this test
			os.Setenv("TOKEN_LIMIT", fmt.Sprintf("%d", tc.tokenLimit))
			resetTokenLimit()

			// Prepare test data
			data := map[string]interface{}{
				"Language": "English",
			}

			// Calculate available tokens
			availableTokens, err := getAvailableTokensForContent(testTemplate, data)
			require.NoError(t, err)

			// Truncate content if needed
			truncatedContent, err := truncateContentByTokens(tc.content, availableTokens)
			require.NoError(t, err)

			// Test with the app's LLM
			ctx := context.Background()
			_, err = app.getSuggestedTitle(ctx, 0, truncatedContent, "Test Title", testLogger, nil)
			require.NoError(t, err)

			// Verify truncation
			if tc.tokenLimit > 0 {
				// Count tokens in final prompt received by LLM
				splitter := textsplitter.NewTokenSplitter()
				tokens, err := splitter.SplitText(mockLLM.lastPrompt)
				require.NoError(t, err)

				// Verify prompt is within limits
				assert.LessOrEqual(t, len(tokens), tc.tokenLimit,
					"Final prompt should be within token limit")

				if len(tc.content) > len(truncatedContent) {
					// Content was truncated
					t.Logf("Content truncated from %d to %d characters",
						len(tc.content), len(truncatedContent))
				}
			} else {
				// No limit set, content should be unchanged
				assert.Contains(t, mockLLM.lastPrompt, tc.content,
					"Original content should be in prompt when no limit is set")
			}
		})
	}
}

func TestTokenLimitInCorrespondentGeneration(t *testing.T) {
	originalTokenLimit := tokenLimit
	defer func() { tokenLimit = originalTokenLimit }()

	// Save current env and restore after test
	originalLimit := os.Getenv("TOKEN_LIMIT")
	defer os.Setenv("TOKEN_LIMIT", originalLimit)

	// Create a test app with mock LLM
	mockLLM := &mockLLM{}
	app := &App{
		LLM: mockLLM,
	}

	// Test content that would exceed reasonable token limits
	longContent := "This is a very long content that would normally exceed token limits. " +
		"It contains multiple sentences and should be truncated appropriately " +
		"based on the token limit that we set."

	// Set a small token limit
	os.Setenv("TOKEN_LIMIT", "50")
	resetTokenLimit()

	// Call getSuggestedCorrespondent
	ctx := context.Background()
	availableCorrespondents := []string{"Test Corp", "Example Inc"}
	correspondentBlackList := []string{"Blocked Corp"}

	_, err := app.getSuggestedCorrespondent(ctx, longContent, "Test Title", availableCorrespondents, correspondentBlackList, nil)
	require.NoError(t, err)

	// Verify the final prompt size
	splitter := textsplitter.NewTokenSplitter()
	tokens, err := splitter.SplitText(mockLLM.lastPrompt)
	require.NoError(t, err)

	// Final prompt should be within token limit
	assert.LessOrEqual(t, len(tokens), 50, "Final prompt should be within token limit")
}

func TestCorrespondentPromptLimitPreventsTemplateOverflow(t *testing.T) {
	originalTokenLimit := tokenLimit
	originalCorrespondentPromptLimit := correspondentPromptLimit
	originalCorrespondentTemplate := correspondentTemplate
	defer func() {
		tokenLimit = originalTokenLimit
		correspondentPromptLimit = originalCorrespondentPromptLimit
		correspondentTemplate = originalCorrespondentTemplate
	}()

	correspondentTemplate = template.Must(template.New("correspondent-limit").Parse(`
Correspondents: {{.AvailableCorrespondents}}
Blacklist: {{.BlackList}}
Title: {{.Title}}
Content: {{.Content}}
Language: {{.Language}}
`))
	tokenLimit = 1500
	correspondentPromptLimit = 25

	correspondents := make([]string, 252)
	for i := range correspondents {
		correspondents[i] = fmt.Sprintf(
			"Example International Correspondent Organization Number %03d Billing Services Incorporated",
			i,
		)
	}
	target := correspondents[len(correspondents)-1]
	templateData := map[string]interface{}{
		"Language":                "English",
		"AvailableCorrespondents": correspondents,
		"BlackList":               []string{},
		"Title":                   target + " invoice",
	}

	_, err := getAvailableTokensForContent(correspondentTemplate, templateData)
	require.ErrorContains(t, err, "prompt template exceeds token limit")

	mockLLM := &mockLLM{Response: target}
	app := &App{LLM: mockLLM}
	suggestion, err := app.generateSingleDocumentSuggestion(
		context.Background(),
		GenerateSuggestionsRequest{GenerateCorrespondents: true},
		Document{ID: 42, Title: target + " invoice", Content: "Monthly account statement"},
		suggestionGenerationContext{availableCorrespondentNames: correspondents},
		logrus.WithField("test", "correspondent-prompt-limit"),
	)
	require.NoError(t, err)
	assert.Equal(t, target, suggestion.SuggestedCorrespondent)
	assert.Contains(t, mockLLM.lastPrompt, target)
	correspondentLine := strings.SplitN(strings.TrimSpace(mockLLM.lastPrompt), "\n", 2)[0]
	assert.Equal(t, correspondentPromptLimit, strings.Count(correspondentLine, "Billing Services Incorporated"))

	promptTokens, err := getTokenCount(mockLLM.lastPrompt)
	require.NoError(t, err)
	assert.LessOrEqual(t, promptTokens, tokenLimit)
}

func TestTokenLimitInTagGeneration(t *testing.T) {
	originalTokenLimit := tokenLimit
	defer func() { tokenLimit = originalTokenLimit }()

	testLogger := logrus.WithField("test", "test")

	// Save current env and restore after test
	originalLimit := os.Getenv("TOKEN_LIMIT")
	defer os.Setenv("TOKEN_LIMIT", originalLimit)

	// Create a test app with mock LLM
	mockLLM := &mockLLM{}
	app := &App{
		LLM: mockLLM,
	}

	// Test content that would exceed reasonable token limits
	longContent := "This is a very long content that would normally exceed token limits. " +
		"It contains multiple sentences and should be truncated appropriately."

	// Set a small token limit
	os.Setenv("TOKEN_LIMIT", "50")
	resetTokenLimit()

	// Call getSuggestedTags
	ctx := context.Background()
	availableTags := []string{"test", "example"}
	originalTags := []string{"original"}

	_, err := app.getSuggestedTags(ctx, longContent, "Test Title", availableTags, originalTags, testLogger, nil)
	require.NoError(t, err)

	// Verify the final prompt size
	splitter := textsplitter.NewTokenSplitter()
	tokens, err := splitter.SplitText(mockLLM.lastPrompt)
	require.NoError(t, err)

	// Final prompt should be within token limit
	assert.LessOrEqual(t, len(tokens), 50, "Final prompt should be within token limit")
}

func TestCreateNewTagsFiltering(t *testing.T) {
	testLogger := logrus.WithField("test", "create-new-tags")

	// Initialize tag template for this test
	var err error
	tagTemplate, err = template.New("tag").Parse(testTagTemplate)
	require.NoError(t, err)

	// Save and restore both new-tag configuration sources.
	originalCreateNewTags := createNewTags
	settingsMutex.RLock()
	originalSettings := settings
	settingsMutex.RUnlock()
	defer func() {
		createNewTags = originalCreateNewTags
		settingsMutex.Lock()
		settings = originalSettings
		settingsMutex.Unlock()
	}()
	setTagsAutoCreate := func(enabled bool) {
		settingsMutex.Lock()
		settings.TagsAutoCreate = enabled
		settingsMutex.Unlock()
	}

	ctx := context.Background()
	availableTags := []string{"invoice", "receipt", "tax"}
	originalTags := []string{}

	t.Run("default filters out new tags", func(t *testing.T) {
		createNewTags = false
		setTagsAutoCreate(false)
		mockLLM := &mockLLM{Response: "invoice, new-tag, receipt"}
		app := &App{LLM: mockLLM}

		tags, err := app.getSuggestedTags(ctx, "Some document content", "Test Invoice", availableTags, originalTags, testLogger, nil)
		require.NoError(t, err)

		assert.Contains(t, tags, "invoice")
		assert.Contains(t, tags, "receipt")
		assert.NotContains(t, tags, "new-tag")
		assert.Contains(t, mockLLM.lastPrompt, "CreateNewTags: false")
	})

	t.Run("env flag allows new tags", func(t *testing.T) {
		createNewTags = true
		setTagsAutoCreate(false)
		mockLLM := &mockLLM{Response: "invoice, new-tag, receipt"}
		app := &App{LLM: mockLLM}

		tags, err := app.getSuggestedTags(ctx, "Some document content", "Test Invoice", availableTags, originalTags, testLogger, nil)
		require.NoError(t, err)

		assert.Contains(t, tags, "invoice")
		assert.Contains(t, tags, "receipt")
		assert.Contains(t, tags, "new-tag")
		assert.Contains(t, mockLLM.lastPrompt, "CreateNewTags: true")
	})

	t.Run("settings flag allows new tags", func(t *testing.T) {
		createNewTags = false
		setTagsAutoCreate(true)
		mockLLM := &mockLLM{Response: "Invoice, NEW-TAG"}
		app := &App{LLM: mockLLM}

		tags, err := app.getSuggestedTags(ctx, "Some document content", "Test Invoice", availableTags, originalTags, testLogger, nil)
		require.NoError(t, err)

		assert.Contains(t, tags, "invoice")
		assert.Contains(t, tags, "NEW-TAG")
		assert.Contains(t, mockLLM.lastPrompt, "CreateNewTags: true")
	})

	t.Run("new tags preserve existing tag casing", func(t *testing.T) {
		createNewTags = true
		setTagsAutoCreate(false)
		mockLLM := &mockLLM{Response: "Invoice, NEW-TAG"}
		app := &App{LLM: mockLLM}

		tags, err := app.getSuggestedTags(ctx, "Some document content", "Test Invoice", availableTags, originalTags, testLogger, nil)
		require.NoError(t, err)

		// Existing tag should use the available tag's casing
		assert.Contains(t, tags, "invoice")
		// New tag keeps its original casing
		assert.Contains(t, tags, "NEW-TAG")
	})

	t.Run("new tags filter out empty tags", func(t *testing.T) {
		createNewTags = true
		setTagsAutoCreate(false)
		mockLLM := &mockLLM{Response: "invoice, , receipt"}
		app := &App{LLM: mockLLM}

		tags, err := app.getSuggestedTags(ctx, "Some document content", "Test Invoice", availableTags, originalTags, testLogger, nil)
		require.NoError(t, err)

		for _, tag := range tags {
			assert.NotEmpty(t, tag)
		}
	})
}

func TestGetSuggestedTagsFiltersProcessingTags(t *testing.T) {
	previousTemplate := tagTemplate
	tagTemplate = template.Must(template.New("tag").Parse(testTagTemplate + "\nOriginal: {{.OriginalTags}}\n"))
	t.Cleanup(func() {
		tagTemplate = previousTemplate
	})

	originalManualTag := manualTag
	originalAutoTag := autoTag
	originalAutoOCRTag := autoOcrTag
	originalPDFOCRCompleteTag := pdfOCRCompleteTag
	originalFailTag := failTag
	originalAutoTagComplete := autoTagComplete
	t.Cleanup(func() {
		manualTag = originalManualTag
		autoTag = originalAutoTag
		autoOcrTag = originalAutoOCRTag
		pdfOCRCompleteTag = originalPDFOCRCompleteTag
		failTag = originalFailTag
		autoTagComplete = originalAutoTagComplete
	})

	manualTag = "paperless-gpt"
	autoTag = "paperless-gpt-auto"
	autoOcrTag = "paperless-gpt-ocr-auto"
	pdfOCRCompleteTag = "paperless-gpt-ocr-complete"
	failTag = "paperless-gpt-failed"
	autoTagComplete = "paperless-gpt-auto-complete"

	originalCreateNewTags := createNewTags
	settingsMutex.RLock()
	originalSettings := settings
	settingsMutex.RUnlock()
	t.Cleanup(func() {
		createNewTags = originalCreateNewTags
		settingsMutex.Lock()
		settings = originalSettings
		settingsMutex.Unlock()
	})
	availableTags := []string{
		strings.ToUpper(manualTag),
		autoTag,
		strings.ToUpper(autoOcrTag),
		pdfOCRCompleteTag,
		strings.ToUpper(failTag),
		autoTagComplete,
		"finance",
		"archive",
	}
	originalTags := []string{
		"ARCHIVE",
		strings.ToUpper(manualTag),
		strings.ToUpper(autoTag),
		strings.ToUpper(autoOcrTag),
		strings.ToUpper(pdfOCRCompleteTag),
		strings.ToUpper(failTag),
		strings.ToUpper(autoTagComplete),
	}

	for _, testCase := range []struct {
		name         string
		allowNewTags bool
		expected     []string
	}{
		{name: "existing tags only", expected: []string{"archive", "finance"}},
		{name: "new tags allowed", allowNewTags: true, expected: []string{"archive", "finance", "new-tag"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			createNewTags = testCase.allowNewTags
			settingsMutex.Lock()
			settings.TagsAutoCreate = false
			settingsMutex.Unlock()

			mockLLM := &mockLLM{Response: strings.Join([]string{
				"finance",
				"new-tag",
				strings.ToUpper(manualTag),
				strings.ToUpper(autoTag),
				strings.ToUpper(autoOcrTag),
				strings.ToUpper(pdfOCRCompleteTag),
				strings.ToUpper(failTag),
				strings.ToUpper(autoTagComplete),
			}, ", ")}
			app := &App{LLM: mockLLM}
			tags, err := app.getSuggestedTags(
				context.Background(),
				"sample content",
				"Test Title",
				availableTags,
				originalTags,
				logrus.WithField("test", "filter-processing-tags"),
				nil,
			)
			require.NoError(t, err)
			assert.ElementsMatch(t, testCase.expected, tags)

			lowerPrompt := strings.ToLower(mockLLM.lastPrompt)
			for _, protectedTag := range protectedWorkflowTags() {
				assert.NotContains(t, lowerPrompt, strings.ToLower(protectedTag))
			}
			assert.Contains(t, lowerPrompt, "tags: [finance archive]")
			assert.Contains(t, lowerPrompt, "original: [archive]")
		})
	}
}

func TestGenerateSingleDocumentSuggestionAutoTagComplete(t *testing.T) {
	for _, testCase := range []struct {
		name             string
		autoTagComplete  string
		isAutoProcessing bool
		expectedAddTags  []string
	}{
		{
			name:             "enabled for auto processing",
			autoTagComplete:  "paperless-gpt-auto-complete",
			isAutoProcessing: true,
			expectedAddTags:  []string{"paperless-gpt-auto-complete"},
		},
		{
			name:             "disabled when empty",
			isAutoProcessing: true,
		},
		{
			name:            "not added during manual review",
			autoTagComplete: "paperless-gpt-auto-complete",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			app := &App{autoTagComplete: testCase.autoTagComplete}
			suggestion, err := app.generateSingleDocumentSuggestion(
				context.Background(),
				GenerateSuggestionsRequest{IsAutoProcessing: testCase.isAutoProcessing},
				Document{ID: 42, Title: "Invoice"},
				suggestionGenerationContext{},
				logrus.WithField("test", "auto-tag-complete"),
			)
			require.NoError(t, err)
			assert.Equal(t, testCase.expectedAddTags, suggestion.AddTags)
		})
	}
}

func TestTokenLimitInTitleGeneration(t *testing.T) {
	originalTokenLimit := tokenLimit
	defer func() { tokenLimit = originalTokenLimit }()
	previousTemplate := titleTemplate
	titleTemplate = template.Must(template.New("title").Parse(testTitleTemplate))
	defer func() { titleTemplate = previousTemplate }()

	testLogger := logrus.WithField("test", "test")

	// Save current env and restore after test
	originalLimit := os.Getenv("TOKEN_LIMIT")
	defer os.Setenv("TOKEN_LIMIT", originalLimit)

	// Create a test app with mock LLM
	mockLLM := &mockLLM{}
	app := &App{
		LLM: mockLLM,
	}

	// Test content that would exceed reasonable token limits
	longContent := "This is a very long content that would normally exceed token limits. " +
		"It contains multiple sentences and should be truncated appropriately."

	// Set a small token limit
	os.Setenv("TOKEN_LIMIT", "50")
	resetTokenLimit()

	// Call getSuggestedTitle
	ctx := context.Background()

	_, err := app.getSuggestedTitle(ctx, 0, longContent, "Original Title", testLogger, nil)
	require.NoError(t, err)

	// Verify the final prompt size
	splitter := textsplitter.NewTokenSplitter()
	tokens, err := splitter.SplitText(mockLLM.lastPrompt)
	require.NoError(t, err)

	// Final prompt should be within token limit
	assert.LessOrEqual(t, len(tokens), 50, "Final prompt should be within token limit")
}

func TestGetSuggestedCreatedDateNormalizesYearFirstDate(t *testing.T) {
	previous := createdDateTemplate
	createdDateTemplate = template.Must(template.New("created_date").Parse("{{.Content}}"))
	t.Cleanup(func() { createdDateTemplate = previous })

	app := &App{LLM: &mockLLM{Response: "2023.01.01"}}
	got, err := app.getSuggestedCreatedDate(context.Background(), "invoice dated 2023.01.01", "", "", logrus.WithField("test", t.Name()), nil)
	require.NoError(t, err)
	assert.Equal(t, "2023-01-01", got)
}

func TestTokenLimitInCreatedDateGeneration(t *testing.T) {
	originalTokenLimit := tokenLimit
	defer func() { tokenLimit = originalTokenLimit }()
	previousTemplate := createdDateTemplate
	createdDateTemplate = template.Must(template.New("created_date").Parse(testCreatedDateContentTemplate))
	defer func() { createdDateTemplate = previousTemplate }()

	testLogger := logrus.WithField("test", "test")

	// Save current env and restore after test
	originalLimit := os.Getenv("TOKEN_LIMIT")
	defer os.Setenv("TOKEN_LIMIT", originalLimit)

	// Create a test app with mock LLM
	mockLLM := &mockLLM{}
	app := &App{
		LLM: mockLLM,
	}

	// Test content that would exceed reasonable token limits
	longContent := "This is a very long content that would normally exceed token limits. " +
		"It contains multiple sentences and should be truncated appropriately."

	// Set a small token limit
	os.Setenv("TOKEN_LIMIT", "50")
	resetTokenLimit()

	// Call getSuggestedCreatedDate
	ctx := context.Background()

	_, err := app.getSuggestedCreatedDate(ctx, longContent, "Example Title", "example.pdf", testLogger, nil)
	require.NoError(t, err)

	// Verify the final prompt size
	splitter := textsplitter.NewTokenSplitter()
	tokens, err := splitter.SplitText(mockLLM.lastPrompt)
	require.NoError(t, err)

	// Final prompt should be within token limit
	assert.LessOrEqual(t, len(tokens), 50, "Final prompt should be within token limit")
}

func TestCreatedDatePromptIncludesDocumentContext(t *testing.T) {
	previousTemplate := createdDateTemplate
	createdDateTemplate = template.Must(template.New("created_date").Parse(`
Title: {{.Title}}
OriginalFileName: {{.OriginalFileName}}
Content: {{.Content}}
`))
	t.Cleanup(func() {
		createdDateTemplate = previousTemplate
	})

	mockLLM := &mockLLM{Response: "2026-06-17"}
	app := &App{LLM: mockLLM}
	_, err := app.getSuggestedCreatedDate(
		context.Background(),
		"invoice content",
		"June Invoice",
		"invoice-june.pdf",
		logrus.WithField("test", "created-date-context"),
		nil,
	)
	require.NoError(t, err)
	assert.Contains(t, mockLLM.lastPrompt, "Title: June Invoice")
	assert.Contains(t, mockLLM.lastPrompt, "OriginalFileName: invoice-june.pdf")
	assert.NotContains(t, mockLLM.lastPrompt, "<no value>")
}

func TestPrepareSuggestionGenerationContextFetchesOnlyRequestedMetadata(t *testing.T) {
	app := &App{
		Client: &mockPaperlessClient{
			TagsError:           fmt.Errorf("tags should not be fetched"),
			CorrespondentsError: fmt.Errorf("correspondents should not be fetched"),
			DocumentTypesError:  fmt.Errorf("document types should not be fetched"),
		},
	}

	_, err := app.prepareSuggestionGenerationContext(context.Background(), GenerateSuggestionsRequest{
		GenerateTitles:      true,
		GenerateCreatedDate: true,
	})
	require.NoError(t, err)

	client, ok := app.Client.(*mockPaperlessClient)
	require.True(t, ok, "Client should be *mockPaperlessClient")
	assert.Zero(t, client.GetAllTagsCalls)
	assert.Zero(t, client.GetAllCorrespondentsCalls)
	assert.Zero(t, client.GetAllDocumentTypesCalls)

	app = &App{Client: &mockPaperlessClient{}}
	contextData, err := app.prepareSuggestionGenerationContext(context.Background(), GenerateSuggestionsRequest{
		GenerateTags:           true,
		GenerateCorrespondents: true,
		GenerateDocumentTypes:  true,
	})
	require.NoError(t, err)

	client, ok = app.Client.(*mockPaperlessClient)
	require.True(t, ok, "Client should be *mockPaperlessClient")
	assert.Equal(t, 1, client.GetAllTagsCalls)
	assert.Equal(t, 1, client.GetAllCorrespondentsCalls)
	assert.Equal(t, 1, client.GetAllDocumentTypesCalls)
	assert.Equal(t, []string{"invoice"}, contextData.availableTagNames)
	assert.Equal(t, []string{"Vendor"}, contextData.availableCorrespondentNames)
	assert.Equal(t, []string{"Invoice"}, contextData.availableDocumentTypeNames)
}

// mockPaperlessClient is a mock implementation of the ClientInterface for testing.
type mockPaperlessClient struct {
	CustomFields              []CustomField
	SimilarDocuments          []Document
	Error                     error
	SimilarDocumentsError     error
	TagsError                 error
	CorrespondentsError       error
	DocumentTypesError        error
	GetAllTagsCalls           int
	GetAllCorrespondentsCalls int
	GetAllDocumentTypesCalls  int
	GetSimilarDocumentsCalls  int
	LastSimilarDocumentID     int
	LastSimilarDocumentCount  int
	// ReferenceMatches maps a reference to the document ids
	// FindDocumentIDsByReference returns for it.
	ReferenceMatches map[string][]int
	ReferenceError   error
	ReferenceLookups []string
}

func (m *mockPaperlessClient) GetCustomFields(ctx context.Context) ([]CustomField, error) {
	if m.Error != nil {
		return nil, m.Error
	}
	return m.CustomFields, nil
}

// Implement other methods of the interface with empty bodies as they are not needed for this test.
func (m *mockPaperlessClient) GetDocumentsByTag(ctx context.Context, tag string, pageSize int) ([]Document, error) {
	return nil, nil
}
func (m *mockPaperlessClient) GetDocumentCountByTag(ctx context.Context, tag string) (int, error) {
	return 0, nil
}
func (m *mockPaperlessClient) UpdateDocuments(ctx context.Context, documents []DocumentSuggestion, db *gorm.DB, isUndo bool) error {
	return nil
}
func (m *mockPaperlessClient) GetDocument(ctx context.Context, documentID int) (Document, error) {
	return Document{}, nil
}
func (m *mockPaperlessClient) GetDocumentThumbnail(ctx context.Context, documentID int) ([]byte, string, error) {
	return nil, "", nil
}
func (m *mockPaperlessClient) SearchDocuments(ctx context.Context, query string, pageSize int) ([]Document, error) {
	return nil, nil
}
func (m *mockPaperlessClient) FindDocumentIDsByReference(ctx context.Context, reference string, excludeID, maxMatches int) ([]int, bool, error) {
	m.ReferenceLookups = append(m.ReferenceLookups, reference)
	if m.ReferenceError != nil {
		return nil, false, m.ReferenceError
	}
	var ids []int
	for _, id := range m.ReferenceMatches[reference] {
		if id != excludeID {
			ids = append(ids, id)
		}
	}
	if len(ids) > maxMatches {
		return nil, true, nil
	}
	return ids, false, nil
}
func (m *mockPaperlessClient) GetDocumentPageImage(ctx context.Context, documentID int, pageIndex int) ([]byte, error) {
	return nil, nil
}
func (m *mockPaperlessClient) GetAllTags(ctx context.Context) (map[string]int, error) {
	m.GetAllTagsCalls++
	if m.TagsError != nil {
		return nil, m.TagsError
	}
	return map[string]int{"invoice": 1, manualTag: 2}, nil
}
func (m *mockPaperlessClient) GetAllCorrespondents(ctx context.Context) (map[string]int, error) {
	m.GetAllCorrespondentsCalls++
	if m.CorrespondentsError != nil {
		return nil, m.CorrespondentsError
	}
	return map[string]int{"Vendor": 1}, nil
}
func (m *mockPaperlessClient) GetAllDocumentTypes(ctx context.Context) ([]DocumentType, error) {
	m.GetAllDocumentTypesCalls++
	if m.DocumentTypesError != nil {
		return nil, m.DocumentTypesError
	}
	return []DocumentType{{ID: 1, Name: "Invoice"}}, nil
}
func (m *mockPaperlessClient) CreateTag(ctx context.Context, tagName string, objPerms *ObjPermissions) (int, error) {
	return 0, nil
}
func (m *mockPaperlessClient) CreateDocumentNote(ctx context.Context, documentID int, note string) (DocumentNote, error) {
	return DocumentNote{}, nil
}
func (m *mockPaperlessClient) DeleteDocumentNote(ctx context.Context, documentID int, noteID int) error {
	return nil
}
func (m *mockPaperlessClient) DownloadDocumentAsImages(ctx context.Context, documentID int, pageLimit int) ([]string, int, error) {
	return nil, 0, nil
}
func (m *mockPaperlessClient) DownloadDocumentAsPDF(ctx context.Context, documentID int, limitPages int, split bool) ([]string, []byte, int, error) {
	return nil, nil, 0, nil
}
func (m *mockPaperlessClient) UploadDocument(ctx context.Context, data []byte, filename string, metadata map[string]interface{}) (string, error) {
	return "", nil
}
func (m *mockPaperlessClient) UploadDocumentVersion(ctx context.Context, documentID int, data []byte, filename string, versionLabel string) (string, error) {
	return "", nil
}
func (m *mockPaperlessClient) GetTaskStatus(ctx context.Context, taskID string) (map[string]interface{}, error) {
	return nil, nil
}
func (m *mockPaperlessClient) DeleteDocument(ctx context.Context, documentID int) error { return nil }
func (m *mockPaperlessClient) GetUiSettings(ctx context.Context) (*UiSettings, error) {
	return &UiSettings{}, nil
}
func (m *mockPaperlessClient) GetPermissions(ctx context.Context, doc *Document) (*ObjPermissions, error) {
	return &ObjPermissions{}, nil
}
func (m *mockPaperlessClient) GetSimilarDocuments(ctx context.Context, documentID int, count int) ([]Document, error) {
	m.GetSimilarDocumentsCalls++
	m.LastSimilarDocumentID = documentID
	m.LastSimilarDocumentCount = count
	if m.SimilarDocumentsError != nil {
		return nil, m.SimilarDocumentsError
	}
	return m.SimilarDocuments, nil
}

func TestGetSuggestedCustomFields(t *testing.T) {
	// 1. Setup
	mockedLLMResponse := `
	[
	  {
	    "field": "Invoice Number",
	    "value": "INV-12345"
	  },
	  {
	    "field": "Due Date",
	    "value": "2025-12-31"
	  },
	  {
		"field": "NonExistentField",
		"value": "Some Value"
	  }
	]
	`

	mockClient := &mockPaperlessClient{
		CustomFields: []CustomField{
			{ID: 1, Name: "Invoice Number", DataType: "string"},
			{ID: 2, Name: "Due Date", DataType: "date"},
			{ID: 3, Name: "Amount", DataType: "float"},
		},
	}

	app := &App{
		LLM:    &mockLLM{Response: mockedLLMResponse},
		Client: mockClient,
	}

	// Create a dummy template file as loadTemplates() will be called
	usePromptsSandbox(t, map[string]string{"custom_field_prompt.tmpl": "test"})

	err := loadTemplates()
	require.NoError(t, err)

	// 2. Define Inputs
	doc := Document{
		Content: "The invoice number is INV-12345 and the due date is 2025-12-31.",
	}
	selectedFieldIDs := []int{1, 2} // User has selected "Invoice Number" and "Due Date"

	// 3. Execute
	testLogger := logrus.WithField("test", "TestGetSuggestedCustomFields")
	suggestions, err := app.getSuggestedCustomFields(context.Background(), doc, selectedFieldIDs, testLogger, nil)

	// 4. Assert
	require.NoError(t, err)
	require.NotNil(t, suggestions)
	assert.Len(t, suggestions, 2, "Should return 2 suggestions, ignoring the non-existent one")

	// Check Invoice Number
	invoiceField, ok := findFieldByID(suggestions, 1)
	assert.True(t, ok, "Invoice Number (ID 1) should be in the suggestions")
	assert.Equal(t, "INV-12345", invoiceField.Value)

	// Check Due Date
	dueDateField, ok := findFieldByID(suggestions, 2)
	assert.True(t, ok, "Due Date (ID 2) should be in the suggestions")
	assert.Equal(t, "2025-12-31", dueDateField.Value)
}

func TestGetSuggestedCustomFieldsNormalizesJSONWhitespace(t *testing.T) {
	previousTemplate := customFieldTemplate
	customFieldTemplate = template.Must(template.New("custom_field").Parse("{{.Content}}"))
	t.Cleanup(func() {
		customFieldTemplate = previousTemplate
	})

	tests := []struct {
		name          string
		response      string
		expectedValue string
	}{
		{
			name:          "byte order mark",
			response:      "\ufeff[{\"field\":\"Invoice Number\",\"value\":\"INV-BOM\"}]",
			expectedValue: "INV-BOM",
		},
		{
			name:          "non-breaking spaces",
			response:      "[\n\u00a0 {\n\u00a0 \"field\":\"Invoice Number\",\n\u00a0 \"value\":\"INV-NBSP\"\n\u00a0 }\n]",
			expectedValue: "INV-NBSP",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app := &App{
				LLM: &mockLLM{Response: test.response},
				Client: &mockPaperlessClient{
					CustomFields: []CustomField{{ID: 1, Name: "Invoice Number", DataType: "string"}},
				},
			}

			suggestions, err := app.getSuggestedCustomFields(
				context.Background(),
				Document{Content: "Invoice"},
				[]int{1},
				logrus.WithField("test", "json-whitespace"),
				nil,
			)
			require.NoError(t, err)
			require.Len(t, suggestions, 1)
			assert.Equal(t, 1, suggestions[0].ID)
			assert.Equal(t, test.expectedValue, suggestions[0].Value)
		})
	}
}

func TestGetSuggestedTitleSimilarDocumentContext(t *testing.T) {
	previousTemplate := titleTemplate
	titleTemplate = template.Must(template.New("title").Parse(`
{{if .SimilarDocumentTitles}}Similar titles:
{{range .SimilarDocumentTitles}}- {{.}}
{{end}}{{end}}Original: {{.Title}}
Content: {{.Content}}
`))
	t.Cleanup(func() {
		titleTemplate = previousTemplate
	})

	tests := []struct {
		name             string
		similarDocuments []Document
		similarError     error
		expectedPrompt   []string
		excludedPrompt   []string
	}{
		{
			name: "similar documents",
			similarDocuments: []Document{
				{ID: 2, Title: "Invoice January 2023"},
				{ID: 3, Title: " Invoice February 2023 "},
				{ID: 4, Title: "  "},
			},
			expectedPrompt: []string{"Similar titles:", "Invoice January 2023", "Invoice February 2023"},
			excludedPrompt: []string{"-   "},
		},
		{
			name:           "no similar documents",
			excludedPrompt: []string{"Similar titles:"},
		},
		{
			name:           "similar document lookup error",
			similarError:   fmt.Errorf("API error"),
			excludedPrompt: []string{"Similar titles:"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mockClient := &mockPaperlessClient{
				SimilarDocuments:      test.similarDocuments,
				SimilarDocumentsError: test.similarError,
			}
			mockLLM := &mockLLM{Response: "Generated Title"}
			app := &App{LLM: mockLLM, Client: mockClient}

			title, err := app.getSuggestedTitle(
				context.Background(),
				42,
				"document content",
				"document.pdf",
				logrus.WithField("test", test.name),
				nil,
			)
			require.NoError(t, err)
			assert.Equal(t, "Generated Title", title)
			assert.Equal(t, 1, mockClient.GetSimilarDocumentsCalls)
			assert.Equal(t, 42, mockClient.LastSimilarDocumentID)
			assert.Equal(t, 5, mockClient.LastSimilarDocumentCount)
			for _, expected := range test.expectedPrompt {
				assert.Contains(t, mockLLM.lastPrompt, expected)
			}
			for _, excluded := range test.excludedPrompt {
				assert.NotContains(t, mockLLM.lastPrompt, excluded)
			}
		})
	}
}

// TestGetSuggestedCustomFields_DocumentLink pins documentlink support.
// paperless-ngx only accepts a list of document ids for such a field. They
// used to reach the LLM like any other field; it filled them with a reference
// number from the text and paperless-ngx rejected the update with "Value must
// be a list". Now the LLM is asked for the references and paperless-gpt
// resolves them to the ids of the documents they identify.
func TestGetSuggestedCustomFields_DocumentLink(t *testing.T) {
	// Other tests leave a small global tokenLimit behind; run without one.
	t.Setenv("TOKEN_LIMIT", "")
	resetTokenLimit()

	const currentDocID = 533

	tests := []struct {
		name             string
		llmValue         string
		referenceMatches map[string][]int
		referenceError   error
		wantLinks        []int // nil: the field is not suggested at all
	}{
		{
			name:             "single reference resolves to the referenced document",
			llmValue:         `["R10927801"]`,
			referenceMatches: map[string][]int{"R10927801": {412}},
			wantLinks:        []int{412},
		},
		{
			name:             "plain string instead of array is accepted",
			llmValue:         `"R10927801"`,
			referenceMatches: map[string][]int{"R10927801": {412}},
			wantLinks:        []int{412},
		},
		{
			name:             "current document is never linked to itself",
			llmValue:         `["R10927801"]`,
			referenceMatches: map[string][]int{"R10927801": {currentDocID, 412}},
			wantLinks:        []int{412},
		},
		{
			name:     "several references are merged without duplicates",
			llmValue: `["R10927801", "V-2026-17"]`,
			referenceMatches: map[string][]int{
				"R10927801": {412},
				"V-2026-17": {412, 87},
			},
			wantLinks: []int{412, 87},
		},
		{
			name:     "numeric reference is accepted",
			llmValue: `[123456]`,
			referenceMatches: map[string][]int{
				"123456": {87},
			},
			wantLinks: []int{87},
		},
		{
			name:             "reference that only matches the current document is dropped",
			llmValue:         `["R10927801"]`,
			referenceMatches: map[string][]int{"R10927801": {currentDocID}},
		},
		{
			name:             "reference matching too many documents is too generic",
			llmValue:         `["0002058"]`,
			referenceMatches: map[string][]int{"0002058": {1, 2, 3, 4, 5, 6}},
		},
		{
			name:             "reference without a digit is not a document identifier",
			llmValue:         `["Zahlungsdiensteaufsichtsgesetz"]`,
			referenceMatches: map[string][]int{"Zahlungsdiensteaufsichtsgesetz": {412}},
		},
		{
			name:             "short number made of digits alone is not linked",
			llmValue:         `["1772"]`,
			referenceMatches: map[string][]int{"1772": {412}},
		},
		{
			name:             "five-digit number is linked",
			llmValue:         `["47110"]`,
			referenceMatches: map[string][]int{"47110": {412}},
			wantLinks:        []int{412},
		},
		{
			name:             "too short reference is not looked up",
			llmValue:         `["12"]`,
			referenceMatches: map[string][]int{"12": {412}},
		},
		{
			name:           "lookup error drops the reference instead of failing",
			llmValue:       `["R10927801"]`,
			referenceError: fmt.Errorf("paperless-ngx unavailable"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			llm := &mockLLM{Response: `[
				{"field": "Invoice Number", "value": "INV-12345"},
				{"field": "Reference", "value": ` + tt.llmValue + `}
			]`}
			client := &mockPaperlessClient{
				CustomFields: []CustomField{
					{ID: 1, Name: "Invoice Number", DataType: "string"},
					{ID: 2, Name: "Reference", DataType: "documentlink"},
				},
				ReferenceMatches: tt.referenceMatches,
				ReferenceError:   tt.referenceError,
			}
			app := &App{LLM: llm, Client: client}

			usePromptsSandbox(t, map[string]string{"custom_field_prompt.tmpl": "{{ .CustomFieldsXML }}"})
			require.NoError(t, loadTemplates())

			doc := Document{ID: currentDocID, Content: "Mahnung zu Rechnung R10927801"}
			suggestions, err := app.getSuggestedCustomFields(context.Background(), doc, []int{1, 2}, logrus.WithField("test", t.Name()), nil)
			require.NoError(t, err)

			assert.Contains(t, llm.lastPrompt, `<field name="Reference" type="documentlink">`)
			assert.Contains(t, llm.lastPrompt, "<description>", "documentlink fields must tell the LLM to return references")

			invoiceField, ok := findFieldByID(suggestions, 1)
			require.True(t, ok, "other fields must not be affected")
			assert.Equal(t, "INV-12345", invoiceField.Value)

			linkField, ok := findFieldByID(suggestions, 2)
			if tt.wantLinks == nil {
				assert.False(t, ok, "unresolved references must not be suggested, got %v", linkField.Value)
				return
			}
			require.True(t, ok)
			assert.Equal(t, tt.wantLinks, linkField.Value)
		})
	}
}

// TestGetSuggestedCustomFields_OnlySelectedFields checks that the LLM cannot
// fill a custom field the user did not select, even if it returns one.
func TestGetSuggestedCustomFields_OnlySelectedFields(t *testing.T) {
	// Other tests leave a small global tokenLimit behind; run without one.
	t.Setenv("TOKEN_LIMIT", "")
	resetTokenLimit()

	llm := &mockLLM{Response: `[
		{"field": "Invoice Number", "value": "INV-12345"},
		{"field": "Amount", "value": "12.50"}
	]`}
	app := &App{
		LLM: llm,
		Client: &mockPaperlessClient{
			CustomFields: []CustomField{
				{ID: 1, Name: "Invoice Number", DataType: "string"},
				{ID: 3, Name: "Amount", DataType: "float"},
			},
		},
	}

	usePromptsSandbox(t, map[string]string{"custom_field_prompt.tmpl": "{{ .CustomFieldsXML }}"})
	require.NoError(t, loadTemplates())

	suggestions, err := app.getSuggestedCustomFields(context.Background(), Document{Content: "x"}, []int{1}, logrus.WithField("test", t.Name()), nil)
	require.NoError(t, err)

	assert.NotContains(t, llm.lastPrompt, `name="Amount"`, "unselected fields must not be sent to the LLM")
	require.Len(t, suggestions, 1)
	assert.Equal(t, 1, suggestions[0].ID)
}

func TestParseCustomFieldLLMResponse(t *testing.T) {
	known := []string{"Invoice Number", "Due Date"}

	tests := []struct {
		name   string
		in     string
		wantOK bool
		want   []llmCustomFieldResponse
	}{
		{
			name:   "empty is nothing found",
			in:     "  \n",
			wantOK: true,
		},
		{
			name:   "array",
			in:     `[{"field":"Invoice Number","value":"INV-1"},{"field":"Due Date","value":"2025-12-31"}]`,
			wantOK: true,
			want: []llmCustomFieldResponse{
				{Field: "Invoice Number", Value: "INV-1"},
				{Field: "Due Date", Value: "2025-12-31"},
			},
		},
		{
			name:   "single object",
			in:     `{"field":"Invoice Number","value":"INV-1"}`,
			wantOK: true,
			want:   []llmCustomFieldResponse{{Field: "Invoice Number", Value: "INV-1"}},
		},
		{
			name:   "map of known names",
			in:     `{"invoice number":"INV-1","Due Date":"2025-12-31","Not Asked":"x"}`,
			wantOK: true,
			want: []llmCustomFieldResponse{
				{Field: "Invoice Number", Value: "INV-1"},
				{Field: "Due Date", Value: "2025-12-31"},
			},
		},
		{
			name:   "non-breaking space indentation",
			in:     "[\n\u00a0 {\n\u00a0 \u00a0 \"field\": \"Invoice Number\",\n\u00a0 \u00a0 \"value\": \"9264\"\n\u00a0 }\n]",
			wantOK: true,
			want:   []llmCustomFieldResponse{{Field: "Invoice Number", Value: "9264"}},
		},
		{
			name:   "latin-1 misread of a non-breaking space",
			in:     "[\u00c2\u00a0{\"field\": \"Invoice Number\", \"value\": \"9264\"}]",
			wantOK: true,
			want:   []llmCustomFieldResponse{{Field: "Invoice Number", Value: "9264"}},
		},
		{
			name:   "markdown fence",
			in:     "```json\n[{\"field\":\"Invoice Number\",\"value\":\"INV-1\"}]\n```",
			wantOK: true,
			want:   []llmCustomFieldResponse{{Field: "Invoice Number", Value: "INV-1"}},
		},
		{
			name:   "fence without a newline",
			in:     "```json[{\"field\":\"Invoice Number\",\"value\":\"INV-1\"}]```",
			wantOK: true,
			want:   []llmCustomFieldResponse{{Field: "Invoice Number", Value: "INV-1"}},
		},
		{
			name:   "prose is not usable",
			in:     "I could not find any custom fields.",
			wantOK: false,
		},
		{
			name:   "map of unknown names is not usable",
			in:     `{"Something Else":"x"}`,
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseCustomFieldLLMResponse(tt.in, known)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestGetSuggestedCustomFields_AcceptsObjectAndMap(t *testing.T) {
	t.Setenv("TOKEN_LIMIT", "")
	resetTokenLimit()

	for _, response := range []string{
		`{"field":"Invoice Number","value":"INV-1"}`,
		`{"Invoice Number":"INV-1"}`,
		"[\n\u00a0{\"field\":\"Invoice Number\",\"value\":\"INV-1\"}\n]",
	} {
		t.Run(response, func(t *testing.T) {
			app := &App{
				LLM: &mockLLM{Response: response},
				Client: &mockPaperlessClient{
					CustomFields: []CustomField{
						{ID: 1, Name: "Invoice Number", DataType: "string"},
					},
				},
			}
			usePromptsSandbox(t, map[string]string{"custom_field_prompt.tmpl": "{{ .CustomFieldsXML }}"})
			require.NoError(t, loadTemplates())

			suggestions, err := app.getSuggestedCustomFields(context.Background(), Document{Content: "x"}, []int{1}, logrus.WithField("test", t.Name()), nil)
			require.NoError(t, err)
			require.Len(t, suggestions, 1)
			assert.Equal(t, 1, suggestions[0].ID)
			assert.Equal(t, "INV-1", suggestions[0].Value)
		})
	}
}

// Helper function to find a custom field by ID in a slice
func findFieldByID(fields []CustomFieldSuggestion, id int) (CustomFieldSuggestion, bool) {
	for _, field := range fields {
		if field.ID == id {
			return field, true
		}
	}
	return CustomFieldSuggestion{}, false
}

// TestGetSuggestedTags_SystemTagsNeverSuggested pins the tag-loop fix.
//
// paperless-gpt's own tags — the triggers it watches for and the markers it
// writes — must never reach the LLM as candidates, and must never come back
// out as suggestions. #877 shows why: with a paperless-ngx workflow chaining
// OCR to tagging, one suggested `paperless-gpt-ocr-complete` re-triggers the
// workflow, which re-adds the auto tag, which re-runs tagging, forever — and
// every pass bills another round of LLM calls. #1015 reports the same leak for
// FAIL_TAG and the completion tags.
//
// Two paths need covering, because they filter differently: with
// CREATE_NEW_TAGS off, suggestions are intersected with the available list;
// with it on, arbitrary suggestions are kept. The second path is the dangerous
// one, since getSuggestedTags merges the document's original tags into its
// suggestions — and on a document being processed those include the trigger
// tag itself.
func TestGetSuggestedTags_SystemTagsNeverSuggested(t *testing.T) {
	systemTagNames := []string{
		"paperless-gpt",               // MANUAL_TAG
		"paperless-gpt-auto",          // AUTO_TAG
		"paperless-gpt-ocr-auto",      // AUTO_OCR_TAG
		"paperless-gpt-failed",        // FAIL_TAG
		"paperless-gpt-auto-complete", // AUTO_TAG_COMPLETE
		"paperless-gpt-ocr-complete",  // PDF_OCR_COMPLETE_TAG
	}

	for _, createNew := range []bool{false, true} {
		name := "CREATE_NEW_TAGS off"
		if createNew {
			name = "CREATE_NEW_TAGS on"
		}
		t.Run(name, func(t *testing.T) {
			previous := struct{ manual, auto, ocrAuto, fail, complete, ocrComplete string }{
				manualTag, autoTag, autoOcrTag, failTag, autoTagComplete, pdfOCRCompleteTag,
			}
			manualTag, autoTag, autoOcrTag = "paperless-gpt", "paperless-gpt-auto", "paperless-gpt-ocr-auto"
			failTag, autoTagComplete, pdfOCRCompleteTag = "paperless-gpt-failed", "paperless-gpt-auto-complete", "paperless-gpt-ocr-complete"
			previousCreateNewTags := createNewTags
			createNewTags = createNew
			isolateWorkflowSettings(t)
			useWorkflows(t, []WorkflowConfig{{
				ID:            "inv",
				TriggerTag:    "paperless-gpt-invoices",
				CompletionTag: "paperless-gpt-invoices-done",
			}}...)
			t.Cleanup(func() {
				manualTag, autoTag, autoOcrTag = previous.manual, previous.auto, previous.ocrAuto
				failTag, autoTagComplete, pdfOCRCompleteTag = previous.fail, previous.complete, previous.ocrComplete
				createNewTags = previousCreateNewTags
			})

			previousTemplate := tagTemplate
			tagTemplate = template.Must(template.New("tag").Parse(testTagTemplate))
			t.Cleanup(func() { tagTemplate = previousTemplate })

			excluded := append([]string{}, systemTagNames...)
			excluded = append(excluded, "paperless-gpt-invoices", "paperless-gpt-invoices-done")

			// The model echoes back every system tag plus one real one — the
			// worst case, and what actually happens when the system tags are
			// visible in the prompt.
			mockLLM := &mockLLM{Response: strings.Join(append(excluded, "Invoice"), ",")}
			app := &App{LLM: mockLLM}

			// Available tags as paperless-ngx would report them: real tags and
			// paperless-gpt's own, since they all live in the same namespace.
			availableTags := append([]string{"Invoice", "Insurance"}, excluded...)
			// The document carries the trigger tag it is being processed under.
			originalTags := []string{"Insurance", "paperless-gpt-auto"}

			suggested, err := app.getSuggestedTags(
				context.Background(), "Some document content", "A Title",
				availableTags, originalTags, logrus.WithField("test", "system-tags"), nil,
			)
			require.NoError(t, err)

			for _, systemTag := range excluded {
				assert.NotContains(t, suggested, systemTag,
					"system tag %q must never be suggested", systemTag)
			}
			// The real tags must still survive the filtering.
			assert.Contains(t, suggested, "Invoice")
			assert.Contains(t, suggested, "Insurance")

			// And they must not have been offered to the model either.
			for _, systemTag := range excluded {
				assert.NotContains(t, mockLLM.lastPrompt, systemTag,
					"system tag %q must not appear in the prompt", systemTag)
			}
		})
	}
}

func TestHandoverTags(t *testing.T) {
	previousManualTag, previousAutoTag := manualTag, autoTag
	manualTag, autoTag = "paperless-gpt", "paperless-gpt-auto"
	t.Cleanup(func() { manualTag, autoTag = previousManualTag, previousAutoTag })

	invoices := &WorkflowConfig{ID: "wf1", TriggerTag: "invoices", CompletionTag: "invoices-done"}
	noCompletion := &WorkflowConfig{ID: "wf2", TriggerTag: "contracts"}

	tests := []struct {
		name       string
		complete   string
		auto       bool
		trigger    string
		workflow   *WorkflowConfig
		wantRemove []string
		wantAdd    []string
	}{
		{"manual review adds no completion tag", "done", false, "", nil,
			[]string{"paperless-gpt", "paperless-gpt-auto"}, nil},
		{"default path adds AUTO_TAG_COMPLETE", "done", true, "paperless-gpt-auto", nil,
			[]string{"paperless-gpt", "paperless-gpt-auto"}, []string{"done"}},
		{"workflow completion tag replaces AUTO_TAG_COMPLETE", "done", true, "invoices", invoices,
			[]string{"paperless-gpt", "paperless-gpt-auto", "invoices"}, []string{"invoices-done"}},
		{"workflow without completion tag falls back to AUTO_TAG_COMPLETE", "done", true, "contracts", noCompletion,
			[]string{"paperless-gpt", "paperless-gpt-auto", "contracts"}, []string{"done"}},
		{"no completion tag configured anywhere", "", true, "contracts", noCompletion,
			[]string{"paperless-gpt", "paperless-gpt-auto", "contracts"}, nil},
		{"trigger equal to AUTO_TAG is not listed twice", "", true, "Paperless-GPT-Auto", nil,
			[]string{"paperless-gpt", "paperless-gpt-auto"}, nil},
		{"the polled tag comes off, whatever the workflow says", "", true, "routed-tag", invoices,
			[]string{"paperless-gpt", "paperless-gpt-auto", "routed-tag"}, []string{"invoices-done"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			remove, add := handoverTags(tt.complete, tt.auto, tt.trigger, tt.workflow)
			assert.Equal(t, tt.wantRemove, remove)
			assert.Equal(t, tt.wantAdd, add)
		})
	}
}

// A selected custom field may itself be called "field". A map reply for it
// must not be mistaken for a single {"field": ..., "value": ...} object.
func TestParseCustomFieldLLMResponseFieldNamedField(t *testing.T) {
	got, ok := parseCustomFieldLLMResponse(`{"field":"INV-1","Due Date":"2025-12-31"}`, []string{"field", "Due Date"})
	require.True(t, ok)
	assert.Equal(t, []llmCustomFieldResponse{
		{Field: "field", Value: "INV-1"},
		{Field: "Due Date", Value: "2025-12-31"},
	}, got)

	got, ok = parseCustomFieldLLMResponse(`{"field":"field","value":"INV-1"}`, []string{"field"})
	require.True(t, ok)
	assert.Equal(t, []llmCustomFieldResponse{{Field: "field", Value: "INV-1"}}, got, "with both keys it is a single object")
}

func TestRawMessageByKeyIsDeterministic(t *testing.T) {
	obj := map[string]json.RawMessage{"due date": json.RawMessage(`"a"`), "DUE DATE": json.RawMessage(`"b"`)}
	for i := 0; i < 20; i++ {
		raw, ok := rawMessageByKey(obj, "Due Date")
		require.True(t, ok)
		assert.Equal(t, `"b"`, string(raw), "sorted order: \"DUE DATE\" before \"due date\"")
	}
}

// usePromptsSandbox runs the test in a temporary working directory with a
// copy of default_prompts/ and the given prompt files. loadTemplates reads
// and writes prompts/ relative to the working directory; running it in the
// repository would overwrite, and the old cleanup delete, a developer's local
// prompts and AI workflows.
func usePromptsSandbox(t *testing.T, prompts map[string]string) {
	t.Helper()
	defaults, err := filepath.Abs("default_prompts")
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, os.CopyFS(filepath.Join(dir, "default_prompts"), os.DirFS(defaults)))
	t.Chdir(dir)
	require.NoError(t, os.MkdirAll("prompts", 0o755))
	for name, content := range prompts {
		require.NoError(t, os.WriteFile(filepath.Join("prompts", name), []byte(content), 0o644))
	}
}

// A prompt in prompts/ that is still an earlier default was never customised
// and gets the current default; a customised prompt is left alone.
func TestLoadTemplatesUpdatesOutdatedDefaults(t *testing.T) {
	const outdated = "an earlier default custom field prompt {{ .CustomFieldsXML }}"
	sum := sha256.Sum256([]byte(outdated))
	previous := previousDefaultPrompts
	previousDefaultPrompts = map[string][]string{"custom_field_prompt.tmpl": {hex.EncodeToString(sum[:])}}
	t.Cleanup(func() { previousDefaultPrompts = previous })

	usePromptsSandbox(t, map[string]string{
		"custom_field_prompt.tmpl": outdated,
		"title_prompt.tmpl":        "my own title prompt {{ .Content }}",
	})
	require.NoError(t, loadTemplates())

	current, err := os.ReadFile("default_prompts/custom_field_prompt.tmpl")
	require.NoError(t, err)
	got, err := os.ReadFile("prompts/custom_field_prompt.tmpl")
	require.NoError(t, err)
	assert.Equal(t, string(current), string(got), "the outdated default is replaced")

	got, err = os.ReadFile("prompts/title_prompt.tmpl")
	require.NoError(t, err)
	assert.Equal(t, "my own title prompt {{ .Content }}", string(got), "a customised prompt is kept")
}

func TestPreviousDefaultPromptsAreForExistingPrompts(t *testing.T) {
	for name, sums := range previousDefaultPrompts {
		current, err := os.ReadFile(filepath.Join("default_prompts", name))
		require.NoError(t, err, "history entry for a prompt that no longer exists: %s", name)
		sum := sha256.Sum256(current)
		assert.NotContains(t, sums, hex.EncodeToString(sum[:]), "%s: the current default must not be listed as outdated", name)
	}
}
