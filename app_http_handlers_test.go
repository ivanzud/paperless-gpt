package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type undoSummaryClient struct {
	*PaperlessClient
	deletedDocumentID  int
	deletedNoteID      int
	deleteErr          error
	updateErr          error
	updateErrs         map[int]error
	updatedDocumentIDs []int
	updatedSuggestions []DocumentSuggestion
	updateIsUndo       bool
	getDocumentCalled  bool
	updateCalled       bool
}

func (client *undoSummaryClient) DeleteDocumentNote(_ context.Context, documentID int, noteID int) error {
	client.deletedDocumentID = documentID
	client.deletedNoteID = noteID
	return client.deleteErr
}

func (client *undoSummaryClient) GetDocument(context.Context, int) (Document, error) {
	client.getDocumentCalled = true
	return Document{}, nil
}

func (client *undoSummaryClient) UpdateDocuments(_ context.Context, documents []DocumentSuggestion, _ *gorm.DB, isUndo bool) error {
	client.updateCalled = true
	client.updatedSuggestions = append(client.updatedSuggestions, documents...)
	client.updateIsUndo = isUndo
	if len(documents) > 0 {
		documentID := documents[0].ID
		client.updatedDocumentIDs = append(client.updatedDocumentIDs, documentID)
		if err, exists := client.updateErrs[documentID]; exists {
			return err
		}
	}
	return client.updateErr
}

// setupTestRouter creates a gin router for testing and sets up necessary directories and files.
func setupTestRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.Default()

	// Isolate to a temp working directory
	tmp := t.TempDir()
	cwd, _ := os.Getwd()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	// Create test directories
	require.NoError(t, os.MkdirAll("prompts", os.ModePerm))
	require.NoError(t, os.MkdirAll("default_prompts", os.ModePerm))

	// Create dummy default prompt files for loadTemplates to find
	promptFiles := []string{
		"title_prompt.tmpl",
		"tag_prompt.tmpl",
		"correspondent_prompt.tmpl",
		"document_type_prompt.tmpl",
		"created_date_prompt.tmpl",
		"custom_field_prompt.tmpl",
		"summary_prompt.tmpl",
		"ocr_prompt.tmpl",
		"adhoc-analysis_prompt.tmpl",
	}
	for _, file := range promptFiles {
		require.NoError(
			t,
			os.WriteFile(
				filepath.Join("default_prompts", file),
				[]byte("default content"),
				0644,
			),
		)
	}

	return router
}

func TestGetPromptsHandler(t *testing.T) {
	router := setupTestRouter(t)

	// Create a dummy prompt file
	promptContent := "Hello {{.Name}}"
	require.NoError(t, os.WriteFile(filepath.Join("prompts", "test_prompt.tmpl"), []byte(promptContent), 0644))

	router.GET("/api/prompts", getPromptsHandler)

	req, _ := http.NewRequest("GET", "/api/prompts", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]string
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
	assert.Contains(t, response, "test_prompt.tmpl")
	assert.Equal(t, promptContent, response["test_prompt.tmpl"])
}

