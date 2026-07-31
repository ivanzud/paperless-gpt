package main

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

type UiSettingsPermissions struct {
	Owner      *int  `json:"default_owner"`
	EditUsers  []int `json:"default_edit_users"`
	ViewUsers  []int `json:"default_view_users"`
	EditGroups []int `json:"default_edit_groups"`
	ViewGroups []int `json:"default_view_groups"`
}

type Permissions struct {
	View struct {
		Users  []int `json:"users"`
		Groups []int `json:"groups"`
	} `json:"view"`
	Change struct {
		Users  []int `json:"users"`
		Groups []int `json:"groups"`
	} `json:"change"`
}

type SetPermissions = Permissions
type DocumentPermissions = Permissions

type ObjPermissions struct {
	Owner          *int            `json:"owner"`
	SetPermissions *SetPermissions `json:"set_permissions"`
}

// GetDocumentsApiResponse is the response payload for /documents endpoint.
// But we are only interested in a subset of the fields.
type GetDocumentsApiResponse struct {
	Count int `json:"count"`
	// Next     interface{} `json:"next"`
	// Previous interface{} `json:"previous"`
	All     []int                          `json:"all"`
	Results []GetDocumentApiResponseResult `json:"results"`
}

// GetDocumentApiResponseResult is a part of the response payload for /documents endpoint.
// But we are only interested in a subset of the fields.
type GetDocumentApiResponseResult struct {
	ID            int `json:"id"`
	Correspondent int `json:"correspondent"`
	// DocumentType        interface{}   `json:"document_type"`
	// StoragePath         interface{}   `json:"storage_path"`
	Title   string `json:"title"`
	Content string `json:"content"`
	Tags    []int  `json:"tags"`
	// Created             time.Time     `json:"created"`
	CreatedDate string `json:"created_date"`
	// Modified            time.Time     `json:"modified"`
	// Added               time.Time     `json:"added"`
	// ArchiveSerialNumber interface{}   `json:"archive_serial_number"`
	OriginalFileName string `json:"original_file_name"`
	// ArchivedFileName    string        `json:"archived_file_name"`
	Owner       int                 `json:"owner"`
	Permissions DocumentPermissions `json:"permissions"`
	Notes       []DocumentNote      `json:"notes"`
	// SearchHit struct {
	// 	Score          float64 `json:"score"`
	// 	Highlights     string  `json:"highlights"`
	// 	NoteHighlights string  `json:"note_highlights"`
	// 	Rank           int     `json:"rank"`
	// } `json:"__search_hit__"`
}

// CustomFieldResponse represents a custom field with its value for a document
type CustomFieldResponse struct {
	Field int         `json:"field"`
	Value interface{} `json:"value"`
	Name  string      `json:"name,omitempty"`
}

// CustomFieldSuggestion represents a suggested custom field with its value and name
type CustomFieldSuggestion struct {
	ID    int         `json:"id"`
	Name  string      `json:"name"`
	Value interface{} `json:"value"`
}

type DocumentNote struct {
	ID   int    `json:"id"`
	Note string `json:"note"`
}

// GetDocumentApiResponse is the response payload for /documents/{id} endpoint.
// But we are only interested in a subset of the fields.
type GetDocumentApiResponse struct {
	ID               int                   `json:"id"`
	Correspondent    int                   `json:"correspondent"`
	DocumentType     int                   `json:"document_type"`
	Title            string                `json:"title"`
	Content          string                `json:"content"`
	Tags             []int                 `json:"tags"`
	CreatedDate      string                `json:"created_date"`
	OriginalFileName string                `json:"original_file_name"`
	Owner            int                   `json:"owner"`
	Permissions      DocumentPermissions   `json:"permissions"`
	Notes            []DocumentNote        `json:"notes"`
	CustomFields     []CustomFieldResponse `json:"custom_fields"`
}

