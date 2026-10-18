package main

import (
	"strings"

	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// GetAllTags backs the tag-undo path, which consults the catalog so it can
// decode history recorded in the older, space-separated format.
func (client *undoSummaryClient) GetAllTags(context.Context) (map[string]int, error) {
	return map[string]int{"invoice": 1, "archive": 2, "paperless-gpt-auto-complete": 3}, nil
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

	// One partially applied document is a success with details; 207 is kept
	// for batches whose documents ended differently.
	assert.Equal(t, http.StatusOK, response.Code)
	assert.True(t, client.updateCalled)

	var payload struct {
		Status   string                  `json:"status"`
		Outcomes []documentUpdateOutcome `json:"outcomes"`
		Partial  *struct {
			DocumentID    int      `json:"document_id"`
			DroppedFields []string `json:"dropped_fields"`
		} `json:"partial"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
	assert.Equal(t, "partial", payload.Status)
	require.Len(t, payload.Outcomes, 1)
	assert.Equal(t, 42, payload.Outcomes[0].DocumentID)
	assert.Equal(t, "partial", payload.Outcomes[0].Status)
	assert.Equal(t, []string{"summary"}, payload.Outcomes[0].DroppedFields)

	// The review UI reads `partial` to say what paperless-ngx did not take.
	require.NotNil(t, payload.Partial, "a partially applied document must be reported under `partial`")
	assert.Equal(t, 42, payload.Partial.DocumentID)
	assert.Equal(t, []string{"summary"}, payload.Partial.DroppedFields)
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

func TestWorkflowCRUDHandlers(t *testing.T) {
	router := setupTestRouter(t)
	isolateWorkflowSettings(t)
	app := &App{}
	router.GET("/api/workflows", app.listWorkflowsHandler)
	router.POST("/api/workflows", app.createWorkflowHandler)
	router.PUT("/api/workflows/:id", app.updateWorkflowHandler)
	router.DELETE("/api/workflows/:id", app.deleteWorkflowHandler)

	doJSON := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var buf bytes.Buffer
		if body != nil {
			require.NoError(t, json.NewEncoder(&buf).Encode(body))
		}
		req, err := http.NewRequest(method, path, &buf)
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	t.Run("GET empty list", func(t *testing.T) {
		w := doJSON(http.MethodGet, "/api/workflows", nil)
		assert.Equal(t, http.StatusOK, w.Code)
		var got []WorkflowConfig
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
		assert.Empty(t, got)
	})

	t.Run("POST without trigger_tag is 400", func(t *testing.T) {
		w := doJSON(http.MethodPost, "/api/workflows", WorkflowConfig{Name: "No Trigger"})
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	var created WorkflowConfig
	t.Run("POST creates a workflow", func(t *testing.T) {
		w := doJSON(http.MethodPost, "/api/workflows", WorkflowConfig{
			Name:          "Invoices",
			TriggerTag:    "paperless-gpt-invoices",
			CompletionTag: "paperless-gpt-invoices-done",
		})
		assert.Equal(t, http.StatusCreated, w.Code)
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
		assert.Equal(t, "paperless-gpt-invoices", created.ID)
		assert.Equal(t, "Invoices", created.Name)

		list := doJSON(http.MethodGet, "/api/workflows", nil)
		var got []WorkflowConfig
		require.NoError(t, json.Unmarshal(list.Body.Bytes(), &got))
		require.Len(t, got, 1)
		assert.Equal(t, created.ID, got[0].ID)
	})

	t.Run("POST duplicate trigger_tag is 409", func(t *testing.T) {
		w := doJSON(http.MethodPost, "/api/workflows", WorkflowConfig{
			Name:       "Also invoices",
			TriggerTag: "paperless-gpt-invoices",
		})
		assert.Equal(t, http.StatusConflict, w.Code)
	})

	t.Run("PUT updates and PUT stolen trigger is 409", func(t *testing.T) {
		other := doJSON(http.MethodPost, "/api/workflows", WorkflowConfig{
			Name:       "Contracts",
			TriggerTag: "paperless-gpt-contracts",
		})
		assert.Equal(t, http.StatusCreated, other.Code)
		var contracts WorkflowConfig
		require.NoError(t, json.Unmarshal(other.Body.Bytes(), &contracts))

		created.Name = "Invoices (updated)"
		upd := doJSON(http.MethodPut, "/api/workflows/"+created.ID, created)
		assert.Equal(t, http.StatusOK, upd.Code)

		stolen := created
		stolen.TriggerTag = "paperless-gpt-contracts"
		conflict := doJSON(http.MethodPut, "/api/workflows/"+created.ID, stolen)
		assert.Equal(t, http.StatusConflict, conflict.Code)

		noTrigger := created
		noTrigger.TriggerTag = "  "
		missing := doJSON(http.MethodPut, "/api/workflows/"+created.ID, noTrigger)
		assert.Equal(t, http.StatusBadRequest, missing.Code, "an update must not clear the trigger tag")

		del := doJSON(http.MethodDelete, "/api/workflows/"+contracts.ID, nil)
		assert.Equal(t, http.StatusOK, del.Code)
	})

	t.Run("DELETE removes a workflow and unknown is 404", func(t *testing.T) {
		w := doJSON(http.MethodDelete, "/api/workflows/"+created.ID, nil)
		assert.Equal(t, http.StatusOK, w.Code)
		missing := doJSON(http.MethodDelete, "/api/workflows/"+created.ID, nil)
		assert.Equal(t, http.StatusNotFound, missing.Code)
	})
}

func TestValidateWorkflowTags(t *testing.T) {
	prev := []string{autoTag, manualTag, autoOcrTag, failTag, autoTagComplete, pdfOCRCompleteTag}
	autoTag, manualTag, autoOcrTag, failTag, autoTagComplete, pdfOCRCompleteTag =
		"paperless-gpt-auto", "paperless-gpt", "paperless-gpt-ocr-auto", "paperless-gpt-failed", "paperless-gpt-done", "paperless-gpt-ocr-done"
	t.Cleanup(func() {
		autoTag, manualTag, autoOcrTag, failTag, autoTagComplete, pdfOCRCompleteTag = prev[0], prev[1], prev[2], prev[3], prev[4], prev[5]
	})

	others := []WorkflowConfig{{ID: "invoices", TriggerTag: "invoices", CompletionTag: "invoices-done"}}

	tests := []struct {
		name       string
		wf         WorkflowConfig
		wantStatus int
	}{
		{"valid workflow", WorkflowConfig{TriggerTag: "contracts", CompletionTag: "contracts-done"}, http.StatusOK},
		{"completion tag may share AUTO_TAG_COMPLETE", WorkflowConfig{TriggerTag: "contracts", CompletionTag: "Paperless-GPT-Done"}, http.StatusOK},
		{"missing trigger", WorkflowConfig{CompletionTag: "x"}, http.StatusBadRequest},
		{"trigger is AUTO_TAG", WorkflowConfig{TriggerTag: "Paperless-GPT-Auto"}, http.StatusBadRequest},
		{"trigger is MANUAL_TAG", WorkflowConfig{TriggerTag: "paperless-gpt"}, http.StatusBadRequest},
		{"trigger is FAIL_TAG", WorkflowConfig{TriggerTag: "paperless-gpt-failed"}, http.StatusBadRequest},
		{"trigger is AUTO_TAG_COMPLETE", WorkflowConfig{TriggerTag: "paperless-gpt-done"}, http.StatusBadRequest},
		{"trigger is PDF_OCR_COMPLETE_TAG", WorkflowConfig{TriggerTag: "paperless-gpt-ocr-done"}, http.StatusBadRequest},
		{"completion is AUTO_TAG", WorkflowConfig{TriggerTag: "contracts", CompletionTag: "paperless-gpt-auto"}, http.StatusBadRequest},
		{"completion is AUTO_OCR_TAG", WorkflowConfig{TriggerTag: "contracts", CompletionTag: "paperless-gpt-ocr-auto"}, http.StatusBadRequest},
		{"completion equals own trigger", WorkflowConfig{TriggerTag: "contracts", CompletionTag: "Contracts"}, http.StatusBadRequest},
		{"duplicate trigger ignores case", WorkflowConfig{TriggerTag: "INVOICES"}, http.StatusConflict},
		{"completion is another workflow's trigger", WorkflowConfig{TriggerTag: "contracts", CompletionTag: "invoices"}, http.StatusConflict},
		{"trigger is another workflow's completion", WorkflowConfig{TriggerTag: "invoices-done"}, http.StatusConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, err := validateWorkflowTags(normalizeWorkflow(tt.wf), others)
			assert.Equal(t, tt.wantStatus, status)
			if tt.wantStatus == http.StatusOK {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

// tagEnsuringClient records the tags the workflow handlers ask paperless-ngx
// to create.
type tagEnsuringClient struct {
	ClientInterface
	ensured []string
}

func (c *tagEnsuringClient) EnsureTagExists(_ context.Context, tag string) error {
	c.ensured = append(c.ensured, tag)
	return nil
}

func TestWorkflowHandlersEnsureTagsExist(t *testing.T) {
	router := setupTestRouter(t)
	isolateWorkflowSettings(t)
	client := &tagEnsuringClient{}
	app := &App{Client: client}
	router.POST("/api/workflows", app.createWorkflowHandler)
	router.PUT("/api/workflows/:id", app.updateWorkflowHandler)

	send := func(method, path string, wf WorkflowConfig) int {
		var buf bytes.Buffer
		require.NoError(t, json.NewEncoder(&buf).Encode(wf))
		req, err := http.NewRequest(method, path, &buf)
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w.Code
	}

	require.Equal(t, http.StatusCreated, send(http.MethodPost, "/api/workflows",
		WorkflowConfig{ID: "invoices", TriggerTag: " invoices ", CompletionTag: "invoices-done"}))
	assert.Equal(t, []string{"invoices", "invoices-done"}, client.ensured, "tags are created trimmed")

	client.ensured = nil
	require.Equal(t, http.StatusOK, send(http.MethodPut, "/api/workflows/invoices",
		WorkflowConfig{TriggerTag: "invoices", CompletionTag: "invoices-booked"}))
	assert.Equal(t, []string{"invoices", "invoices-booked"}, client.ensured)

	client.ensured = nil
	require.Equal(t, http.StatusBadRequest, send(http.MethodPut, "/api/workflows/invoices",
		WorkflowConfig{TriggerTag: "invoices", CompletionTag: "invoices"}))
	assert.Empty(t, client.ensured, "a rejected workflow creates no tags")
}

// A save that fails must not leave the change active in memory: the client
// was told it failed, so background processing must keep using the
// workflows as they are on disk.
func TestWorkflowHandlersKeepStateOnFailedSave(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	router := setupTestRouter(t)
	isolateWorkflowSettings(t)
	app := &App{}
	router.POST("/api/workflows", app.createWorkflowHandler)
	router.PUT("/api/workflows/:id", app.updateWorkflowHandler)
	router.DELETE("/api/workflows/:id", app.deleteWorkflowHandler)

	existing := WorkflowConfig{ID: "invoices", Name: "Invoices", TriggerTag: "invoices", CompletionTag: "invoices-done"}
	useWorkflows(t, existing)

	// Read-only directories make every write fail.
	wfDir := filepath.Join(workflows.dir, "invoices")
	require.NoError(t, os.Chmod(wfDir, 0o555))
	require.NoError(t, os.Chmod(workflows.dir, 0o555))
	t.Cleanup(func() {
		_ = os.Chmod(workflows.dir, 0o755)
		_ = os.Chmod(wfDir, 0o755)
	})

	send := func(method, path string, wf *WorkflowConfig) int {
		var buf bytes.Buffer
		if wf != nil {
			require.NoError(t, json.NewEncoder(&buf).Encode(wf))
		}
		req, err := http.NewRequest(method, path, &buf)
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w.Code
	}

	assert.Equal(t, http.StatusInternalServerError, send(http.MethodPost, "/api/workflows", &WorkflowConfig{TriggerTag: "contracts"}))
	assert.Equal(t, []WorkflowConfig{existing}, workflows.List(), "create")

	assert.Equal(t, http.StatusInternalServerError, send(http.MethodPut, "/api/workflows/invoices", &WorkflowConfig{TriggerTag: "bills"}))
	assert.Equal(t, []WorkflowConfig{existing}, workflows.List(), "update")

	assert.Equal(t, http.StatusInternalServerError, send(http.MethodDelete, "/api/workflows/invoices", nil))
	assert.Equal(t, []WorkflowConfig{existing}, workflows.List(), "delete")
}

// partialUpdateClient applies an update but reports a dropped field, then
// records the fail-tag update that follows.
type partialUpdateClient struct {
	mockPaperlessClient
	calls [][]DocumentSuggestion
}

func (c *partialUpdateClient) UpdateDocuments(_ context.Context, docs []DocumentSuggestion, _ *gorm.DB, _ bool) error {
	c.calls = append(c.calls, docs)
	if len(c.calls) == 1 {
		return &PartialUpdateError{DocumentID: docs[0].ID, DroppedFields: []string{"correspondent"}}
	}
	return nil
}

func (c *partialUpdateClient) GetDocument(_ context.Context, id int) (Document, error) {
	return Document{ID: id, Tags: []string{"invoice"}}, nil
}

// A manual apply that went through without some fields is not a failure:
// the UI must not say nothing was applied, and the document is flagged for
// review like in the background path.
func TestUpdateDocumentsHandlerReportsPartialUpdates(t *testing.T) {
	prevFailTag := failTag
	failTag = "paperless-gpt-failed"
	t.Cleanup(func() { failTag = prevFailTag })

	router := setupTestRouter(t)
	client := &partialUpdateClient{}
	app := &App{Client: client}
	router.PATCH("/api/update-documents", app.updateDocumentsHandler)

	body := `[{"id":7,"original_document":{"id":7},"suggested_correspondent":"Hidden Corp","suggested_title":"Bill"}]`
	req, err := http.NewRequest(http.MethodPatch, "/api/update-documents", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Partial struct {
			DocumentID    int      `json:"document_id"`
			DroppedFields []string `json:"dropped_fields"`
			FailTag       string   `json:"fail_tag"`
		} `json:"partial"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, 7, resp.Partial.DocumentID)
	assert.Equal(t, []string{"correspondent"}, resp.Partial.DroppedFields)
	assert.Equal(t, "paperless-gpt-failed", resp.Partial.FailTag)

	require.Len(t, client.calls, 2, "the fail tag is applied in a second update")
	assert.Equal(t, []string{"paperless-gpt-failed"}, client.calls[1][0].SuggestedTags)
}
