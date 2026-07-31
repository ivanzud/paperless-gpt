package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Helper struct to hold common test data and methods
type testEnv struct {
	t             *testing.T
	server        *httptest.Server
	client        *PaperlessClient
	requestCount  int
	mockResponses map[string]http.HandlerFunc
	db            *gorm.DB
}

// newTestEnv initializes a new test environment
func newTestEnv(t *testing.T) *testEnv {
	env := &testEnv{
		t:             t,
		mockResponses: make(map[string]http.HandlerFunc),
	}

	// Initialize test database
	db, err := InitializeTestDB()
	require.NoError(t, err)
	env.db = db

	// Create a mock server with a handler that dispatches based on URL path
	env.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		env.requestCount++
		handler, exists := env.mockResponses[r.URL.Path]
		if !exists {
			t.Fatalf("Unexpected request URL: %s", r.URL.Path)
		}
		// Set common headers and invoke the handler
		assert.Equal(t, "Token test-token", r.Header.Get("Authorization"))
		handler(w, r)
	}))

	// Initialize the PaperlessClient with the mock server URL
	env.client = NewPaperlessClient(env.server.URL, "test-token")
	env.client.HTTPClient = env.server.Client()

	// Add mock response for /api/correspondents/
	env.setMockResponse("/api/correspondents/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"results": [{"id": 1, "name": "Alpha"}, {"id": 2, "name": "Beta"}]}`))
	})

	// Add mock response for /api/document_types/
	env.setMockResponse("/api/document_types/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"results": []}`))
	})

	// Add mock response for /api/custom_fields/
	env.setMockResponse("/api/custom_fields/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"results": []}`))
	})

	return env
}

func InitializeTestDB() (*gorm.DB, error) {
	// Use in-memory SQLite for testing
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		return nil, err
	}

	// Migrate schema
	err = db.AutoMigrate(&ModificationHistory{})
	if err != nil {
		return nil, err
	}

	return db, nil
}

func TestCorrespondentOmitsNilOwner(t *testing.T) {
	payload, err := json.Marshal(instantiateCorrespondent("Test Correspondent"))
	require.NoError(t, err)

	assert.NotContains(t, string(payload), `"owner"`)
	assert.NotContains(t, string(payload), `"set_permissions"`)
}

func TestGetPermissionsDocumentModeCopiesOwnerAndPermissions(t *testing.T) {
	originalMode := objPermissions
	objPermissions = "document"
	t.Cleanup(func() { objPermissions = originalMode })

	doc := Document{Owner: 7}
	doc.Permissions.View.Users = []int{7}
	doc.Permissions.Change.Groups = []int{3}

	client := NewPaperlessClient("http://example.com", "token")
	perms, err := client.GetPermissions(context.Background(), &doc)
	require.NoError(t, err)

	require.NotNil(t, perms.Owner)
	assert.Equal(t, 7, *perms.Owner)
	require.NotNil(t, perms.SetPermissions)
	assert.Equal(t, []int{7}, perms.SetPermissions.View.Users)
	assert.Equal(t, []int{3}, perms.SetPermissions.Change.Groups)
}

func TestCreateTagAppliesObjectPermissions(t *testing.T) {
	env := newTestEnv(t)
	defer env.teardown()

	owner := 7
	perms := &ObjPermissions{
		Owner:          &owner,
		SetPermissions: &SetPermissions{},
	}
	perms.SetPermissions.View.Users = []int{7}
	perms.SetPermissions.Change.Groups = []int{3}

	env.setMockResponse("/api/tags/", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)

		bodyBytes, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		defer r.Body.Close()

		var requestBody TagRequest
		require.NoError(t, json.Unmarshal(bodyBytes, &requestBody))
		assert.Equal(t, "shared-tag", requestBody.Name)
		require.NotNil(t, requestBody.Owner)
		assert.Equal(t, owner, *requestBody.Owner)
		require.NotNil(t, requestBody.SetPermissions)
		assert.Equal(t, []int{7}, requestBody.SetPermissions.View.Users)
		assert.Equal(t, []int{3}, requestBody.SetPermissions.Change.Groups)

		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id": 42}`))
	})

	tagID, err := env.client.CreateTag(context.Background(), "shared-tag", perms)
	require.NoError(t, err)
	assert.Equal(t, 42, tagID)
}

// teardown closes the mock server
func (env *testEnv) teardown() {
	env.server.Close()
}

// Helper method to set a mock response for a specific path
func (env *testEnv) setMockResponse(path string, handler http.HandlerFunc) {
	env.mockResponses[path] = handler
}

// TestNewPaperlessClient tests the creation of a new PaperlessClient instance
func TestNewPaperlessClient(t *testing.T) {
	baseURL := "http://example.com"
	apiToken := "test-token"

	client := NewPaperlessClient(baseURL, apiToken)

	assert.Equal(t, "http://example.com", client.BaseURL)
	assert.Equal(t, apiToken, client.APIToken)
	assert.NotNil(t, client.HTTPClient)
}