func TestUpdatePromptsHandler(t *testing.T) {
	router := setupTestRouter(t)

	// Create a dummy prompt file to be updated
	require.NoError(t, os.WriteFile(filepath.Join("prompts", "update_prompt.tmpl"), []byte("Initial content"), 0644))
	// The setup function already creates the default prompts, so we just need the one we are updating
	require.NoError(t, os.WriteFile(filepath.Join("default_prompts", "update_prompt.tmpl"), []byte("Default content"), 0644))

	router.POST("/api/prompts", updatePromptsHandler)

	t.Run("Successful update", func(t *testing.T) {
		newContent := "Updated content with {{.Value}}"
		payload := gin.H{
			"filename": "update_prompt.tmpl",
			"content":  newContent,
		}
		jsonPayload, _ := json.Marshal(payload)

		req, _ := http.NewRequest("POST", "/api/prompts", bytes.NewBuffer(jsonPayload))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		// Verify file content
		fileContent, err := os.ReadFile(filepath.Join("prompts", "update_prompt.tmpl"))
		assert.NoError(t, err)
		assert.Equal(t, newContent, string(fileContent))
	})

	t.Run("Invalid template content", func(t *testing.T) {
		invalidContent := "Invalid {{.Value"
		payload := gin.H{
			"filename": "update_prompt.tmpl",
			"content":  invalidContent,
		}
		jsonPayload, _ := json.Marshal(payload)

		req, _ := http.NewRequest("POST", "/api/prompts", bytes.NewBuffer(jsonPayload))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("File not found", func(t *testing.T) {
		payload := gin.H{
			"filename": "non_existent_prompt.tmpl",
			"content":  "Some content",
		}
		jsonPayload, _ := json.Marshal(payload)

		req, _ := http.NewRequest("POST", "/api/prompts", bytes.NewBuffer(jsonPayload))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		// This test is now for a successful creation of a new file, which the handler should do.
		// The handler logic will be updated in the next step.
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("Path traversal attempt", func(t *testing.T) {
		payload := gin.H{
			"filename": "../evil.tmpl",
			"content":  "irrelevant",
		}
		jsonPayload, _ := json.Marshal(payload)

		req, _ := http.NewRequest("POST", "/api/prompts", bytes.NewBuffer(jsonPayload))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestGetVersionHandler(t *testing.T) {
	router := setupTestRouter(t)
	router.GET("/api/version", getVersionHandler)

	req, _ := http.NewRequest("GET", "/api/version", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]string
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	// Check that the response contains the expected fields
	assert.Contains(t, response, "version")
	assert.Contains(t, response, "commit")
	assert.Contains(t, response, "buildDate")

	// Verify the values are the default development values
	assert.Equal(t, "devVersion", response["version"])
	assert.Equal(t, "devCommit", response["commit"])
	assert.Equal(t, "devBuildDate", response["buildDate"])
}

func TestUpdateDocumentsHandlerReturnsStructuredPartialSuccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	client := &undoSummaryClient{
		PaperlessClient: &PaperlessClient{},
		updateErr: &PartialUpdateError{
			DocumentID:    42,
			DroppedFields: []string{"summary"},
		},
	}
	app := &App{Client: client}
	router.PATCH("/api/update-documents", app.updateDocumentsHandler)

	request := httptest.NewRequest(
		http.MethodPatch,
		"/api/update-documents",
		bytes.NewBufferString(`[{"id":42}]`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	assert.Equal(t, http.StatusMultiStatus, response.Code)
	assert.True(t, client.updateCalled)

	var payload struct {
		Status   string                  `json:"status"`
		Outcomes []documentUpdateOutcome `json:"outcomes"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
	assert.Equal(t, "partial", payload.Status)
	require.Len(t, payload.Outcomes, 1)
	assert.Equal(t, 42, payload.Outcomes[0].DocumentID)
	assert.Equal(t, "partial", payload.Outcomes[0].Status)
	assert.Equal(t, []string{"summary"}, payload.Outcomes[0].DroppedFields)
}

func TestUpdateDocumentsHandlerReportsEveryBatchOutcome(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	client := &undoSummaryClient{
		PaperlessClient: &PaperlessClient{},
		updateErrs: map[int]error{
			42: &PartialUpdateError{DocumentID: 42, DroppedFields: []string{"summary"}},
			43: errors.New("hard update failure"),
		},
	}
	app := &App{Client: client}
	router.PATCH("/api/update-documents", app.updateDocumentsHandler)

	request := httptest.NewRequest(
		http.MethodPatch,
		"/api/update-documents",
		bytes.NewBufferString(`[{"id":41},{"id":42},{"id":43}]`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	assert.Equal(t, http.StatusMultiStatus, response.Code)
	assert.Equal(t, []int{41, 42, 43}, client.updatedDocumentIDs)

	var payload struct {
		Status   string                  `json:"status"`
		Outcomes []documentUpdateOutcome `json:"outcomes"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
	assert.Equal(t, "partial", payload.Status)
	require.Len(t, payload.Outcomes, 3)
	assert.Equal(t, documentUpdateOutcome{DocumentID: 41, Status: "applied"}, payload.Outcomes[0])
	assert.Equal(t, documentUpdateOutcome{DocumentID: 42, Status: "partial", DroppedFields: []string{"summary"}}, payload.Outcomes[1])
	assert.Equal(t, documentUpdateOutcome{DocumentID: 43, Status: "failed"}, payload.Outcomes[2])
}

func TestUndoSummaryModificationDeletesCreatedNote(t *testing.T) {
	for _, testCase := range []struct {
		name             string
		deleteErr        error
		expectedStatus   int
		expectedUndone   bool
		expectedDeleteID int
	}{
		{
			name:             "deletes exact note then marks history undone",
			expectedStatus:   http.StatusOK,
			expectedUndone:   true,
			expectedDeleteID: 77,
		},
		{
			name:             "keeps history active when Paperless rejects delete",
			deleteErr:        errors.New("paperless returned 500"),
			expectedStatus:   http.StatusInternalServerError,
			expectedDeleteID: 77,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			router := gin.New()
			db, err := InitializeTestDB()
			require.NoError(t, err)

			noteID := 77
			modification := ModificationHistory{
				DocumentID:    42,
				ModField:      "summary",
				PreviousValue: "",
				NewValue:      "Reviewed summary",
				RemoteID:      &noteID,
			}
			require.NoError(t, InsertModification(db, &modification))

			client := &undoSummaryClient{
				PaperlessClient: &PaperlessClient{},
				deleteErr:       testCase.deleteErr,
			}
			app := &App{Client: client, Database: db}
			router.POST("/api/undo-modification/:id", app.undoModificationHandler)

			request := httptest.NewRequest(
				http.MethodPost,
				fmt.Sprintf("/api/undo-modification/%d", modification.ID),
				nil,
			)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			assert.Equal(t, testCase.expectedStatus, response.Code)
			assert.Equal(t, 42, client.deletedDocumentID)
			assert.Equal(t, testCase.expectedDeleteID, client.deletedNoteID)
			assert.False(t, client.getDocumentCalled)
			assert.False(t, client.updateCalled)

			stored, err := GetModification(db, int(modification.ID))
			require.NoError(t, err)
			assert.Equal(t, testCase.expectedUndone, stored.Undone)
		})
	}
}

func TestUndoSummaryModificationWithoutRemoteIDIsRejected(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	db, err := InitializeTestDB()
	require.NoError(t, err)

	modification := ModificationHistory{
		DocumentID:  43,
		ModField:    "summary",
		NewValue:    "Untracked summary",
		DateChanged: time.Now().Format(time.RFC3339),
	}
	require.NoError(t, InsertModification(db, &modification))

	client := &undoSummaryClient{PaperlessClient: &PaperlessClient{}}
	app := &App{Client: client, Database: db}
	router.POST("/api/undo-modification/:id", app.undoModificationHandler)

	request := httptest.NewRequest(
		http.MethodPost,
		fmt.Sprintf("/api/undo-modification/%d", modification.ID),
		nil,
	)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	assert.Equal(t, http.StatusInternalServerError, response.Code)
	assert.Zero(t, client.deletedDocumentID)
	assert.Zero(t, client.deletedNoteID)

	stored, err := GetModification(db, int(modification.ID))
	require.NoError(t, err)
	assert.False(t, stored.Undone)
}

func TestUndoTagModificationUsesJSONHistory(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	db, err := InitializeTestDB()
	require.NoError(t, err)

	modification := ModificationHistory{
		DocumentID:    44,
		ModField:      "tags",
		PreviousValue: `["invoice","archive"]`,
		NewValue:      `["invoice","paperless-gpt-auto-complete"]`,
	}
	require.NoError(t, InsertModification(db, &modification))

	client := &undoSummaryClient{PaperlessClient: &PaperlessClient{}}
	app := &App{Client: client, Database: db}
	router.POST("/api/undo-modification/:id", app.undoModificationHandler)

	request := httptest.NewRequest(
		http.MethodPost,
		fmt.Sprintf("/api/undo-modification/%d", modification.ID),
		nil,
	)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	assert.Equal(t, http.StatusOK, response.Code)
	assert.True(t, client.updateIsUndo)
	require.Len(t, client.updatedSuggestions, 1)
	assert.Equal(t, []string{"invoice", "archive"}, client.updatedSuggestions[0].SuggestedTags)

	stored, err := GetModification(db, int(modification.ID))
	require.NoError(t, err)
	assert.True(t, stored.Undone)
}