// Document is a stripped down version of the document object from paperless-ngx.
// Response payload for /documents endpoint and part of request payload for /generate-suggestions endpoint
type Document struct {
	ID               int                   `json:"id"`
	Title            string                `json:"title"`
	Content          string                `json:"content"`
	Tags             []string              `json:"tags"`
	Correspondent    string                `json:"correspondent"`
	Owner            int                   `json:"owner"`
	Permissions      DocumentPermissions   `json:"permissions"`
	CreatedDate      string                `json:"created_date"`
	OriginalFileName string                `json:"original_file_name"`
	DocumentTypeName string                `json:"document_type_name"`
	CustomFields     []CustomFieldResponse `json:"custom_fields"`
}

// GenerateSuggestionsRequest is the request payload for generating suggestions for /generate-suggestions endpoint
type GenerateSuggestionsRequest struct {
	Documents              []Document `json:"documents"`
	GenerateTitles         bool       `json:"generate_titles,omitempty"`
	GenerateTags           bool       `json:"generate_tags,omitempty"`
	GenerateCorrespondents bool       `json:"generate_correspondents,omitempty"`
	GenerateCreatedDate    bool       `json:"generate_created_date,omitempty"`
	GenerateCustomFields   bool       `json:"generate_custom_fields,omitempty"`
	GenerateSummary        bool       `json:"generate_summary,omitempty"`
	GenerateDocumentTypes  bool       `json:"generate_document_types,omitempty"`
	IsAutoProcessing       bool       `json:"-"` // internal flag; not exposed via API
}

// AnalyzeDocumentsRequest is the request payload for the ad-hoc analysis
type AnalyzeDocumentsRequest struct {
	DocumentIDs []int  `json:"document_ids"`
	Prompt      string `json:"prompt"`
}

// Settings defines the structure for server-side UI settings
type Settings struct {
	CustomFieldsEnable      bool        `json:"custom_fields_enable"`
	CustomFieldsSelectedIDs []int       `json:"custom_fields_selected_ids"`
	CustomFieldsWriteMode   string      `json:"custom_fields_write_mode"` // "append" or "replace"
	TagsAutoCreate          bool        `json:"tags_auto_create"`
	OCR                     OCRDefaults `json:"ocr"`
}

type UiSettingsUser struct {
	ID          int    `json:"id"`
	Username    string `json:"username"`
	IsStaff     bool   `json:"is_staff"`
	IsSuperuser bool   `json:"is_superuser"`
	Groups      []int  `json:"groups"`
}

type UiSettingsSettings struct {
	Permissions UiSettingsPermissions `json:"permissions"`
}

type UiSettings struct {
	User     UiSettingsUser     `json:"user"`
	Settings UiSettingsSettings `json:"settings"`
}

// OCRDefaults are persisted run-option defaults, editable from the UI.
// A nil field means "use the env-derived value". They drive Auto-OCR and
// prefill the Playground — the ramp from manual runs to hands-off auto mode.
type OCRDefaults struct {
	LimitPages      *int    `json:"limit_pages,omitempty"`
	ProcessMode     *string `json:"process_mode,omitempty"`
	UploadPDF       *bool   `json:"upload_pdf,omitempty"`
	ReplaceOriginal *bool   `json:"replace_original,omitempty"`
	CopyMetadata    *bool   `json:"copy_metadata,omitempty"`
}

// DocumentSuggestion is the response payload for /generate-suggestions endpoint and the request payload for /update-documents endpoint (as an array)
type DocumentSuggestion struct {
	ID                     int                     `json:"id"`
	OriginalDocument       Document                `json:"original_document"`
	SuggestedTitle         string                  `json:"suggested_title,omitempty"`
	SuggestedTags          []string                `json:"suggested_tags,omitempty"`
	SuggestedContent       string                  `json:"suggested_content,omitempty"`
	SuggestedCorrespondent string                  `json:"suggested_correspondent,omitempty"`
	SuggestedCreatedDate   string                  `json:"suggested_created_date,omitempty"`
	SuggestedDocumentType  string                  `json:"suggested_document_type,omitempty"`
	SuggestedCustomFields  []CustomFieldSuggestion `json:"suggested_custom_fields,omitempty"`
	SuggestedSummary       string                  `json:"suggested_summary,omitempty"`
	KeepOriginalTags       bool                    `json:"keep_original_tags,omitempty"`
	RemoveTags             []string                `json:"remove_tags,omitempty"`
	AddTags                []string                `json:"add_tags,omitempty"`
	CustomFieldsWriteMode  string                  `json:"custom_fields_write_mode,omitempty"`
	CustomFieldsEnable     bool                    `json:"custom_fields_enable"`
}