// TestDo tests the Do method of PaperlessClient
func TestDo(t *testing.T) {
	env := newTestEnv(t)
	defer env.teardown()

	// Set mock response for "/test-path"
	env.setMockResponse("/test-path", func(w http.ResponseWriter, r *http.Request) {
		// Verify the request method
		assert.Equal(t, "GET", r.Method)
		// Send a mock response
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"message": "success"}`))
	})

	ctx := context.Background()
	resp, err := env.client.Do(ctx, "GET", "/test-path", nil)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, `{"message": "success"}`, string(body))
}

// TestGetAllTags tests the GetAllTags method, including pagination
func TestGetAllTags(t *testing.T) {
	env := newTestEnv(t)
	defer env.teardown()

	// Mock data for paginated responses
	page1 := map[string]interface{}{
		"results": []map[string]interface{}{
			{"id": 1, "name": "tag1"},
			{"id": 2, "name": "tag2"},
		},
		"next": fmt.Sprintf("%s/api/tags/?page=2", env.server.URL),
	}
	page2 := map[string]interface{}{
		"results": []map[string]interface{}{
			{"id": 3, "name": "tag3"},
		},
		"next": nil,
	}

	// Set mock responses for pagination
	env.setMockResponse("/api/tags/", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("page")
		if query == "2" {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(page2)
		} else {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(page1)
		}
	})

	ctx := context.Background()
	tags, err := env.client.GetAllTags(ctx)
	require.NoError(t, err)

	expectedTags := map[string]int{
		"tag1": 1,
		"tag2": 2,
		"tag3": 3,
	}

	assert.Equal(t, expectedTags, tags)
}

// TestGetDocumentCountByTag tests the GetDocumentCountByTag method
func TestGetDocumentCountByTag(t *testing.T) {
	env := newTestEnv(t)
	defer env.teardown()

	// Mock data for paginated responses
	data1 := map[string]interface{}{
		"count": 1,
		"results": []map[string]interface{}{
			{"document_count": 5},
		},
	}

	data2 := map[string]interface{}{
		"count":   0,
		"results": []map[string]interface{}{},
	}

	// Set mock responses for pagination
	env.setMockResponse("/api/tags/", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("name__iexact")
		if query == "available" {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(data1)
		} else {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(data2)
		}
	})

	ctx := context.Background()
	countAvailable, err := env.client.GetDocumentCountByTag(ctx, "available")
	require.NoError(t, err)
	assert.Equal(t, 5, countAvailable)

	countNotAvailable, err := env.client.GetDocumentCountByTag(ctx, "notavailable")
	require.NoError(t, err)
	assert.Equal(t, 0, countNotAvailable)
}

// TestGetDocumentsByTag tests the GetDocumentsByTag method
func TestGetDocumentsByTag(t *testing.T) {
	env := newTestEnv(t)
	defer env.teardown()

	// Mock data for documents
	documentsResponse := GetDocumentsApiResponse{
		Results: []GetDocumentApiResponseResult{
			{
				ID:               1,
				Title:            "Document 1",
				Content:          "Content 1",
				Tags:             []int{1, 2},
				Correspondent:    1,
				CreatedDate:      "1999-09-01",
				OriginalFileName: "invoice-1999.pdf",
			},
			{
				ID:               2,
				Title:            "Document 2",
				Content:          "Content 2",
				Tags:             []int{2, 3},
				Correspondent:    2,
				CreatedDate:      "1999-09-02",
				OriginalFileName: "receipt-1999.pdf",
			},
		},
	}

	// Mock data for tags
	tagsResponse := map[string]interface{}{
		"results": []map[string]interface{}{
			{"id": 1, "name": "tag1"},
			{"id": 2, "name": "tag2"},
			{"id": 3, "name": "tag3"},
		},
		"next": nil,
	}

	// Mock data for tags
	tagsExactResponse := map[string]interface{}{
		"results": []map[string]interface{}{
			{"document_count": 2},
		},
		"count": 1,
	}

	// Set mock responses
	env.setMockResponse("/api/documents/", func(w http.ResponseWriter, r *http.Request) {
		// Verify query parameters
		expectedQuery := "tags__name__iexact=tag2&page_size=25&full_perms=true"
		assert.Equal(t, expectedQuery, r.URL.RawQuery)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(documentsResponse)
	})

	env.setMockResponse("/api/tags/", func(w http.ResponseWriter, r *http.Request) {
		// Handle GetDocumentCountByTag call
		if nameFilter := r.URL.Query().Get("name__iexact"); nameFilter != "" {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(tagsExactResponse)
		} else {
			// Handle GetAllTags call
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(tagsResponse)
		}
	})

	ctx := context.Background()
	tag := "tag2"
	documents, err := env.client.GetDocumentsByTag(ctx, tag, 25)
	require.NoError(t, err)

	expectedDocuments := []Document{
		{
			ID:               1,
			Title:            "Document 1",
			Content:          "Content 1",
			Tags:             []string{"tag1", "tag2"},
			Correspondent:    "Alpha",
			CreatedDate:      "1999-09-01",
			OriginalFileName: "invoice-1999.pdf",
		},
		{
			ID:               2,
			Title:            "Document 2",
			Content:          "Content 2",
			Tags:             []string{"tag2", "tag3"},
			Correspondent:    "Beta",
			CreatedDate:      "1999-09-02",
			OriginalFileName: "receipt-1999.pdf",
		},
	}

	assert.Equal(t, expectedDocuments, documents)
}

// TestGetDocumentsByTagWithEmoji tests the GetDocumentsByTag method with emoji and special characters
func TestGetDocumentsByTagWithEmoji(t *testing.T) {
	env := newTestEnv(t)
	defer env.teardown()

	// Mock data for documents
	documentsResponse := GetDocumentsApiResponse{
		Results: []GetDocumentApiResponseResult{
			{
				ID:            1,
				Title:         "AI Document",
				Content:       "Content about AI",
				Tags:          []int{1},
				Correspondent: 1,
				CreatedDate:   "2024-01-01",
			},
		},
	}

	// Mock data for tags
	tagsResponse := map[string]interface{}{
		"results": []map[string]interface{}{
			{"id": 1, "name": "🤖 AI-Queue"},
		},
		"next": nil,
	}

	// Mock data for exact tag match
	tagsExactResponse := map[string]interface{}{
		"results": []map[string]interface{}{
			{"document_count": 1},
		},
		"count": 1,
	}

	// Set mock responses
	env.setMockResponse("/api/documents/", func(w http.ResponseWriter, r *http.Request) {
		// Verify query parameters - the tag should be URL-encoded
		expectedQuery := fmt.Sprintf("tags__name__iexact=%s&page_size=25&full_perms=true", url.QueryEscape("🤖 AI-Queue"))
		assert.Equal(t, expectedQuery, r.URL.RawQuery)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(documentsResponse)
	})

	env.setMockResponse("/api/tags/", func(w http.ResponseWriter, r *http.Request) {
		// Handle GetDocumentCountByTag call
		if nameFilter := r.URL.Query().Get("name__iexact"); nameFilter != "" {
			// Verify the decoded value matches our emoji tag
			assert.Equal(t, "🤖 AI-Queue", nameFilter)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(tagsExactResponse)
		} else {
			// Handle GetAllTags call
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(tagsResponse)
		}
	})

	ctx := context.Background()
	tag := "🤖 AI-Queue"
	documents, err := env.client.GetDocumentsByTag(ctx, tag, 25)
	require.NoError(t, err)

	expectedDocuments := []Document{
		{
			ID:            1,
			Title:         "AI Document",
			Content:       "Content about AI",
			Tags:          []string{"🤖 AI-Queue"},
			Correspondent: "Alpha",
			CreatedDate:   "2024-01-01",
		},
	}

	assert.Equal(t, expectedDocuments, documents)
}

// TestDownloadPDF tests the DownloadPDF method
func TestDownloadPDF(t *testing.T) {
	env := newTestEnv(t)
	defer env.teardown()

	document := Document{
		ID: 123,
	}

	// Get sample PDF from tests/pdf/sample.pdf
	pdfFile := "tests/pdf/sample.pdf"
	pdfContent, err := os.ReadFile(pdfFile)
	require.NoError(t, err)

	// Set mock response
	downloadPath := fmt.Sprintf("/api/documents/%d/download/", document.ID)
	env.setMockResponse(downloadPath, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(pdfContent)
	})

	ctx := context.Background()
	data, err := env.client.DownloadPDF(ctx, document)
	require.NoError(t, err)
	assert.Equal(t, pdfContent, data)
}

// TestUpdateDocuments tests the UpdateDocuments method
func TestUpdateDocuments(t *testing.T) {
	env := newTestEnv(t)
	defer env.teardown()

	// Mock data for documents to update
	documents := []DocumentSuggestion{
		{
			ID: 1,
			OriginalDocument: Document{
				ID:          1,
				Title:       "Old Title",
				Tags:        []string{"tag1", "tag3", "manual", "removeMe"},
				CreatedDate: "1999-09-01",
			},
			SuggestedTitle:       "New Title",
			SuggestedTags:        []string{"tag2", "tag3"},
			RemoveTags:           []string{"removeMe"},
			SuggestedCreatedDate: "1999-09-02",
		},
	}
	idTag1 := 1
	idTag2 := 2
	idTag3 := 4
	// Mock data for tags
	tagsResponse := map[string]interface{}{
		"results": []map[string]interface{}{
			{"id": idTag1, "name": "tag1"},
			{"id": idTag2, "name": "tag2"},
			{"id": 3, "name": "manual"},
			{"id": idTag3, "name": "tag3"},
			{"id": 5, "name": "removeMe"},
		},
		"next": nil,
	}

	// Set the manual tag
	manualTag = "manual"

	// Set mock responses
	env.setMockResponse("/api/tags/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(tagsResponse)
	})

	updatePath := fmt.Sprintf("/api/documents/%d/", documents[0].ID)
	env.setMockResponse(updatePath, func(w http.ResponseWriter, r *http.Request) {
		// Verify the request method
		assert.Equal(t, "PATCH", r.Method)

		// Read and parse the request body
		bodyBytes, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		defer r.Body.Close()

		var updatedFields map[string]interface{}
		err = json.Unmarshal(bodyBytes, &updatedFields)
		require.NoError(t, err)

		// Expected updated fields
		expectedFields := map[string]interface{}{
			"title": "New Title",
			// do not keep previous tags since the tag generation will already take care to include old ones:
			"tags":         []interface{}{float64(idTag2), float64(idTag3)},
			"created_date": "1999-09-02",
		}

		assert.Equal(t, expectedFields, updatedFields)

		w.WriteHeader(http.StatusOK)
	})

	ctx := context.Background()
	err := env.client.UpdateDocuments(ctx, documents, env.db, false)
	require.NoError(t, err)
}

func TestUpdateDocuments_NormalizesMonetaryCustomFields(t *testing.T) {
	env := newTestEnv(t)
	defer env.teardown()

	env.setMockResponse("/api/tags/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"results":[]}`))
	})
	env.setMockResponse("/api/custom_fields/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"results":[{"id":77,"name":"Amount","data_type":"monetary"},{"id":88,"name":"Reference","data_type":"string"}]}`))
	})

	document := DocumentSuggestion{
		ID: 1,
		OriginalDocument: Document{
			ID:    1,
			Title: "Invoice",
		},
		CustomFieldsWriteMode: "replace",
		SuggestedCustomFields: []CustomFieldSuggestion{
			{ID: 77, Name: "Amount", Value: "USD1,053.52"},
			{ID: 88, Name: "Reference", Value: "USD1,053.52"},
		},
	}

	env.setMockResponse("/api/documents/1/", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPatch, r.Method)

		bodyBytes, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		defer r.Body.Close()

		var payload struct {
			CustomFields []CustomFieldResponse `json:"custom_fields"`
		}
		err = json.Unmarshal(bodyBytes, &payload)
		require.NoError(t, err)
		require.Len(t, payload.CustomFields, 2)

		assert.Equal(t, 77, payload.CustomFields[0].Field)
		assert.Equal(t, "USD1053.52", payload.CustomFields[0].Value)
		assert.Equal(t, 88, payload.CustomFields[1].Field)
		assert.Equal(t, "USD1,053.52", payload.CustomFields[1].Value)

		w.WriteHeader(http.StatusOK)
	})

	err := env.client.UpdateDocuments(context.Background(), []DocumentSuggestion{document}, env.db, false)
	require.NoError(t, err)
}

