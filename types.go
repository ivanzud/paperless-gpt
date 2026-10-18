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
	Title                 string `json:"title"`
	Content               string `json:"content"`
	Tags                  []int  `json:"tags"`
	CreatedDate           string `json:"created"`
	DeprecatedCreatedDate string `json:"created_date,omitempty"`
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

func (result GetDocumentApiResponseResult) paperlessCreatedDate() string {
	if result.CreatedDate != "" {
		return result.CreatedDate
	}
	return result.DeprecatedCreatedDate
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
	ID                    int                   `json:"id"`
	Correspondent         int                   `json:"correspondent"`
	DocumentType          int                   `json:"document_type"`
	Title                 string                `json:"title"`
	Content               string                `json:"content"`
	Tags                  []int                 `json:"tags"`
	CreatedDate           string                `json:"created"`
	DeprecatedCreatedDate string                `json:"created_date,omitempty"`
	OriginalFileName      string                `json:"original_file_name"`
	Owner                 int                   `json:"owner"`
	Permissions           DocumentPermissions   `json:"permissions"`
	Notes                 []DocumentNote        `json:"notes"`
	CustomFields          []CustomFieldResponse `json:"custom_fields"`
}

func (response GetDocumentApiResponse) paperlessCreatedDate() string {
	if response.CreatedDate != "" {
		return response.CreatedDate
	}
	return response.DeprecatedCreatedDate
}

// Document is a stripped down version of the document object from paperless-ngx.
// Response payload for /documents endpoint and part of request payload for /generate-suggestions endpoint
//
// Tags holds the names of the document's tags that are visible to the
// configured API user, while TagIDs holds the raw tag IDs paperless-ngx
// reports — including tags the API user cannot see, which resolve to no
// name. Keeping the IDs around lets updates preserve invisible tags instead
// of silently dropping them.
type Document struct {
	ID               int                   `json:"id"`
	Title            string                `json:"title"`
	Content          string                `json:"content"`
	Tags             []string              `json:"tags"`
	TagIDs           []int                 `json:"tag_ids,omitempty"`
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
	// Workflow, when set, overrides the global prompts and generation flags.
	// Set by the background processor and the workflow preview.
	Workflow *WorkflowConfig `json:"-"`
	// TriggerTag is the tag the background processor found the document
	// under; it comes off once processing succeeds.
	TriggerTag string `json:"-"`
}

// AnalyzeDocumentsRequest is the request payload for the ad-hoc analysis
type AnalyzeDocumentsRequest struct {
	DocumentIDs []int  `json:"document_ids"`
	Prompt      string `json:"prompt"`
}

// WorkflowConfig defines a named processing workflow triggered by a specific paperless-ngx tag.
// Each workflow can override the global generation flags and carry its own prompt templates.
type WorkflowConfig struct {
	// ID is a stable, URL-safe identifier (e.g. "invoices"). Auto-generated when empty.
	ID string `json:"id"`
	// Name is a human-readable label shown in the UI.
	Name string `json:"name"`
	// TriggerTag is the paperless-ngx tag that activates this workflow (e.g. "paperless-gpt-invoices").
	TriggerTag string `json:"trigger_tag"`
	// CompletionTag is optionally added to a document after successful processing.
	CompletionTag string `json:"completion_tag,omitempty"`
	// Generation flags – nil means "inherit the global default".
	GenerateTitles         *bool `json:"generate_titles,omitempty"`
	GenerateTags           *bool `json:"generate_tags,omitempty"`
	GenerateCorrespondents *bool `json:"generate_correspondents,omitempty"`
	GenerateCreatedDate    *bool `json:"generate_created_date,omitempty"`
	GenerateDocumentTypes  *bool `json:"generate_document_types,omitempty"`
	GenerateCustomFields   *bool `json:"generate_custom_fields,omitempty"`
	// CustomFieldIDs are the custom fields this workflow fills. Empty means the
	// global selection from Settings.
	CustomFieldIDs []int `json:"custom_field_ids,omitempty"`
	// RequireApproval holds the workflow's suggestions until the user
	// approves them in the review queue instead of writing them right away.
	RequireApproval bool `json:"require_approval,omitempty"`
	// EnableOCR, when true, runs OCR on the document before metadata generation.
	// nil or false means OCR is skipped for this workflow.
	EnableOCR *bool `json:"enable_ocr,omitempty"`
	// OCRLimitPages overrides the effective OCR page limit for this workflow.
	// nil = use effective defaults (settings over env); 0 = no limit.
	OCRLimitPages *int `json:"ocr_limit_pages,omitempty"`
	// Prompts maps prompt-file names (e.g. "title_prompt") to their template content.
	// An absent key means the global default prompt is used.
	// "ocr_prompt" overrides the OCR template when EnableOCR is true.
	Prompts map[string]string `json:"prompts,omitempty"`
}

// Settings defines the structure for server-side UI settings
type Settings struct {
	CustomFieldsEnable      bool             `json:"custom_fields_enable"`
	CustomFieldsSelectedIDs []int            `json:"custom_fields_selected_ids"`
	CustomFieldsWriteMode   string           `json:"custom_fields_write_mode"` // "append" or "replace"
	TagsAutoCreate          bool             `json:"tags_auto_create"`
	OCR                     OCRDefaults      `json:"ocr"`
	Workflows               []WorkflowConfig `json:"workflows,omitempty"`
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
// Searchable-PDF upload modes (PDF_UPLOAD_MODE).
const (
	// PDFUploadModeNew uploads the searchable PDF as a new document.
	PDFUploadModeNew = "new"
	// PDFUploadModeVersion adds the searchable PDF as a new version of the same
	// document (paperless-ngx 3.0+). The document id, its metadata and the
	// original file are kept; nothing is deleted.
	PDFUploadModeVersion = "version"
)

type OCROptions struct {
	UploadPDF       bool   // Whether to upload the generated PDF
	UploadMode      string // PDFUploadModeNew (default) or PDFUploadModeVersion
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
	FindDocumentIDsByReference(ctx context.Context, reference string, excludeID, maxMatches int) (ids []int, tooMany bool, err error)
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
	UploadDocumentVersion(ctx context.Context, documentID int, data []byte, filename string, versionLabel string) (string, error)
	GetTaskStatus(ctx context.Context, taskID string) (map[string]interface{}, error)
	DeleteDocument(ctx context.Context, documentID int) error
	GetUiSettings(ctx context.Context) (*UiSettings, error)
	GetPermissions(ctx context.Context, doc *Document) (*ObjPermissions, error)
}

// DocumentProcessor defines the interface for processing documents with OCR
type DocumentProcessor interface {
	ProcessDocumentOCR(ctx context.Context, documentID int, options OCROptions, jobID string) (*ProcessedDocument, error)
}