type Correspondent struct {
	Name              string `json:"name"`
	MatchingAlgorithm int    `json:"matching_algorithm"`
	Match             string `json:"match"`
	IsInsensitive     bool   `json:"is_insensitive"`
	// omitempty so nil owners are dropped from the JSON body; paperless-ngx
	// then falls back to the request user (request.user) as the owner of
	// the newly created object. Sending "owner": null overrides that and
	// produces ownerless correspondents — they still appear in the
	// correspondents list, but documents assigned to them are shown as
	// "private" in the UI instead of the correspondent name.
	Owner          *int            `json:"owner,omitempty"`
	SetPermissions *SetPermissions `json:"set_permissions,omitempty"`
}

// OCROptions contains options for the OCR processing
type OCROptions struct {
	UploadPDF       bool   // Whether to upload the generated PDF
	ReplaceOriginal bool   // Whether to delete the original document after uploading
	CopyMetadata    bool   // Whether to copy metadata from the original document
	LimitPages      int    // Limit on the number of pages to process (0 = no limit)
	ProcessMode     string // OCR processing mode: "image" (default) or "pdf"
	ExistingContent string // Existing document text (e.g., from Tesseract) to include in OCR prompt
	PromptOverride  string // Run-scoped OCR prompt template; empty = use the saved template
}

// PartialUpdateError signals that some requested changes were applied while
// others were rejected or failed in a later API operation. Callers should
// treat this as a successful update but apply the fail tag so the user knows
// the document needs review.
type PartialUpdateError struct {
	DocumentID    int
	DroppedFields []string
}

func (e *PartialUpdateError) Error() string {
	return fmt.Sprintf("document %d updated partially; %d requested field(s) were not applied: %v", e.DocumentID, len(e.DroppedFields), e.DroppedFields)
}

// ClientInterface defines the interface for PaperlessClient operations
type ClientInterface interface {
	GetDocumentsByTag(ctx context.Context, tag string, pageSize int) ([]Document, error)
	GetDocumentCountByTag(ctx context.Context, tag string) (int, error)
	UpdateDocuments(ctx context.Context, documents []DocumentSuggestion, db *gorm.DB, isUndo bool) error
	GetDocument(ctx context.Context, documentID int) (Document, error)
	GetDocumentThumbnail(ctx context.Context, documentID int) ([]byte, string, error)
	SearchDocuments(ctx context.Context, query string, pageSize int) ([]Document, error)
	GetDocumentPageImage(ctx context.Context, documentID int, pageIndex int) ([]byte, error)
	GetAllTags(ctx context.Context) (map[string]int, error)
	GetAllCorrespondents(ctx context.Context) (map[string]int, error)
	GetAllDocumentTypes(ctx context.Context) ([]DocumentType, error)
	GetCustomFields(ctx context.Context) ([]CustomField, error)
	CreateTag(ctx context.Context, tagName string, objPerms *ObjPermissions) (int, error)
	CreateDocumentNote(ctx context.Context, documentID int, note string) (DocumentNote, error)
	DeleteDocumentNote(ctx context.Context, documentID int, noteID int) error
	DownloadDocumentAsImages(ctx context.Context, documentID int, pageLimit int) ([]string, int, error)
	DownloadDocumentAsPDF(ctx context.Context, documentID int, limitPages int, split bool) ([]string, []byte, int, error)
	UploadDocument(ctx context.Context, data []byte, filename string, metadata map[string]interface{}) (string, error)
	GetTaskStatus(ctx context.Context, taskID string) (map[string]interface{}, error)
	DeleteDocument(ctx context.Context, documentID int) error
	GetUiSettings(ctx context.Context) (*UiSettings, error)
	GetPermissions(ctx context.Context, doc *Document) (*ObjPermissions, error)
}

// DocumentProcessor defines the interface for processing documents with OCR
type DocumentProcessor interface {
	ProcessDocumentOCR(ctx context.Context, documentID int, options OCROptions, jobID string) (*ProcessedDocument, error)
}