func TestUpdateDocuments_DropsInvalidCreatedDateBeforePatch(t *testing.T) {
	env := newTestEnv(t)
	defer env.teardown()

	env.setMockResponse("/api/tags/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"results":[]}`))
	})

	document := DocumentSuggestion{
		ID: 1,
		OriginalDocument: Document{
			ID:          1,
			Title:       "Old Title",
			CreatedDate: "2023-01-01",
		},
		SuggestedTitle:       "New Title",
		SuggestedCreatedDate: "2023-01-79",
	}

	env.setMockResponse("/api/documents/1/", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPatch, r.Method)

		bodyBytes, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		defer r.Body.Close()

		var payload map[string]interface{}
		err = json.Unmarshal(bodyBytes, &payload)
		require.NoError(t, err)

		assert.Equal(t, "New Title", payload["title"])
		assert.NotContains(t, payload, "created_date")

		w.WriteHeader(http.StatusOK)
	})

	err := env.client.UpdateDocuments(context.Background(), []DocumentSuggestion{document}, env.db, false)
	var partial *PartialUpdateError
	require.True(t, errors.As(err, &partial))
	assert.Equal(t, 1, partial.DocumentID)
	assert.Equal(t, []string{"created_date"}, partial.DroppedFields)
}

func TestUpdateDocuments_StripsRejectedCustomFieldAndRetries(t *testing.T) {
	env := newTestEnv(t)
	defer env.teardown()

	env.setMockResponse("/api/tags/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"results":[]}`))
	})
	env.setMockResponse("/api/custom_fields/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"results":[{"id":77,"name":"Valid","data_type":"string"},{"id":88,"name":"Invalid","data_type":"string"}]}`))
	})

	document := DocumentSuggestion{
		ID: 1,
		OriginalDocument: Document{
			ID:    1,
			Title: "Old Title",
		},
		SuggestedTitle:        "New Title",
		CustomFieldsWriteMode: "replace",
		SuggestedCustomFields: []CustomFieldSuggestion{
			{ID: 77, Name: "Valid", Value: "keep"},
			{ID: 88, Name: "Invalid", Value: "drop"},
		},
	}

	patchCalls := 0
	env.setMockResponse("/api/documents/1/", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPatch, r.Method)
		patchCalls++

		bodyBytes, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		defer r.Body.Close()

		var payload struct {
			Title        string                `json:"title"`
			CustomFields []CustomFieldResponse `json:"custom_fields"`
		}
		err = json.Unmarshal(bodyBytes, &payload)
		require.NoError(t, err)

		if patchCalls == 1 {
			require.Len(t, payload.CustomFields, 2)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"custom_fields":[{},{"value":["Invalid custom field value"]}]}`))
			return
		}

		assert.Equal(t, "New Title", payload.Title)
		require.Len(t, payload.CustomFields, 1)
		assert.Equal(t, 77, payload.CustomFields[0].Field)
		assert.Equal(t, "keep", payload.CustomFields[0].Value)
		w.WriteHeader(http.StatusOK)
	})

	err := env.client.UpdateDocuments(context.Background(), []DocumentSuggestion{document}, env.db, false)
	var partial *PartialUpdateError
	require.True(t, errors.As(err, &partial))
	assert.Equal(t, []string{"custom_fields[1](field_id=88)"}, partial.DroppedFields)
	assert.Equal(t, 2, patchCalls)
}

func TestUpdateDocuments_CreatesMissingSystemTag(t *testing.T) {
	env := newTestEnv(t)
	defer env.teardown()

	autoOcrTag = "paperless-gpt-ocr-auto"
	pdfOCRCompleteTag = "paperless-gpt-ocr-complete"

	documents := []DocumentSuggestion{
		{
			ID: 1,
			OriginalDocument: Document{
				ID:    1,
				Title: "Doc for OCR",
				Tags:  []string{autoOcrTag},
			},
			SuggestedTags:    []string{pdfOCRCompleteTag},
			KeepOriginalTags: true,
			RemoveTags:       []string{autoOcrTag},
		},
	}

	env.setMockResponse("/api/tags/", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"results":[{"id":11,"name":"paperless-gpt-ocr-auto"}]}`))
		case http.MethodPost:
			bodyBytes, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			defer r.Body.Close()

			var requestBody map[string]string
			err = json.Unmarshal(bodyBytes, &requestBody)
			require.NoError(t, err)
			assert.Equal(t, pdfOCRCompleteTag, requestBody["name"])

			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":42}`))
		default:
			t.Fatalf("Unexpected method for /api/tags/: %s", r.Method)
		}
	})

	updatePath := fmt.Sprintf("/api/documents/%d/", documents[0].ID)
	env.setMockResponse(updatePath, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPatch, r.Method)

		bodyBytes, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		defer r.Body.Close()

		var updatedFields map[string]interface{}
		err = json.Unmarshal(bodyBytes, &updatedFields)
		require.NoError(t, err)

		assert.Equal(t, map[string]interface{}{
			"tags": []interface{}{float64(42)},
		}, updatedFields)

		w.WriteHeader(http.StatusOK)
	})

	ctx := context.Background()
	err := env.client.UpdateDocuments(ctx, documents, env.db, false)
	require.NoError(t, err)
}

