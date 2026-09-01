package main

import (
	"context"
	"fmt"
	"os"
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
			_, err = app.getSuggestedTitle(ctx, 0, truncatedContent, "Test Title", testLogger)
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

	_, err := app.getSuggestedCorrespondent(ctx, longContent, "Test Title", availableCorrespondents, correspondentBlackList)
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

	_, err := app.getSuggestedTags(ctx, longContent, "Test Title", availableTags, originalTags, testLogger)
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

		tags, err := app.getSuggestedTags(ctx, "Some document content", "Test Invoice", availableTags, originalTags, testLogger)
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

		tags, err := app.getSuggestedTags(ctx, "Some document content", "Test Invoice", availableTags, originalTags, testLogger)
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

		tags, err := app.getSuggestedTags(ctx, "Some document content", "Test Invoice", availableTags, originalTags, testLogger)
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

		tags, err := app.getSuggestedTags(ctx, "Some document content", "Test Invoice", availableTags, originalTags, testLogger)
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

		tags, err := app.getSuggestedTags(ctx, "Some document content", "Test Invoice", availableTags, originalTags, testLogger)
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

	_, err := app.getSuggestedTitle(ctx, 0, longContent, "Original Title", testLogger)
	require.NoError(t, err)

	// Verify the final prompt size
	splitter := textsplitter.NewTokenSplitter()
	tokens, err := splitter.SplitText(mockLLM.lastPrompt)
	require.NoError(t, err)

	// Final prompt should be within token limit
	assert.LessOrEqual(t, len(tokens), 50, "Final prompt should be within token limit")
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

	_, err := app.getSuggestedCreatedDate(ctx, longContent, "Example Title", "example.pdf", testLogger)
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
	err := os.MkdirAll("prompts", 0755)
	require.NoError(t, err)
	err = os.WriteFile("prompts/custom_field_prompt.tmpl", []byte("test"), 0644)
	require.NoError(t, err)
	defer os.RemoveAll("prompts")

	err = loadTemplates()
	require.NoError(t, err)

	// 2. Define Inputs
	doc := Document{
		Content: "The invoice number is INV-12345 and the due date is 2025-12-31.",
	}
	selectedFieldIDs := []int{1, 2} // User has selected "Invoice Number" and "Due Date"

	// 3. Execute
	testLogger := logrus.WithField("test", "TestGetSuggestedCustomFields")
	suggestions, err := app.getSuggestedCustomFields(context.Background(), doc, selectedFieldIDs, testLogger)

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

// Helper function to find a custom field by ID in a slice
func findFieldByID(fields []CustomFieldSuggestion, id int) (CustomFieldSuggestion, bool) {
	for _, field := range fields {
		if field.ID == id {
			return field, true
		}
	}
	return CustomFieldSuggestion{}, false
}