func TestUpdateDocumentsAppliesAutoTagComplete(t *testing.T) {
	previousAutoTagComplete := autoTagComplete
	previousCreateNewTags := createNewTags
	settingsMutex.RLock()
	previousSettings := settings
	settingsMutex.RUnlock()
	t.Cleanup(func() {
		autoTagComplete = previousAutoTagComplete
		createNewTags = previousCreateNewTags
		settingsMutex.Lock()
		settings = previousSettings
		settingsMutex.Unlock()
	})

	autoTagComplete = "paperless-gpt-auto-complete"
	createNewTags = false
	settingsMutex.Lock()
	settings.TagsAutoCreate = false
	settingsMutex.Unlock()

	for _, testCase := range []struct {
		name           string
		existingTag    bool
		existingName   string
		expectedTagIDs []int
		expectedPosts  int
	}{
		{
			name:           "uses existing tag case-insensitively",
			existingTag:    true,
			existingName:   "Paperless-GPT-Auto-Complete",
			expectedTagIDs: []int{10, 20},
		},
		{
			name:           "creates missing protected tag with auto creation disabled",
			expectedTagIDs: []int{10, 20},
			expectedPosts:  1,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			env := newTestEnv(t)
			defer env.teardown()

			postCalls := 0
			env.setMockResponse("/api/tags/", func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodGet:
					results := []map[string]interface{}{{"id": 10, "name": "invoice"}}
					if testCase.existingTag {
						results = append(results, map[string]interface{}{"id": 20, "name": testCase.existingName})
					}
					w.WriteHeader(http.StatusOK)
					require.NoError(t, json.NewEncoder(w).Encode(map[string]interface{}{"results": results}))
				case http.MethodPost:
					postCalls++
					var payload TagRequest
					require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
					assert.Equal(t, autoTagComplete, payload.Name)
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write([]byte(`{"id":20}`))
				default:
					t.Fatalf("unexpected tags method: %s", r.Method)
				}
			})

			env.setMockResponse("/api/documents/1/", func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodPatch, r.Method)
				var payload struct {
					Tags []int `json:"tags"`
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
				assert.Equal(t, testCase.expectedTagIDs, payload.Tags)
				w.WriteHeader(http.StatusOK)
			})

			document := DocumentSuggestion{
				ID:               1,
				OriginalDocument: Document{ID: 1, Title: "Invoice", Tags: []string{"invoice"}},
				AddTags:          []string{strings.ToUpper(autoTagComplete)},
			}

			err := env.client.UpdateDocuments(context.Background(), []DocumentSuggestion{document}, env.db, false)
			require.NoError(t, err)
			assert.Equal(t, testCase.expectedPosts, postCalls)

			var tagHistory ModificationHistory
			require.NoError(t, env.db.
				Where("document_id = ? AND mod_field = ?", document.ID, "tags").
				Order("id DESC").
				First(&tagHistory).Error)
			var previousTags []string
			var newTags []string
			require.NoError(t, json.Unmarshal([]byte(tagHistory.PreviousValue), &previousTags))
			require.NoError(t, json.Unmarshal([]byte(tagHistory.NewValue), &newTags))
			assert.Equal(t, []string{"invoice"}, previousTags)
			expectedCompleteName := autoTagComplete
			if testCase.existingTag {
				expectedCompleteName = testCase.existingName
			}
			assert.ElementsMatch(t, []string{"invoice", expectedCompleteName}, newTags)
		})
	}
}

func TestParseCreatedDocumentNote(t *testing.T) {
	for _, testCase := range []struct {
		name string
		body string
		id   int
	}{
		{
			name: "list selects newest matching note",
			body: `[{"id":2,"note":"Reviewed summary"},{"id":77,"note":"Reviewed summary"},{"id":90,"note":"Other"}]`,
			id:   77,
		},
		{
			name: "notes envelope",
			body: `{"notes":[{"id":78,"note":"Reviewed summary"}]}`,
			id:   78,
		},
		{
			name: "results envelope",
			body: `{"results":[{"id":79,"note":"Reviewed summary"}]}`,
			id:   79,
		},
		{
			name: "single note",
			body: `{"id":80,"note":"Reviewed summary"}`,
			id:   80,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			note, err := parseCreatedDocumentNote([]byte(testCase.body), "Reviewed summary")
			require.NoError(t, err)
			assert.Equal(t, testCase.id, note.ID)
			assert.Equal(t, "Reviewed summary", note.Note)
		})
	}

	_, err := parseCreatedDocumentNote([]byte(`[{"id":77,"note":"Different"}]`), "Reviewed summary")
	require.Error(t, err)
}

func TestUpdateDocumentsAppliesSuggestedSummaryAsNote(t *testing.T) {
	env := newTestEnv(t)
	defer env.teardown()
	const documentID = 901

	env.setMockResponse("/api/tags/", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"results":[]}`))
	})
	env.setMockResponse("/api/documents/901/notes/", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		var payload struct {
			Note string `json:"note"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		assert.Equal(t, "Reviewed summary", payload.Note)
		assert.Empty(t, r.URL.Query())
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[{"id":2,"note":"Existing note"},{"id":77,"note":"Reviewed summary"}]`))
	})

	document := DocumentSuggestion{
		ID:               documentID,
		OriginalDocument: Document{ID: documentID, Title: "Invoice"},
		SuggestedSummary: "Reviewed summary",
	}

	err := env.client.UpdateDocuments(context.Background(), []DocumentSuggestion{document}, env.db, false)
	require.NoError(t, err)

	var modification ModificationHistory
	require.NoError(t, env.db.Where("document_id = ? AND mod_field = ?", documentID, "summary").First(&modification).Error)
	assert.Empty(t, modification.PreviousValue)
	assert.Equal(t, "Reviewed summary", modification.NewValue)
	require.NotNil(t, modification.RemoteID)
	assert.Equal(t, 77, *modification.RemoteID)
}

func TestUpdateDocumentsSummaryPostFailureDoesNotCreateHistory(t *testing.T) {
	env := newTestEnv(t)
	defer env.teardown()
	const documentID = 902

	env.setMockResponse("/api/tags/", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"results":[]}`))
	})
	env.setMockResponse("/api/documents/902/notes/", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"detail":"sensitive document content"}`))
	})

	document := DocumentSuggestion{
		ID:               documentID,
		OriginalDocument: Document{ID: documentID, Title: "Invoice"},
		SuggestedSummary: "Reviewed summary",
	}

	err := env.client.UpdateDocuments(context.Background(), []DocumentSuggestion{document}, env.db, false)
	require.Error(t, err)
	var partial *PartialUpdateError
	assert.False(t, errors.As(err, &partial))
	assert.NotContains(t, err.Error(), "sensitive document content")

	var count int64
	require.NoError(t, env.db.Model(&ModificationHistory{}).Where("document_id = ? AND mod_field = ?", documentID, "summary").Count(&count).Error)
	assert.Zero(t, count)
}

func TestUpdateDocumentsReturnsPartialWhenSummaryFailsAfterMetadata(t *testing.T) {
	env := newTestEnv(t)
	defer env.teardown()
	const documentID = 903

	env.setMockResponse("/api/tags/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"results":[]}`))
	})
	env.setMockResponse("/api/documents/903/", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPatch, r.Method)
		var payload map[string]interface{}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		assert.Equal(t, "Updated invoice", payload["title"])
		w.WriteHeader(http.StatusOK)
	})
	env.setMockResponse("/api/documents/903/notes/", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		w.WriteHeader(http.StatusBadGateway)
	})

	document := DocumentSuggestion{
		ID:               documentID,
		OriginalDocument: Document{ID: documentID, Title: "Invoice"},
		SuggestedTitle:   "Updated invoice",
		SuggestedSummary: "Reviewed summary",
	}

	err := env.client.UpdateDocuments(context.Background(), []DocumentSuggestion{document}, env.db, false)
	var partial *PartialUpdateError
	require.True(t, errors.As(err, &partial))
	assert.Equal(t, documentID, partial.DocumentID)
	assert.Equal(t, []string{"summary"}, partial.DroppedFields)

	var titleCount int64
	require.NoError(t, env.db.Model(&ModificationHistory{}).Where("document_id = ? AND mod_field = ?", documentID, "title").Count(&titleCount).Error)
	assert.EqualValues(t, 1, titleCount)

	var summaryCount int64
	require.NoError(t, env.db.Model(&ModificationHistory{}).Where("document_id = ? AND mod_field = ?", documentID, "summary").Count(&summaryCount).Error)
	assert.Zero(t, summaryCount)
}

func TestUpdateDocumentsAddsSummaryAfterMetadata(t *testing.T) {
	env := newTestEnv(t)
	defer env.teardown()
	const documentID = 904

	env.setMockResponse("/api/tags/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"results":[]}`))
	})

	var requestOrder []string
	env.setMockResponse("/api/documents/904/", func(w http.ResponseWriter, r *http.Request) {
		requestOrder = append(requestOrder, "metadata")
		assert.Equal(t, http.MethodPatch, r.Method)
		w.WriteHeader(http.StatusOK)
	})
	env.setMockResponse("/api/documents/904/notes/", func(w http.ResponseWriter, r *http.Request) {
		requestOrder = append(requestOrder, "summary")
		assert.Equal(t, http.MethodPost, r.Method)

		var titleHistoryCount int64
		require.NoError(t, env.db.Model(&ModificationHistory{}).
			Where("document_id = ? AND mod_field = ?", documentID, "title").
			Count(&titleHistoryCount).Error)
		assert.EqualValues(t, 1, titleHistoryCount)

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[{"id":91,"note":"Reviewed summary"}]`))
	})

	document := DocumentSuggestion{
		ID:               documentID,
		OriginalDocument: Document{ID: documentID, Title: "Invoice"},
		SuggestedTitle:   "Updated invoice",
		SuggestedSummary: "Reviewed summary",
	}

	err := env.client.UpdateDocuments(context.Background(), []DocumentSuggestion{document}, env.db, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"metadata", "summary"}, requestOrder)

	var summary ModificationHistory
	require.NoError(t, env.db.Where("document_id = ? AND mod_field = ?", documentID, "summary").First(&summary).Error)
	require.NotNil(t, summary.RemoteID)
	assert.Equal(t, 91, *summary.RemoteID)
}

func TestUpdateDocumentsAddsSummaryWhenAllMetadataIsDropped(t *testing.T) {
	env := newTestEnv(t)
	defer env.teardown()
	const documentID = 905

	env.setMockResponse("/api/tags/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"results":[]}`))
	})
	env.setMockResponse("/api/documents/905/notes/", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[{"id":92,"note":"Reviewed summary"}]`))
	})

	document := DocumentSuggestion{
		ID:                   documentID,
		OriginalDocument:     Document{ID: documentID, Title: "Invoice", CreatedDate: "2024-01-01"},
		SuggestedCreatedDate: "2024-99-99",
		SuggestedSummary:     "Reviewed summary",
	}

	err := env.client.UpdateDocuments(context.Background(), []DocumentSuggestion{document}, env.db, false)
	var partial *PartialUpdateError
	require.True(t, errors.As(err, &partial))
	assert.Equal(t, []string{"created_date"}, partial.DroppedFields)

	var summary ModificationHistory
	require.NoError(t, env.db.Where("document_id = ? AND mod_field = ?", documentID, "summary").First(&summary).Error)
	require.NotNil(t, summary.RemoteID)
	assert.Equal(t, 92, *summary.RemoteID)
}

func TestUpdateDocumentsReportsLocallyDroppedFieldWithoutSummary(t *testing.T) {
	env := newTestEnv(t)
	defer env.teardown()
	const documentID = 909

	env.setMockResponse("/api/tags/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"results":[]}`))
	})

	document := DocumentSuggestion{
		ID:                   documentID,
		OriginalDocument:     Document{ID: documentID, Title: "Invoice", CreatedDate: "2024-01-01"},
		SuggestedCreatedDate: "2024-99-99",
	}

	err := env.client.UpdateDocuments(context.Background(), []DocumentSuggestion{document}, env.db, false)
	var partial *PartialUpdateError
	require.True(t, errors.As(err, &partial))
	assert.Equal(t, documentID, partial.DocumentID)
	assert.Equal(t, []string{"created_date"}, partial.DroppedFields)
}

func TestUpdateDocumentsReportsAllFieldsRejectedWithoutSummary(t *testing.T) {
	env := newTestEnv(t)
	defer env.teardown()
	const documentID = 910

	env.setMockResponse("/api/tags/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"results":[]}`))
	})
	env.setMockResponse("/api/documents/910/", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPatch, r.Method)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"title":["Invalid title"]}`))
	})

	document := DocumentSuggestion{
		ID:               documentID,
		OriginalDocument: Document{ID: documentID, Title: "Invoice"},
		SuggestedTitle:   "Rejected title",
	}

	err := env.client.UpdateDocuments(context.Background(), []DocumentSuggestion{document}, env.db, false)
	var partial *PartialUpdateError
	require.True(t, errors.As(err, &partial))
	assert.Equal(t, documentID, partial.DocumentID)
	assert.Equal(t, []string{"title"}, partial.DroppedFields)
}

func TestCreateDocumentNoteRejectsInvalidSuccessResponse(t *testing.T) {
	env := newTestEnv(t)
	defer env.teardown()

	env.setMockResponse("/api/documents/906/notes/", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[{"id":93,"note":"Different note"}]`))
	})

	_, err := env.client.CreateDocumentNote(context.Background(), 906, "Reviewed summary")
	require.Error(t, err)
}

func TestSummaryHistoryFailureDeletesCreatedNote(t *testing.T) {
	env := newTestEnv(t)
	defer env.teardown()
	const documentID = 907

	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "history.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&ModificationHistory{}))
	require.NoError(t, db.Exec(`
		CREATE TRIGGER reject_summary_history
		BEFORE INSERT ON modification_histories
		BEGIN
			SELECT RAISE(FAIL, 'history unavailable');
		END;
	`).Error)

	var requestOrder []string
	env.setMockResponse("/api/documents/907/notes/", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			requestOrder = append(requestOrder, "create")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[{"id":94,"note":"Reviewed summary"}]`))
		case http.MethodDelete:
			requestOrder = append(requestOrder, "delete")
			assert.Equal(t, "94", r.URL.Query().Get("id"))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		default:
			t.Fatalf("unexpected method: %s", r.Method)
		}
	})

	err = env.client.addSummaryNoteAndHistory(context.Background(), documentID, "Reviewed summary", db)
	require.Error(t, err)
	assert.Equal(t, []string{"create", "delete"}, requestOrder)
}

func TestDeleteDocumentNoteAcceptsPaperlessResponse(t *testing.T) {
	env := newTestEnv(t)
	defer env.teardown()

	env.setMockResponse("/api/documents/908/notes/", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		assert.Equal(t, "95", r.URL.Query().Get("id"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[{"id":1,"note":"Unrelated note"}]`))
	})

	require.NoError(t, env.client.DeleteDocumentNote(context.Background(), 908, 95))
}

func TestDeleteDocumentNoteRejectsNon2xx(t *testing.T) {
	env := newTestEnv(t)
	defer env.teardown()

	env.setMockResponse("/api/documents/1/notes/", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		assert.Equal(t, "77", r.URL.Query().Get("id"))
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"detail":"sensitive document content"}`))
	})

	err := env.client.DeleteDocumentNote(context.Background(), 1, 77)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "sensitive document content")
}

// TestUpdateDocuments_RemovingLastTag tests the behavior when removing the last remaining tag
// from a document, which Paperless-NGX REST API does not allow (empty tags array is rejected).
// The test covers two scenarios:
//  1. Document has only the manual tag with other field changes (title) - should update title first,
//     then remove the manual tag in a separate call
//  2. Document has only the manual tag with NO other changes - should skip the update entirely
func TestUpdateDocuments_RemovingLastTag(t *testing.T) {
	// in this scenario, the manualTag is set, but the
	// document processing sends both the auto and manual
	// versions of the tag to be removed. this is why you'll
	// see the autoTag included in the RemoveTags but not in the original document.
	manualTag = "paperless-gpt"
	autoTag = "paperless-gpt-auto"

	tests := []struct {
		name              string
		document          DocumentSuggestion
		expectUpdateCalls int
		validateCalls     func(t *testing.T, calls []map[string]interface{})
	}{
		{
			name: "with_other_field_changes",
			document: DocumentSuggestion{
				ID: 1,
				OriginalDocument: Document{
					ID:          1,
					Title:       "Old Title",
					Tags:        []string{manualTag},
					CreatedDate: "1999-09-01",
				},
				SuggestedTitle: "New Title",
				SuggestedTags:  []string{},
				RemoveTags:     []string{manualTag, autoTag},
			},
			expectUpdateCalls: 2,
			validateCalls: func(t *testing.T, calls []map[string]interface{}) {
				// First call: should update title but NOT tags
				assert.Equal(t, map[string]interface{}{"title": "New Title"}, calls[0],
					"First call should only update title, not tags")

				// Second call: should remove the manual tag with empty array
				tagsValue, tagsPresent := calls[1]["tags"]
				require.True(t, tagsPresent, "Second call must include tags field")
				tagSlice, ok := tagsValue.([]interface{})
				require.True(t, ok, "tags should be an array")
				assert.Empty(t, tagSlice, "tags array should be empty to remove manual tag")
			},
		},
		{
			name: "no_other_changes",
			document: DocumentSuggestion{
				ID: 2,
				OriginalDocument: Document{
					ID:          2,
					Title:       "Same Title",
					Tags:        []string{manualTag},
					CreatedDate: "1999-09-01",
				},
				SuggestedTitle: "",
				SuggestedTags:  []string{},
				RemoveTags:     []string{manualTag, autoTag},
			},
			expectUpdateCalls: 1,
			validateCalls: func(t *testing.T, calls []map[string]interface{}) {
				// Should make one call to remove the manual tag with empty array
				// Even though there are no other field changes, the manual tag MUST be removed
				tagsValue, tagsPresent := calls[0]["tags"]
				require.True(t, tagsPresent, "Must include tags field to remove manual tag")
				tagSlice, ok := tagsValue.([]interface{})
				require.True(t, ok, "tags should be an array")
				assert.Empty(t, tagSlice, "tags array should be empty to remove manual tag")
			},
		},
		{
			name: "case_insensitive_queue_tag_with_no_other_changes",
			document: DocumentSuggestion{
				ID: 3,
				OriginalDocument: Document{
					ID:          3,
					Title:       "Same Title",
					Tags:        []string{strings.ToUpper(manualTag)},
					CreatedDate: "1999-09-01",
				},
				RemoveTags: []string{manualTag, autoTag},
			},
			expectUpdateCalls: 1,
			validateCalls: func(t *testing.T, calls []map[string]interface{}) {
				tagsValue, tagsPresent := calls[0]["tags"]
				require.True(t, tagsPresent, "Must include tags field to remove case-variant queue tag")
				tagSlice, ok := tagsValue.([]interface{})
				require.True(t, ok, "tags should be an array")
				assert.Empty(t, tagSlice)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newTestEnv(t)
			defer env.teardown()

			// Mock tags response
			env.setMockResponse("/api/tags/", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"results": []map[string]interface{}{
						{"id": 1, "name": "paperless-gpt"},
					},
					"next": nil,
				})
			})

			// Track update calls (PATCH only, not GET)
			var updateCalls []map[string]interface{}
			updatePath := fmt.Sprintf("/api/documents/%d/", tt.document.ID)

			env.setMockResponse(updatePath, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					// Return document state after first update (still has paperless-gpt tag)
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"id":                 tt.document.ID,
						"title":              "New Title", // Title was updated
						"tags":               []int{1},    // Still has paperless-gpt tag
						"created_date":       tt.document.OriginalDocument.CreatedDate,
						"content":            "",
						"correspondent":      nil,
						"custom_fields":      []interface{}{},
						"original_file_name": "test.pdf",
						"document_type":      nil,
					})
					return
				}

				// Track PATCH calls
				assert.Equal(t, "PATCH", r.Method)
				bodyBytes, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				defer r.Body.Close()

				var updatedFields map[string]interface{}
				err = json.Unmarshal(bodyBytes, &updatedFields)
				require.NoError(t, err)

				updateCalls = append(updateCalls, updatedFields)
				w.WriteHeader(http.StatusOK)
			})

			ctx := context.Background()
			err := env.client.UpdateDocuments(ctx, []DocumentSuggestion{tt.document}, env.db, false)
			require.NoError(t, err)

			assert.Len(t, updateCalls, tt.expectUpdateCalls,
				"Expected %d update calls, got %d", tt.expectUpdateCalls, len(updateCalls))

			if tt.expectUpdateCalls > 0 {
				tt.validateCalls(t, updateCalls)
			}

			var tagHistory ModificationHistory
			require.NoError(t, env.db.
				Where("document_id = ? AND mod_field = ?", tt.document.ID, "tags").
				Order("id DESC").
				First(&tagHistory).Error)
			var previousTags []string
			var newTags []string
			require.NoError(t, json.Unmarshal([]byte(tagHistory.PreviousValue), &previousTags))
			require.NoError(t, json.Unmarshal([]byte(tagHistory.NewValue), &newTags))
			assert.Equal(t, tt.document.OriginalDocument.Tags, previousTags)
			assert.Empty(t, newTags)
		})
	}
}

// TestUrlEncode tests the urlEncode function
func TestUrlEncode(t *testing.T) {
	input := "tag:tag1 tag:tag2"
	expected := "tag:tag1+tag:tag2"
	result := urlEncode(input)
	assert.Equal(t, expected, result)
}

// TestDownloadDocumentAsImages tests the DownloadDocumentAsImages method
func TestDownloadDocumentAsImages(t *testing.T) {
	env := newTestEnv(t)
	defer env.teardown()

	document := Document{
		ID: 123,
	}

	// Get sample PDF from tests/pdf/sample.pdf
	pdfFile := "tests/pdf/sample.pdf"
	pdfContent, err := os.ReadFile(pdfFile)
	require.NoError(t, err)

	// Set mock response
	downloadPath := fmt.Sprintf("/api/documents/%d/download/", document.ID)
	env.setMockResponse(downloadPath, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(pdfContent)
	})

	ctx := context.Background()
	imagePaths, totalPages, err := env.client.DownloadDocumentAsImages(ctx, document.ID, 0)
	require.NoError(t, err)

	// Verify that exatly one page was extracted
	assert.Len(t, imagePaths, 1)
	// The path shall end with paperless-gpt/document-123/page000.jpg
	assert.Contains(t, imagePaths[0], "paperless-gpt/document-123/page000.jpg")
	for _, imagePath := range imagePaths {
		_, err := os.Stat(imagePath)
		assert.NoError(t, err)
	}

	// Verify total pages count
	assert.Equal(t, 1, totalPages, "Total pages should be 1")
}

func TestDownloadDocumentAsImages_ManyPages(t *testing.T) {
	env := newTestEnv(t)
	defer env.teardown()

	document := Document{
		ID: 321,
	}

	// Get sample PDF from tests/pdf/many-pages.pdf
	pdfFile := "tests/pdf/many-pages.pdf"
	pdfContent, err := os.ReadFile(pdfFile)
	require.NoError(t, err)

	// Set mock response
	downloadPath := fmt.Sprintf("/api/documents/%d/download/", document.ID)
	env.setMockResponse(downloadPath, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(pdfContent)
	})

	ctx := context.Background()
	env.client.CacheFolder = "tests/tmp"
	// Clean the cache folder
	os.RemoveAll(env.client.CacheFolder)
	imagePaths, totalPages, err := env.client.DownloadDocumentAsImages(ctx, document.ID, 50)
	require.NoError(t, err)

	// Verify that exactly 50 pages were extracted - the original doc contains 52 pages
	assert.Len(t, imagePaths, 50)
	// The path shall end with tests/tmp/document-321/page000.jpg
	for _, imagePath := range imagePaths {
		_, err := os.Stat(imagePath)
		assert.NoError(t, err)
		assert.Contains(t, imagePath, "tests/tmp/document-321/page")
	}

	// Verify total pages count
	assert.Equal(t, 52, totalPages, "Total pages should be 52")
}

// TestDownloadDocumentAsPDF tests the DownloadDocumentAsPDF method
func TestDownloadDocumentAsPDF(t *testing.T) {
	env := newTestEnv(t)
	defer env.teardown()

	documentID := 123

	// Get sample PDF from tests/pdf/sample.pdf
	pdfFile := "tests/pdf/sample.pdf"
	pdfContent, err := os.ReadFile(pdfFile)
	require.NoError(t, err)

	// Set mock response
	downloadPath := fmt.Sprintf("/api/documents/%d/download/", documentID)
	env.setMockResponse(downloadPath, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(pdfContent)
	})

	ctx := context.Background()

	// Test without PDF splitting
	pdfPaths, pdfData, totalPages, err := env.client.DownloadDocumentAsPDF(ctx, documentID, 0, false)
	require.NoError(t, err)
	assert.Empty(t, pdfPaths, "No paths should be returned when split=false")
	assert.Equal(t, pdfContent, pdfData)
	assert.Equal(t, 1, totalPages)

	// Testing with splitting=true would be more complex so we'll skip that for simplicity
}

func TestDownloadDocumentAsPDF_SplitWithPageLimit(t *testing.T) {
	env := newTestEnv(t)
	defer env.teardown()

	const documentID = 456
	pdfContent, err := os.ReadFile("tests/pdf/five-pager.pdf")
	require.NoError(t, err)

	downloadPath := fmt.Sprintf("/api/documents/%d/download/", documentID)
	env.setMockResponse(downloadPath, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, writeErr := w.Write(pdfContent)
		assert.NoError(t, writeErr)
	})

	env.client.CacheFolder = t.TempDir()
	const limitPages = 2
	pdfPaths, _, totalPages, err := env.client.DownloadDocumentAsPDF(
		context.Background(),
		documentID,
		limitPages,
		true,
	)
	require.NoError(t, err)
	assert.Equal(t, 5, totalPages)
	assert.Len(t, pdfPaths, limitPages)

	for _, pdfPath := range pdfPaths {
		_, err := os.Stat(pdfPath)
		assert.NoError(t, err)
	}

	docDir := filepath.Join(env.client.CacheFolder, fmt.Sprintf("document-%d-pdf", documentID))
	entries, err := os.ReadDir(docDir)
	require.NoError(t, err)
	splitFileCount := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "original_") && strings.HasSuffix(entry.Name(), ".pdf") {
			splitFileCount++
		}
	}
	assert.Equal(t, limitPages, splitFileCount, "the page limit must bound split output")
}

func TestGetSimilarDocuments(t *testing.T) {
	env := newTestEnv(t)
	defer env.teardown()

	// Mock response for similar documents API
	similarDocs := []GetDocumentApiResponseResult{
		{
			ID:    2,
			Title: "Invoice January 2023 - Company ABC",
		},
		{
			ID:    3,
			Title: "Invoice February 2023 - Company ABC",
		},
		{
			ID:    4,
			Title: "Receipt March 2023 - Company XYZ",
		},
	}

	response := GetDocumentsApiResponse{
		Count:   3,
		Results: similarDocs,
	}

	env.mockResponses["/api/documents/"] = func(w http.ResponseWriter, r *http.Request) {
		// Verify query parameters
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "-score", r.URL.Query().Get("ordering"))
		assert.Equal(t, "true", r.URL.Query().Get("truncate_content"))
		assert.Equal(t, "1", r.URL.Query().Get("more_like_id"))
		assert.Equal(t, "5", r.URL.Query().Get("page_size"))

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(response)
	}

	// Add required mocks for tags and correspondents that GetSimilarDocuments calls
	env.setMockResponse("/api/tags/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"results": []map[string]interface{}{
				{"id": 1, "name": "tag1"},
			},
			"next": nil,
		})
	})

	// Test successful case
	ctx := context.Background()
	documents, err := env.client.GetSimilarDocuments(ctx, 1, 5)
	require.NoError(t, err)
	assert.Len(t, documents, 3)
	assert.Equal(t, "Invoice January 2023 - Company ABC", documents[0].Title)
	assert.Equal(t, "Invoice February 2023 - Company ABC", documents[1].Title)
	assert.Equal(t, "Receipt March 2023 - Company XYZ", documents[2].Title)
}

func TestGetSimilarDocuments_NoResults(t *testing.T) {
	env := newTestEnv(t)
	defer env.teardown()

	// Mock response with no results
	response := GetDocumentsApiResponse{
		Count:   0,
		Results: []GetDocumentApiResponseResult{},
	}

	env.mockResponses["/api/documents/"] = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(response)
	}

	// Add required mocks for tags and correspondents
	env.setMockResponse("/api/tags/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"results": []map[string]interface{}{},
			"next":    nil,
		})
	})

	ctx := context.Background()
	documents, err := env.client.GetSimilarDocuments(ctx, 1, 5)
	require.NoError(t, err)
	assert.Len(t, documents, 0)
}

func TestGetSimilarDocuments_Error(t *testing.T) {
	env := newTestEnv(t)
	defer env.teardown()

	// Add required mocks for tags (since GetSimilarDocuments calls GetAllTags first)
	env.setMockResponse("/api/tags/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"results": []map[string]interface{}{},
			"next":    nil,
		})
	})

	env.mockResponses["/api/documents/"] = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("Internal Server Error"))
	}

	ctx := context.Background()
	documents, err := env.client.GetSimilarDocuments(ctx, 1, 5)
	assert.Error(t, err)
	assert.Nil(t, documents)
	assert.Contains(t, err.Error(), "error searching similar documents")
}

func TestGetSimilarDocuments_TagsError(t *testing.T) {
	env := newTestEnv(t)
	defer env.teardown()

	// Mock tags endpoint to return an error
	env.setMockResponse("/api/tags/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("Tags API Error"))
	})

	ctx := context.Background()
	documents, err := env.client.GetSimilarDocuments(ctx, 1, 5)
	assert.Error(t, err)
	assert.Nil(t, documents)
	assert.Contains(t, err.Error(), "failed to get tags for exclusion")
}

func TestGetSimilarDocuments_ExcludesPaperlessGPTTags(t *testing.T) {
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

	env := newTestEnv(t)
	defer env.teardown()

	// Mock similar documents
	similarDocs := []GetDocumentApiResponseResult{
		{
			ID:               2,
			Title:            "Test Document 1",
			OriginalFileName: "similar-document.pdf",
		},
	}

	response := GetDocumentsApiResponse{
		Count:   1,
		Results: similarDocs,
	}

	// Track the received query parameters
	var receivedQuery string
	env.mockResponses["/api/documents/"] = func(w http.ResponseWriter, r *http.Request) {
		receivedQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(response)
	}

	// Add required mocks for tags (include paperless-gpt tags)
	env.setMockResponse("/api/tags/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"results": []map[string]interface{}{
				{"id": 1, "name": "regular-tag"},
				{"id": 2, "name": "PAPERLESS-GPT"},
				{"id": 3, "name": "paperless-gpt-auto"},
				{"id": 4, "name": "Paperless-GPT-OCR-Auto"},
				{"id": 5, "name": "paperless-gpt-ocr-complete"},
				{"id": 6, "name": "PAPERLESS-GPT-FAILED"},
				{"id": 7, "name": "paperless-gpt-auto-complete"},
			},
			"next": nil,
		})
	})

	ctx := context.Background()
	documents, err := env.client.GetSimilarDocuments(ctx, 1, 5)
	require.NoError(t, err)
	assert.Len(t, documents, 1)
	assert.Equal(t, "similar-document.pdf", documents[0].OriginalFileName)

	// Verify that the query excludes the paperless-gpt tags
	assert.Contains(t, receivedQuery, "ordering=-score")
	assert.Contains(t, receivedQuery, "truncate_content=true")
	assert.Contains(t, receivedQuery, "more_like_id=1")
	assert.Contains(t, receivedQuery, "page_size=5")
	queryValues, err := url.ParseQuery(receivedQuery)
	require.NoError(t, err)
	excludedIDs := strings.Split(queryValues.Get("tags__id__none"), ",")
	assert.ElementsMatch(t, []string{"2", "3", "4", "5", "6", "7"}, excludedIDs)
}

func TestGetSimilarDocuments_NoPaperlessGPTTagsToExclude(t *testing.T) {
	// Set environment variables for the test
	originalManualTag := os.Getenv("MANUAL_TAG")
	originalAutoTag := os.Getenv("AUTO_TAG")
	defer func() {
		os.Setenv("MANUAL_TAG", originalManualTag)
		os.Setenv("AUTO_TAG", originalAutoTag)
	}()

	// Set the tag values and reinitialize the global variables
	os.Setenv("MANUAL_TAG", "paperless-gpt")
	os.Setenv("AUTO_TAG", "paperless-gpt-auto")
	manualTag = "paperless-gpt"
	autoTag = "paperless-gpt-auto"

	env := newTestEnv(t)
	defer env.teardown()

	// Mock similar documents
	similarDocs := []GetDocumentApiResponseResult{
		{
			ID:    2,
			Title: "Test Document 1",
		},
	}

	response := GetDocumentsApiResponse{
		Count:   1,
		Results: similarDocs,
	}

	// Track the received query parameters
	var receivedQuery string
	env.mockResponses["/api/documents/"] = func(w http.ResponseWriter, r *http.Request) {
		receivedQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(response)
	}

	// Add required mocks for tags (no paperless-gpt tags this time)
	env.setMockResponse("/api/tags/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"results": []map[string]interface{}{
				{"id": 1, "name": "regular-tag"},
				{"id": 2, "name": "other-tag"},
			},
			"next": nil,
		})
	})

	ctx := context.Background()
	documents, err := env.client.GetSimilarDocuments(ctx, 1, 5)
	require.NoError(t, err)
	assert.Len(t, documents, 1)

	// Verify that the query does not include tag exclusions when no paperless-gpt tags exist
	assert.Contains(t, receivedQuery, "ordering=-score")
	assert.Contains(t, receivedQuery, "truncate_content=true")
	assert.Contains(t, receivedQuery, "more_like_id=1")
	assert.Contains(t, receivedQuery, "page_size=5")
	assert.NotContains(t, receivedQuery, "tags__id__none", "Should not include tag exclusions when no paperless-gpt tags exist")
}
