package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"paperless-gpt/internal/pdfrender"

	"github.com/disintegration/imaging"
	"github.com/gabriel-vasile/mimetype"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"
)

// PaperlessClient struct to interact with the Paperless-NGX API
type PaperlessClient struct {
	BaseURL     string
	APIToken    string
	HTTPClient  *http.Client
	CacheFolder string
	UserID      *int
}

// CustomField represents a custom field from the Paperless-ngx API
type CustomField struct {
	ID        int                  `json:"id"`
	Name      string               `json:"name"`
	DataType  string               `json:"data_type"`
	ExtraData CustomFieldExtraData `json:"extra_data"`
}

type CustomFieldExtraData struct {
	SelectOptions []SelectOption `json:"select_options"`
}

type SelectOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// DocumentType represents a document type from the Paperless-ngx API
type DocumentType struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type TagRequest struct {
	Name           string          `json:"name"`
	Owner          *int            `json:"owner"`
	SetPermissions *SetPermissions `json:"set_permissions,omitempty"`
}

func mapPermissions(uiSettingsPermissions *UiSettingsPermissions) *Permissions {
	var permissions Permissions
	permissions.View.Users = uiSettingsPermissions.ViewUsers
	permissions.View.Groups = uiSettingsPermissions.ViewGroups
	permissions.Change.Users = uiSettingsPermissions.EditUsers
	permissions.Change.Groups = uiSettingsPermissions.EditGroups
	return &permissions
}

// lookupTagID resolves a tag name to its paperless-ngx id, ignoring case.
//
// paperless-ngx preserves the case a tag was created with, while the names
// paperless-gpt matches against are typed by a user into MANUAL_TAG, AUTO_TAG,
// AUTO_TAG_COMPLETE and friends. An exact map index makes a tag that exists look
// missing, and both outcomes are bad: with CREATE_NEW_TAGS off the tag is
// silently dropped, so a processed document keeps no completion marker and is
// indistinguishable from one that was never touched; with it on, a second tag is
// created that differs from the existing one only by case.
//
// The rest of the tag handling already ignores case -- the RemoveTags pass below
// and the suggestion filtering in app_llm.go both use strings.EqualFold -- so an
// exact lookup here is the odd one out.
//
// The stored spelling is returned alongside the id so callers record what
// paperless-ngx actually holds rather than the user's variant.
func lookupTagID(availableTags map[string]int, tagName string) (string, int, bool) {
	return lookupNamedID("Tag", availableTags, tagName)
}

// isNoMatchCorrespondent recognizes explicit no-match values from the
// correspondent prompt. They must never become real correspondents.
func isNoMatchCorrespondent(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "unknown", "unbekannt", "none", "n/a", "no correspondent":
		return true
	default:
		return false
	}
}

// lookupNamedID finds a tag, correspondent or document type by name the way
// paperless-ngx compares names: case-insensitively, so "MONTANA GmbH" finds
// an existing "Montana GmbH" instead of trying to create a duplicate that
// paperless-ngx rejects. An exact match wins; among case variants the lowest
// id wins.
func lookupNamedID(kind string, available map[string]int, name string) (string, int, bool) {
	name = strings.TrimSpace(name)
	if id, exists := available[name]; exists {
		return name, id, true
	}

	// GetAllTags keys this map by the exact name paperless-ngx returned, so a
	// database that already contains case variants ("Foo" and "foo") holds both
	// as separate entries. Ranging a map would let Go's randomised iteration
	// order decide which id wins, making the resulting PATCH nondeterministic
	// across runs. Settle it on the lowest id instead: ids increase with
	// creation, so the lowest is the original tag and any case-variant
	// duplicate -- exactly what the CREATE_NEW_TAGS path used to mint -- sorts
	// after it.
	matchedName, matchedID, matches := "", 0, 0
	for availableName, id := range available {
		if !strings.EqualFold(availableName, name) {
			continue
		}
		matches++
		if matches == 1 || id < matchedID {
			matchedName, matchedID = availableName, id
		}
	}
	if matches == 0 {
		return "", 0, false
	}
	if matches > 1 {
		log.Warnf("%s %q matches %d entries in paperless-ngx that differ only by case; using %q (id %d). Merge the duplicates to make this unambiguous.",
			kind, name, matches, matchedName, matchedID)
	}
	return matchedName, matchedID, true
}

// resolveTagNames maps paperless-ngx tag IDs to tag names using the tag set
// visible to the configured API user. IDs that are not present in allTags —
// for example tags owned by another paperless-ngx user that a scoped-down API
// token cannot see — resolve to no name and are returned in the second value
// so callers can warn and preserve them during updates.
func resolveTagNames(tagIDs []int, allTags map[string]int) (names []string, invisibleIDs []int) {
	idToName := make(map[int]string, len(allTags))
	for name, id := range allTags {
		idToName[id] = name
	}
	names = make([]string, 0, len(tagIDs))
	for _, id := range tagIDs {
		if name, ok := idToName[id]; ok {
			names = append(names, name)
		} else {
			invisibleIDs = append(invisibleIDs, id)
		}
	}
	return names, invisibleIDs
}

func hasSameTags(original, suggested []string) bool {
	if len(original) != len(suggested) {
		return false
	}

	// Create copies to avoid modifying original slices
	orig := make([]string, len(original))
	sugg := make([]string, len(suggested))

	copy(orig, original)
	copy(sugg, suggested)

	// Sort both slices
	sort.Strings(orig)
	sort.Strings(sugg)

	// Compare elements
	for i := range orig {
		if orig[i] != sugg[i] {
			return false
		}
	}

	return true
}

var selectOptionErrorRegex = regexp.MustCompile(`(?i)(?:'|")label(?:'|")\s*:\s*(?:'|")([^'"]+)(?:'|")\s*,\s*(?:'|")id(?:'|")\s*:\s*(?:'|")([^'"]+)(?:'|")`)

func remapSelectCustomFieldLabelsFromError(updatedFields map[string]interface{}, responseBody []byte, documentID int) bool {
	customFields, ok := updatedFields["custom_fields"].([]CustomFieldResponse)
	if !ok || len(customFields) == 0 {
		return false
	}

	var errorPayload struct {
		CustomFields []map[string][]string `json:"custom_fields"`
	}
	if err := json.Unmarshal(responseBody, &errorPayload); err != nil || len(errorPayload.CustomFields) == 0 {
		return false
	}

	changed := false
	for i, fieldErrors := range errorPayload.CustomFields {
		if i >= len(customFields) {
			break
		}

		messages := fieldErrors["non_field_errors"]
		if len(messages) == 0 {
			continue
		}

		currentValue, isString := customFields[i].Value.(string)
		if !isString {
			continue
		}
		currentValue = strings.TrimSpace(currentValue)
		if currentValue == "" {
			continue
		}

		options := parseSelectOptionsFromValidationMessages(messages)
		mappedID, exists := options[strings.ToLower(currentValue)]
		if !exists {
			continue
		}

		customFields[i].Value = mappedID
		changed = true
		log.Infof("Document %d: mapped custom field %d select label %q to id %q", documentID, customFields[i].Field, currentValue, mappedID)
	}

	if changed {
		updatedFields["custom_fields"] = customFields
	}
	return changed
}

func parseSelectOptionsFromValidationMessages(messages []string) map[string]string {
	options := make(map[string]string)
	for _, message := range messages {
		matches := selectOptionErrorRegex.FindAllStringSubmatch(message, -1)
		for _, match := range matches {
			if len(match) < 3 {
				continue
			}
			label := strings.ToLower(strings.TrimSpace(match[1]))
			id := strings.TrimSpace(match[2])
			if label != "" && id != "" {
				options[label] = id
			}
		}
	}
	return options
}

// parsePaperlessValidationErrors interprets paperless-ngx's 400 response body
// and classifies the validation errors into three buckets:
//   - scalarFields: top-level fields (other than custom_fields and tags) that
//     paperless rejected. These can be removed from the PATCH and retried.
//   - customFieldIndices: indices into the custom_fields array whose entries
//     paperless rejected. These entries can be removed and retried.
//   - unrecoverable: true if the response contains errors paperless-gpt cannot
//     safely drop (currently: any failure that references the "tags" field,
//     because the tag update is what breaks the auto-processing loop). The
//     caller must treat this as a hard failure.
//
// Returns (nil, nil, false) if the body cannot be parsed as the expected
// shape; the caller treats this as a hard failure too.
//
// Example paperless-ngx 400 bodies this parser handles:
//
//	{"created_date":["Date has wrong format..."],
//	 "custom_fields":[{},{},{},{},{},{"non_field_errors":["..."]},{},{}]}
//
//	{"custom_fields":{"2":{"non_field_errors":["Date has wrong format..."]}}}
func parsePaperlessValidationErrors(body []byte) (scalarFields map[string]bool, customFieldIndices []int, unrecoverable bool) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, nil, false
	}
	scalarFields = make(map[string]bool)
	for key, val := range raw {
		switch key {
		case "tags":
			// Tag updates are load-bearing for the loop-break behaviour; we
			// must not drop them silently. Treat as unrecoverable so the
			// caller falls back to recoverFromFailedUpdate.
			unrecoverable = true
			return
		case "custom_fields":
			switch errs := val.(type) {
			case []any:
				for i, entry := range errs {
					obj, ok := entry.(map[string]any)
					if !ok || len(obj) == 0 {
						continue
					}
					customFieldIndices = append(customFieldIndices, i)
				}
			case map[string]any:
				// Newer paperless-ngx versions report only the failing
				// entries, keyed by their index in the custom_fields array.
				for key, entry := range errs {
					i, err := strconv.Atoi(key)
					if err != nil || i < 0 {
						// Not an index (e.g. a list-level error) — bail to
						// the hard-failure path.
						unrecoverable = true
						return
					}
					if obj, ok := entry.(map[string]any); ok && len(obj) == 0 {
						continue
					}
					customFieldIndices = append(customFieldIndices, i)
				}
				slices.Sort(customFieldIndices)
			default:
				// Unexpected shape — bail to hard-failure path.
				unrecoverable = true
				return
			}
		default:
			scalarFields[key] = true
		}
	}
	if len(scalarFields) == 0 && len(customFieldIndices) == 0 {
		return nil, nil, false
	}
	return scalarFields, customFieldIndices, false
}

func stripFailedFields(
	updatedFields map[string]interface{},
	originalFields map[string]interface{},
	scalarFields map[string]bool,
	customFieldIndices []int,
) []string {
	var dropped []string

	for apiField := range scalarFields {
		field := internalDocumentFieldName(apiField)
		if _, present := updatedFields[field]; present {
			delete(updatedFields, field)
			delete(originalFields, field)
			dropped = append(dropped, field)
		}
	}

	if len(customFieldIndices) == 0 {
		return dropped
	}

	customFields, ok := updatedFields["custom_fields"].([]CustomFieldResponse)
	if !ok {
		return dropped
	}

	sortedIndices := slices.Clone(customFieldIndices)
	sort.Sort(sort.Reverse(sort.IntSlice(sortedIndices)))
	for _, idx := range sortedIndices {
		if idx < 0 || idx >= len(customFields) {
			continue
		}
		dropped = append(dropped, fmt.Sprintf("custom_fields[%d](field_id=%d)", idx, customFields[idx].Field))
		customFields = append(customFields[:idx], customFields[idx+1:]...)
	}

	if len(customFields) == 0 {
		delete(updatedFields, "custom_fields")
		delete(originalFields, "custom_fields")
	} else {
		updatedFields["custom_fields"] = customFields
	}

	return dropped
}

func internalDocumentFieldName(apiField string) string {
	if apiField == "created" {
		return "created_date"
	}
	return apiField
}

func documentPatchPayload(fields map[string]interface{}) map[string]interface{} {
	payload := make(map[string]interface{}, len(fields))
	for field, value := range fields {
		payload[field] = value
	}

	if createdDate, ok := payload["created_date"]; ok {
		payload["created"] = createdDate
		delete(payload, "created_date")
	}

	return payload
}

func (client *PaperlessClient) patchDocumentFields(ctx context.Context, path string, documentID int, fields map[string]interface{}) (int, []byte, error) {
	jsonData, err := json.Marshal(documentPatchPayload(fields))
	if err != nil {
		return 0, nil, fmt.Errorf("error marshalling JSON for document %d: %w", documentID, err)
	}

	resp, err := client.Do(ctx, "PATCH", path, bytes.NewBuffer(jsonData))
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, bodyBytes, nil
}

func recoverPatchValidationFailure(
	updatedFields map[string]interface{},
	originalFields map[string]interface{},
	bodyBytes []byte,
	statusCode int,
	documentID int,
) ([]string, error) {
	if statusCode != http.StatusBadRequest && statusCode != http.StatusUnprocessableEntity {
		return nil, fmt.Errorf("error updating document %d: %d, %s", documentID, statusCode, string(bodyBytes))
	}

	lowerBody := strings.ToLower(string(bodyBytes))
	if _, hasCustomFields := updatedFields["custom_fields"]; hasCustomFields && strings.Contains(lowerBody, "custom") {
		if remapSelectCustomFieldLabelsFromError(updatedFields, bodyBytes, documentID) {
			log.Warnf("Document %d: remapped custom-field select labels after validation failure; retrying update.", documentID)
			return nil, nil
		}
	}

	scalarFails, customFieldFails, unrecoverable := parsePaperlessValidationErrors(bodyBytes)
	if unrecoverable || (len(scalarFails) == 0 && len(customFieldFails) == 0) {
		return nil, fmt.Errorf("error updating document %d: %d, %s", documentID, statusCode, string(bodyBytes))
	}

	newlyDropped := stripFailedFields(updatedFields, originalFields, scalarFails, customFieldFails)
	if len(newlyDropped) == 0 {
		return nil, fmt.Errorf("error updating document %d: %d, %s", documentID, statusCode, string(bodyBytes))
	}

	return newlyDropped, nil
}

func (client *PaperlessClient) patchDocumentWithValidationRecovery(
	ctx context.Context,
	path string,
	documentID int,
	updatedFields map[string]interface{},
	originalFields map[string]interface{},
	droppedFields []string,
) (bool, []string, error) {
	const maxPatchRetries = 3

	for attempt := 0; attempt <= maxPatchRetries; attempt++ {
		if len(updatedFields) == 0 {
			log.Warnf("Document %d: paperless-ngx rejected all suggested fields; skipping update. Dropped fields: %v", documentID, droppedFields)
			return true, droppedFields, nil
		}

		statusCode, bodyBytes, err := client.patchDocumentFields(ctx, path, documentID, updatedFields)
		if err != nil {
			return false, droppedFields, fmt.Errorf("error updating document %d: %w", documentID, err)
		}
		if statusCode == http.StatusOK {
			return false, droppedFields, nil
		}

		newlyDropped, err := recoverPatchValidationFailure(updatedFields, originalFields, bodyBytes, statusCode, documentID)
		if err != nil {
			return false, droppedFields, err
		}
		if len(newlyDropped) == 0 {
			continue
		}

		droppedFields = append(droppedFields, newlyDropped...)
		log.Warnf("Document %d: paperless-ngx rejected fields %v on attempt %d/%d; retrying without them. Raw response: %s", documentID, newlyDropped, attempt+1, maxPatchRetries+1, string(bodyBytes))
	}

	return false, droppedFields, fmt.Errorf("error updating document %d: paperless-ngx still rejected the update after %d retries; dropped fields: %v", documentID, maxPatchRetries, droppedFields)
}

func getPaperlessHTTPTimeout() time.Duration {
	const defaultTimeout = 5 * time.Minute

	raw := strings.TrimSpace(os.Getenv("PAPERLESS_HTTP_TIMEOUT"))
	if raw == "" {
		return defaultTimeout
	}
	if raw == "0" {
		return 0
	}

	timeout, err := time.ParseDuration(raw)
	if err != nil {
		log.Warnf("Invalid PAPERLESS_HTTP_TIMEOUT value %q, using default %s", raw, defaultTimeout)
		return defaultTimeout
	}
	if timeout < 0 {
		log.Warnf("Negative PAPERLESS_HTTP_TIMEOUT value %q is invalid, using default %s", raw, defaultTimeout)
		return defaultTimeout
	}

	return timeout
}

// NewPaperlessClient creates a new instance of PaperlessClient with a default HTTP client
func NewPaperlessClient(baseURL, apiToken string) *PaperlessClient {
	cacheFolder := os.Getenv("PAPERLESS_GPT_CACHE_DIR")

	// Create a custom HTTP transport with TLS configuration
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			// #nosec G402 -- this is an explicit user opt-in for self-signed Paperless deployments.
			InsecureSkipVerify: paperlessInsecureSkipVerify,
		},
	}
	httpClient := &http.Client{
		Transport: tr,
		Timeout:   getPaperlessHTTPTimeout(),
	}

	return &PaperlessClient{
		BaseURL:     strings.TrimRight(baseURL, "/"),
		APIToken:    apiToken,
		HTTPClient:  httpClient,
		CacheFolder: cacheFolder,
	}
}

// Do method to make requests to the Paperless-NGX API
func (client *PaperlessClient) Do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	url := fmt.Sprintf("%s/%s", client.BaseURL, strings.TrimLeft(path, "/"))
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", fmt.Sprintf("Token %s", client.APIToken))

	// Set Content-Type if body is present
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	log.WithFields(logrus.Fields{
		"method": method,
		"url":    url,
	}).Debug("Making HTTP request")

	resp, err := client.HTTPClient.Do(req)
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"url":    url,
			"method": method,
			"error":  err,
		}).Error("HTTP request failed")
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}

	// Check if response is HTML instead of JSON for API endpoints
	if strings.HasPrefix(path, "api/") {
		contentType := resp.Header.Get("Content-Type")
		if strings.Contains(contentType, "text/html") {
			bodyBytes, _ := io.ReadAll(resp.Body)
			if err := resp.Body.Close(); err != nil {
				log.WithError(err).WithField("url", url).Warn("Failed to close HTML error response body")
			}

			// Create a new response with the same body for the caller
			resp = &http.Response{
				Status:     resp.Status,
				StatusCode: resp.StatusCode,
				Header:     resp.Header,
				Body:       io.NopCloser(bytes.NewBuffer(bodyBytes)),
			}

			log.WithFields(logrus.Fields{
				"url":          url,
				"method":       method,
				"content-type": contentType,
				"status-code":  resp.StatusCode,
				"response":     string(bodyBytes),
				"base-url":     client.BaseURL,
				"request-path": path,
				"full-headers": resp.Header,
			}).Error("Received HTML response for API request")

			return nil, fmt.Errorf("received HTML response instead of JSON (status: %d). This often indicates an SSL/TLS issue or invalid authentication. Check your PAPERLESS_URL, PAPERLESS_TOKEN and PAPERLESS_INSECURE_SKIP_VERIFY settings. Full response: %s", resp.StatusCode, string(bodyBytes))
		}
	}

	return resp, nil
}

// GetUiSettings retrieves the default object permissions for the API user.
func (client *PaperlessClient) GetUiSettings(ctx context.Context) (*UiSettings, error) {
	resp, err := client.Do(ctx, "GET", "api/ui_settings/", nil)
	if err != nil {
		return nil, fmt.Errorf("error obtaining ui_settings: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("error obtaining ui_settings: %d, %s", resp.StatusCode, string(bodyBytes))
	}

	var uiSettings UiSettings
	if err := json.NewDecoder(resp.Body).Decode(&uiSettings); err != nil {
		return nil, err
	}

	return &uiSettings, nil
}

// GetPermissions resolves owner and permissions for new tags and correspondents.
func (client *PaperlessClient) GetPermissions(ctx context.Context, doc *Document) (*ObjPermissions, error) {
	switch objPermissions {
	case "none":
		return &ObjPermissions{}, nil
	case "document":
		if doc == nil {
			return nil, fmt.Errorf("cannot copy object permissions without a document")
		}

		objPerms := &ObjPermissions{SetPermissions: &doc.Permissions}
		if doc.Owner != 0 {
			objPerms.Owner = &doc.Owner
		}
		return objPerms, nil
	case "client":
		uiSettings, err := client.GetUiSettings(ctx)
		if err != nil {
			return nil, fmt.Errorf("error getting permissions: %w", err)
		}
		return &ObjPermissions{
			Owner:          uiSettings.Settings.Permissions.Owner,
			SetPermissions: mapPermissions(&uiSettings.Settings.Permissions),
		}, nil
	default:
		return nil, fmt.Errorf("invalid OBJ_PERMISSIONS value %q", objPermissions)
	}
}

func (client *PaperlessClient) getPermissionsOrWarn(ctx context.Context, doc *Document, format string, args ...interface{}) *ObjPermissions {
	objPerms, err := client.GetPermissions(ctx, doc)
	if err != nil {
		log.Warnf(format+": %v", append(args, err)...)
		return nil
	}
	return objPerms
}

// GetAllTags retrieves all tags from the Paperless-NGX API
func (client *PaperlessClient) GetAllTags(ctx context.Context) (map[string]int, error) {
	tagIDMapping := make(map[string]int)
	// Paperless defaults to 25 results per page. This helper is used during
	// startup for marker-tag checks, so asking for a larger page size avoids
	// hundreds of slow round trips on installations with large tag catalogs.
	// Pagination is still followed because the server may cap page_size.
	path := "api/tags/?page_size=1000"

	for path != "" {
		resp, err := client.Do(ctx, "GET", path, nil)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			bodyBytes, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("error fetching tags: %d, %s", resp.StatusCode, string(bodyBytes))
		}

		var tagsResponse struct {
			Results []struct {
				ID   int    `json:"id"`
				Name string `json:"name"`
			} `json:"results"`
			Next string `json:"next"`
		}

		err = json.NewDecoder(resp.Body).Decode(&tagsResponse)
		if err != nil {
			return nil, err
		}

		for _, tag := range tagsResponse.Results {
			tagIDMapping[tag.Name] = tag.ID
		}

		// Extract relative path from the Next URL
		if tagsResponse.Next != "" {
			nextURL := tagsResponse.Next
			if strings.HasPrefix(nextURL, "http") {
				// Extract just the path portion from the full URL
				if parsedURL, err := url.Parse(nextURL); err == nil {
					path = strings.TrimPrefix(parsedURL.Path, "/")
					if parsedURL.RawQuery != "" {
						path += "?" + parsedURL.RawQuery
					}
				} else {
					return nil, fmt.Errorf("failed to parse next URL: %v", err)
				}
			} else {
				path = strings.TrimPrefix(nextURL, "/")
			}
		} else {
			path = ""
		}
	}

	return tagIDMapping, nil
}

// GetDocumentCountByTag checks if there is a document for the specified tag (it is much faster than api/documents/)
func (client *PaperlessClient) GetDocumentCountByTag(ctx context.Context, tag string) (int, error) {
	path := fmt.Sprintf("api/tags/?name__iexact=%s", url.QueryEscape(tag))

	resp, err := client.Do(ctx, "GET", path, nil)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("error fetching tags: %d, %s", resp.StatusCode, string(bodyBytes))
	}

	var tagsResponse struct {
		Results []struct {
			DocumentCount int `json:"document_count"`
		} `json:"results"`
		Count int `json:"count"`
	}

	err = json.NewDecoder(resp.Body).Decode(&tagsResponse)
	if err != nil {
		return 0, err
	}

	if tagsResponse.Count == 0 {
		return 0, nil
	}

	return tagsResponse.Results[0].DocumentCount, nil
}

// GetDocumentsByTag retrieves documents that match the specified tag
func (client *PaperlessClient) GetDocumentsByTag(ctx context.Context, tag string, pageSize int) ([]Document, error) {
	documentCount, err := client.GetDocumentCountByTag(ctx, tag)
	if err != nil {
		return nil, fmt.Errorf("error checking document count for tag %s: %w", tag, err)
	}
	if documentCount == 0 {
		return []Document{}, nil
	}

	path := fmt.Sprintf("api/documents/?tags__name__iexact=%s&page_size=%d&full_perms=true", url.QueryEscape(tag), pageSize)

	resp, err := client.Do(ctx, "GET", path, nil)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed in GetDocumentsByTag: %w", err)
	}
	defer resp.Body.Close()

	// Read the response body
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		log.WithFields(logrus.Fields{
			"status_code": resp.StatusCode,
			"path":        path,
			"response":    string(bodyBytes),
			"headers":     resp.Header,
		}).Error("Error response from server in GetDocumentsByTag")
		return nil, fmt.Errorf("error searching documents: status=%d, body=%s", resp.StatusCode, string(bodyBytes))
	}

	var documentsResponse GetDocumentsApiResponse
	err = json.Unmarshal(bodyBytes, &documentsResponse)
	if err != nil {
		log.WithFields(logrus.Fields{
			"response_body": string(bodyBytes),
			"error":         err,
		}).Error("Failed to parse JSON response in GetDocumentsByTag")
		return nil, fmt.Errorf("failed to parse JSON response: %w", err)
	}

	allTags, err := client.GetAllTags(ctx)
	if err != nil {
		return nil, err
	}

	allCorrespondents, err := client.GetAllCorrespondents(ctx)
	if err != nil {
		return nil, err
	}

	documents := make([]Document, 0, len(documentsResponse.Results))
	for _, result := range documentsResponse.Results {
		tagNames, invisibleTagIDs := resolveTagNames(result.Tags, allTags)
		if len(invisibleTagIDs) > 0 {
			log.Warnf("Document %d has tag IDs %v that are not visible to the API user; they will be preserved but cannot be managed.", result.ID, invisibleTagIDs)
		}

		correspondentName := ""
		if result.Correspondent != 0 {
			for name, id := range allCorrespondents {
				if result.Correspondent == id {
					correspondentName = name
					break
				}
			}
		}

		documents = append(documents, Document{
			ID:               result.ID,
			Title:            result.Title,
			Content:          result.Content,
			Correspondent:    correspondentName,
			Tags:             tagNames,
			TagIDs:           result.Tags,
			Owner:            result.Owner,
			Permissions:      result.Permissions,
			CreatedDate:      result.paperlessCreatedDate(),
			OriginalFileName: result.OriginalFileName,
		})
	}

	return documents, nil
}

// GetSimilarDocuments retrieves documents similar to the given document, excluding paperless-gpt workflow tags.
func (client *PaperlessClient) GetSimilarDocuments(ctx context.Context, documentID int, count int) ([]Document, error) {
	// Similar documents are optional context for title generation, and the
	// caller treats an error as "none available". Types that embed a nil
	// *PaperlessClient pick this method up by promotion, so answer them with
	// an error rather than a nil-pointer panic.
	if client == nil {
		return nil, errors.New("paperless client is not configured")
	}
	availableTags, err := client.GetAllTags(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get tags for exclusion: %w", err)
	}

	// Exclude every case variant of a system tag, not just one: a database
	// holding both "paperless-gpt" and "Paperless-GPT" must not let documents
	// carrying the other spelling back in as examples.
	var excludedTagIDs []string
	for _, tagName := range protectedWorkflowTags() {
		if tagName == "" {
			continue
		}
		for availableName, tagID := range availableTags {
			if strings.EqualFold(availableName, tagName) {
				excludedTagIDs = append(excludedTagIDs, strconv.Itoa(tagID))
			}
		}
	}
	// Map order is random; keep the query string stable.
	sort.Strings(excludedTagIDs)

	path := fmt.Sprintf("api/documents/?more_like_id=%d&page_size=%d&ordering=-score&truncate_content=true", documentID, count)
	if len(excludedTagIDs) > 0 {
		path += "&tags__id__none=" + strings.Join(excludedTagIDs, ",")
	}

	resp, err := client.Do(ctx, "GET", path, nil)
	if err != nil {
		return nil, fmt.Errorf("error searching similar documents: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("error searching similar documents: %d, %s", resp.StatusCode, string(bodyBytes))
	}

	var response GetDocumentsApiResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("failed to parse similar documents response: %w", err)
	}

	documents := make([]Document, 0, len(response.Results))
	for _, result := range response.Results {
		documents = append(documents, Document{
			ID:               result.ID,
			Title:            result.Title,
			Content:          result.Content,
			CreatedDate:      result.paperlessCreatedDate(),
			OriginalFileName: result.OriginalFileName,
		})
	}

	return documents, nil
}

// DownloadPDF downloads the PDF file of the specified document
func (client *PaperlessClient) DownloadPDF(ctx context.Context, document Document) ([]byte, error) {
	path := fmt.Sprintf("api/documents/%d/download/", document.ID)
	resp, err := client.Do(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("error downloading document %d: %d, %s", document.ID, resp.StatusCode, string(bodyBytes))
	}

	return io.ReadAll(resp.Body)
}

// GetDocumentThumbnail fetches the rendered thumbnail image for a document.
func (client *PaperlessClient) GetDocumentThumbnail(ctx context.Context, documentID int) ([]byte, string, error) {
	path := fmt.Sprintf("api/documents/%d/thumb/", documentID)
	resp, err := client.Do(ctx, "GET", path, nil)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, "", fmt.Errorf("error fetching thumbnail for document %d: %d, %s", documentID, resp.StatusCode, string(bodyBytes))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "image/png"
	}
	return data, contentType, nil
}

// ErrDocumentNotFound is returned by GetDocument when paperless-ngx answers
// 404: the document was deleted, or the API user can't see it.
var ErrDocumentNotFound = errors.New("document not found")

func (client *PaperlessClient) GetDocument(ctx context.Context, documentID int) (Document, error) {
	// TODO: This function can be optimized by caching the results of GetAllTags, GetAllCorrespondents, and GetCustomFields.
	// A simple time-based cache could be implemented in the PaperlessClient to avoid fetching this data on every call.
	path := fmt.Sprintf("api/documents/%d/?full_perms=true", documentID)
	resp, err := client.Do(ctx, "GET", path, nil)
	if err != nil {
		return Document{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return Document{}, fmt.Errorf("error fetching document %d: %w", documentID, ErrDocumentNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return Document{}, fmt.Errorf("error fetching document %d: %d, %s", documentID, resp.StatusCode, string(bodyBytes))
	}

	var documentResponse GetDocumentApiResponse
	err = json.NewDecoder(resp.Body).Decode(&documentResponse)
	if err != nil {
		return Document{}, err
	}

	allTags, err := client.GetAllTags(ctx)
	if err != nil {
		return Document{}, err
	}

	allCorrespondents, err := client.GetAllCorrespondents(ctx)
	if err != nil {
		return Document{}, err
	}

	allCustomFields, err := client.GetCustomFields(ctx)
	if err != nil {
		return Document{}, err
	}
	customFieldMap := make(map[int]string)
	for _, field := range allCustomFields {
		customFieldMap[field.ID] = field.Name
	}

	for i, cf := range documentResponse.CustomFields {
		if name, ok := customFieldMap[cf.Field]; ok {
			documentResponse.CustomFields[i].Name = name
		}
	}

	// Match tag IDs to tag names
	tagNames, invisibleTagIDs := resolveTagNames(documentResponse.Tags, allTags)
	if len(invisibleTagIDs) > 0 {
		log.Warnf("Document %d has tag IDs %v that are not visible to the API user; they will be preserved but cannot be managed.", documentResponse.ID, invisibleTagIDs)
	}

	// Match correspondent ID to correspondent name
	correspondentName := ""
	for name, id := range allCorrespondents {
		if documentResponse.Correspondent == id {
			correspondentName = name
			break
		}
	}

	// Get all document types to find the name
	allDocumentTypes, err := client.GetAllDocumentTypes(ctx)
	if err != nil {
		return Document{}, err
	}
	documentTypeName := ""
	for _, docType := range allDocumentTypes {
		if documentResponse.DocumentType == docType.ID {
			documentTypeName = docType.Name
			break
		}
	}

	return Document{
		ID:               documentResponse.ID,
		Title:            documentResponse.Title,
		Content:          documentResponse.Content,
		Correspondent:    correspondentName,
		Tags:             tagNames,
		TagIDs:           documentResponse.Tags,
		CreatedDate:      documentResponse.paperlessCreatedDate(),
		OriginalFileName: documentResponse.OriginalFileName,
		CustomFields:     documentResponse.CustomFields,
		Owner:            documentResponse.Owner,
		Permissions:      documentResponse.Permissions,
		DocumentTypeName: documentTypeName,
	}, nil
}

// getCurrentCustomFields fetches only the document's custom fields. Unlike
// GetDocument it does not resolve tag, correspondent or document-type names,
// so a failure in those auxiliary lookups cannot block a custom-fields merge.
func (client *PaperlessClient) getCurrentCustomFields(ctx context.Context, documentID int) ([]CustomFieldResponse, error) {
	path := fmt.Sprintf("api/documents/%d/", documentID)
	resp, err := client.Do(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("error fetching document %d: %d, %s", documentID, resp.StatusCode, string(bodyBytes))
	}

	var documentResponse GetDocumentApiResponse
	if err := json.NewDecoder(resp.Body).Decode(&documentResponse); err != nil {
		return nil, err
	}
	return documentResponse.CustomFields, nil
}

// marshalTagsForHistory encodes tag names as JSON for ModificationHistory storage.
// JSON keeps the History display (JSON.parse) and undo (json.Unmarshal) working,
// unlike fmt.Sprintf("%v") which produces Go syntax like "[a b]".
func marshalTagsForHistory(tags []string) string {
	if tags == nil {
		tags = []string{}
	}
	b, err := json.Marshal(tags)
	if err != nil {
		return fmt.Sprintf("%v", tags)
	}
	return string(b)
}

// UpdateDocuments updates the specified documents with suggested changes
func (client *PaperlessClient) UpdateDocuments(ctx context.Context, documents []DocumentSuggestion, db *gorm.DB, isUndo bool) error {
	availableTags, err := client.GetAllTags(ctx)
	if err != nil {
		return fmt.Errorf("error fetching available tags: %w", err)
	}

	availableCorrespondents, err := client.GetAllCorrespondents(ctx)
	if err != nil {
		return fmt.Errorf("error fetching available correspondents: %w", err)
	}

	// Build document type name -> ID map
	documentTypes, err := client.GetAllDocumentTypes(ctx)
	if err != nil {
		return fmt.Errorf("error fetching available document types: %w", err)
	}
	availableDocumentTypes := make(map[string]int)
	for _, dt := range documentTypes {
		availableDocumentTypes[dt.Name] = dt.ID
	}

	// Reverse lookup for the tag catalog, used to decide which of a
	// document's tag IDs were resolvable to a name when the document was
	// fetched: an ID whose name is absent from originalDoc.Tags was never
	// nameable for this suggestion, so it must be preserved verbatim in tag
	// updates rather than inferred from the catalog state at update time.
	idToTagName := make(map[int]string, len(availableTags))
	for name, tagID := range availableTags {
		idToTagName[tagID] = name
	}

	// Build a field-id -> data_type map once per call, lazily — only fetch
	// when at least one document has suggested custom fields, so users who
	// don't use the feature pay nothing. Used to normalize monetary values
	// at the API boundary so paperless-ngx's validator accepts them
	// (see normalize_monetary.go), and to skip values it would reject.
	var customFieldTypes map[int]string
	for _, d := range documents {
		if len(d.SuggestedCustomFields) > 0 {
			allFields, err := client.GetCustomFields(ctx)
			if err != nil {
				return fmt.Errorf("error fetching custom fields for normalization: %w", err)
			}
			customFieldTypes = make(map[int]string, len(allFields))
			for _, f := range allFields {
				customFieldTypes[f.ID] = f.DataType
			}
			break
		}
		break
	}

	var firstPartial *PartialUpdateError

	for _, document := range documents {
		documentID := document.ID
		originalDoc := document.OriginalDocument
		updatedFields := make(map[string]interface{})
		originalFields := make(map[string]interface{})
		var partialDroppedFields []string

		// isHandoverTag reports whether a tag marks the document as waiting for
		// paperless-gpt and has to come off once it is processed: the global
		// trigger tags plus whatever the suggestion asks to remove (a
		// workflow's trigger tag). AddTags win over it, matching the order of
		// the main tag pass below.
		isHandoverTag := func(tagName string) bool {
			for _, added := range document.AddTags {
				if strings.EqualFold(tagName, added) {
					return false
				}
			}
			for _, t := range [...]string{autoTag, manualTag, autoOcrTag} {
				if strings.EqualFold(tagName, t) {
					return true
				}
			}
			for _, t := range document.RemoveTags {
				if strings.EqualFold(tagName, t) {
					return true
				}
			}
			return false
		}

		// The tag names the original snapshot resolved. Tag IDs in
		// originalDoc.TagIDs whose (current) catalog name is not in this set
		// were invisible when the document was fetched and are preserved
		// verbatim by every tag-writing path below.
		originalTagNames := make(map[string]bool, len(originalDoc.Tags))
		for _, tagName := range originalDoc.Tags {
			originalTagNames[tagName] = true
		}

		// --- TAGS ---
		finalTagNames := append([]string(nil), originalDoc.Tags...)
		if len(document.SuggestedTags) > 0 {
			if document.KeepOriginalTags {
				finalTagNames = append(finalTagNames, document.SuggestedTags...)
			} else {
				finalTagNames = append([]string{}, document.SuggestedTags...)
				// Suggestions never contain paperless-gpt's own tags (they are
				// filtered out of the LLM's view), so replacing the tags with
				// them used to drop PDF_OCR_COMPLETE_TAG, AUTO_TAG_COMPLETE,
				// FAIL_TAG or a workflow's completion tag already on the
				// document (#1156). Keep those. An undo restores an exact
				// earlier state.
				if !isUndo {
					finalTagNames = append(finalTagNames, keptSystemTags(originalDoc.Tags)...)
				}
			}
		}
		var cleanedTags []string
		for _, tagName := range finalTagNames {
			isRemoved := false
			for _, tagToRemove := range document.RemoveTags {
				if strings.EqualFold(tagName, tagToRemove) {
					isRemoved = true
					break
				}
			}
			if !isRemoved {
				cleanedTags = append(cleanedTags, tagName)
			}
		}
		finalTagNames = cleanedTags

		// Tags paperless-gpt adds mechanically to mark a document as done
		// (AUTO_TAG_COMPLETE). These are applied *after* the RemoveTags pass on
		// purpose: the trigger tag being removed and the completion tag being
		// added are two halves of the same handover, and when a user configures
		// both to the same name the completion tag has to win — otherwise the
		// document ends up with neither and looks unprocessed.
		finalTagNames = append(finalTagNames, document.AddTags...)

		// Case-insensitive compaction: also trims and drops empties, and matches
		// the case-insensitive comparison the RemoveTags pass above uses.
		finalTagNames = compactTagNamesCaseInsensitive(finalTagNames)

		log.Debugf("Document %d: Final tag names after compacting: %v", documentID, finalTagNames)

		// appliedTagNames tracks the tag names actually sent via PATCH, for
		// human-readable history (#842). It can differ from finalTagNames when
		// a suggested tag does not exist and CREATE_NEW_TAGS is disabled, or
		// when creation fails.
		var appliedTagNames []string
		// NOTE: this will dump the OCR complete tag if it doesn't exist in paperless-ngx
		if !hasSameTags(originalDoc.Tags, finalTagNames) {
			var finalTagIDs []int
			for _, tagName := range finalTagNames {
				// lookupTagID is case-insensitive and deterministic. Record the spelling
				// paperless-ngx actually holds, not the suggestion's variant.
				if availableTagName, tagID, exists := lookupTagID(availableTags, tagName); exists {
					finalTagIDs = append(finalTagIDs, tagID)
					appliedTagNames = append(appliedTagNames, availableTagName)
					continue
				}

				// paperless-gpt's own workflow tags are always created when missing,
				// under their canonical name: dropping one would leave the document
				// looking unprocessed. Other unknown tags need tag creation enabled.
				canonicalSystemTag, isSystemTag := canonicalProtectedWorkflowTag(tagName)
				if !isSystemTag && !tagsAutoCreateEnabled() {
					log.Debugf("Document %d: skipping unknown non-system tag %q", documentID, tagName)
					continue
				}
				if isSystemTag {
					tagName = canonicalSystemTag
				}

				objPerms := client.getPermissionsOrWarn(ctx, &originalDoc, "Document %d: Failed to get permissions for new tag %q", documentID, tagName)
				tagID, err := client.CreateTag(ctx, tagName, objPerms)
				if err != nil {
					if isSystemTag {
						return fmt.Errorf("error creating missing system tag %q for document %d: %w", tagName, documentID, err)
					}
					log.Warnf("Document %d: Failed to create new tag %q: %v", documentID, tagName, err)
					continue
				}

				availableTags[tagName] = tagID
				finalTagIDs = append(finalTagIDs, tagID)
				appliedTagNames = append(appliedTagNames, tagName)
				log.Infof("Document %d: created new tag %q with ID %d", documentID, tagName, tagID)
			}
			// Re-attach tag IDs that had no name in the original snapshot:
			// they cannot appear in finalTagNames, but they are still on the
			// document and were never meant to be touched. Checking against
			// the snapshot's names (not current visibility) also preserves a
			// tag that became visible between the fetch and this update.
			for _, tagID := range originalDoc.TagIDs {
				name, known := idToTagName[tagID]
				if !known || !originalTagNames[name] {
					finalTagIDs = append(finalTagIDs, tagID)
				}
			}
			// Only update tags if there are remaining tags after changes
			// Sending an empty tags array causes Paperless-NGX to return an error
			// However, we need to track this for a potential second update
			if len(finalTagIDs) > 0 {
				originalFields["tags"] = originalDoc.Tags
				updatedFields["tags"] = finalTagIDs
			} else {
				// Mark that we need to remove tags but can't do it in this update
				// We'll handle this after the main update completes
				originalFields["tags"] = originalDoc.Tags
			}
		}

		// --- CORRESPONDENT ---
		// With PRESERVE_EXISTING_METADATA, a correspondent that is already set
		// wins over the suggestion. That leaves paperless-ngx' own classifier (or
		// a manual correction) in charge and limits the LLM to documents that do
		// not have a correspondent yet.
		if document.SuggestedCorrespondent != "" && !strings.EqualFold(strings.TrimSpace(document.SuggestedCorrespondent), originalDoc.Correspondent) &&
			!(preserveExistingMetadata && originalDoc.Correspondent != "") {
			if isNoMatchCorrespondent(document.SuggestedCorrespondent) {
				// The correspondent prompt answers "Unknown" when nothing fits.
				// That is a valid answer meaning "no correspondent", not a
				// failure: leave the correspondent alone and do not flag the
				// document, and never create a correspondent named "Unknown".
				log.Infof("Document %d: no correspondent found (%q), leaving it unchanged", documentID, strings.TrimSpace(document.SuggestedCorrespondent))
			} else if _, corrID, exists := lookupNamedID("Correspondent", availableCorrespondents, document.SuggestedCorrespondent); exists {
				originalFields["correspondent"] = originalDoc.Correspondent
				updatedFields["correspondent"] = corrID
			} else {
				objPerms := client.getPermissionsOrWarn(ctx, &originalDoc, "Correspondent %s: Failed to get permissions", document.SuggestedCorrespondent)
				newCorr := instantiateCorrespondent(strings.TrimSpace(document.SuggestedCorrespondent))
				newCorrID, err := client.CreateOrGetCorrespondent(ctx, newCorr, objPerms)
				if err != nil {
					// Losing the correspondent must not cost the title, tags
					// and everything else: apply the rest and flag the document
					// like any other field paperless-ngx rejected. A typical
					// cause is a correspondent the API user cannot see.
					log.Warnf("Document %d: could not use correspondent %q, applying the other changes without it: %v", documentID, document.SuggestedCorrespondent, err)
					partialDroppedFields = append(partialDroppedFields, "correspondent")
				} else {
					originalFields["correspondent"] = originalDoc.Correspondent
					updatedFields["correspondent"] = newCorrID
				}
			}
		}

		// --- DOCUMENT TYPE ---
		// Same as above: an existing document type is kept when
		// PRESERVE_EXISTING_METADATA is enabled.
		if document.SuggestedDocumentType != "" && !strings.EqualFold(strings.TrimSpace(document.SuggestedDocumentType), originalDoc.DocumentTypeName) &&
			!(preserveExistingMetadata && originalDoc.DocumentTypeName != "") {
			originalFields["document_type"] = originalDoc.DocumentTypeName
			if _, docTypeID, exists := lookupNamedID("Document type", availableDocumentTypes, document.SuggestedDocumentType); exists {
				updatedFields["document_type"] = docTypeID
			} else {
				// Unlike correspondents, we don't create new document types - only use existing ones
				log.Warnf("Document type '%s' not found in available types, skipping for document %d", document.SuggestedDocumentType, documentID)
			}
		}

		// --- TITLE ---
		suggestedTitle := document.SuggestedTitle
		if len(suggestedTitle) > 128 {
			suggestedTitle = suggestedTitle[:128]
		}
		if suggestedTitle != "" && suggestedTitle != originalDoc.Title {
			originalFields["title"] = originalDoc.Title
			updatedFields["title"] = suggestedTitle
		}

		// --- CREATED DATE ---
		suggestedCreatedDate := document.SuggestedCreatedDate
		if suggestedCreatedDate != "" {
			// Validate that the date is a real Gregorian calendar date, not just
			// a correctly-formatted string. time.Parse rejects impossible dates
			// like "2023-01-79" (day 79) that the regex `^\d{4}-\d{2}-\d{2}$`
			// would accept. Year-first dates written with dots or slashes, or
			// with a trailing time, are rewritten to YYYY-MM-DD first: that is
			// the only format paperless-ngx accepts, and a dotted date used to
			// be dropped here after the model had already found the right day.
			// Dropped pre-flight rather than after a 400 to avoid an unnecessary
			// round-trip. A date the model cannot produce is a deterministic
			// outcome — retrying the document will yield the same answer every
			// time — so the field is skipped like an empty suggestion, not
			// recorded as a dropped field for the fail tag. The fail tag stays
			// reserved for fields paperless-ngx actually rejected.
			normalizedCreatedDate := normalizeCreatedDate(suggestedCreatedDate)
			if _, err := time.Parse("2006-01-02", normalizedCreatedDate); err == nil {
				originalFields["created_date"] = document.OriginalDocument.CreatedDate
				updatedFields["created_date"] = normalizedCreatedDate
			} else {
				log.Warnf("Document %d: created_date %q is not a valid YYYY-MM-DD calendar date, skipping. (%v)", documentID, suggestedCreatedDate, err)
			}
		}

		// --- CONTENT ---
		if document.SuggestedContent != "" && document.SuggestedContent != originalDoc.Content {
			originalFields["content"] = originalDoc.Content
			updatedFields["content"] = document.SuggestedContent
		}

		// --- CUSTOM FIELDS ---
		// Normalize each suggested value for its field's data type, then drop
		// the ones paperless-ngx would reject anyway. One invalid value makes
		// paperless-ngx refuse the whole PATCH, so it is skipped here like the
		// created date above: pre-flight, with a warning, and not recorded as
		// a dropped field for the fail tag. A typical case is an impossible
		// date the document itself prints ("pay by 2026-09-31"): the model
		// copies it faithfully and will do so on every retry.
		var suggestedCustomFields []CustomFieldSuggestion
		for _, sf := range document.SuggestedCustomFields {
			dataType := customFieldTypes[sf.ID]
			value := normalizeCustomFieldValue(dataType, sf.Value)
			if err := validateCustomFieldValue(dataType, value); err != nil {
				log.Warnf("Document %d: custom field %q (id %d) value %v is not valid for its %s type, skipping. (%v)", documentID, sf.Name, sf.ID, sf.Value, dataType, err)
				continue
			}
			sf.Value = value
			suggestedCustomFields = append(suggestedCustomFields, sf)
		}
		if len(suggestedCustomFields) > 0 {
			log.Infof("Processing custom fields for document %d with mode: '%s'", documentID, document.CustomFieldsWriteMode)

			// append and update merge the suggestion into the document's
			// existing custom fields, and paperless-ngx replaces the whole
			// custom_fields array on PATCH. OriginalDocument does not always
			// carry that state — auto-tag documents arrive via the list
			// endpoint, which returns no custom fields, and a manual
			// suggestion can be stale by the time it is applied. Merging
			// against an empty or outdated list silently deletes every field
			// we did not process, so fetch the current state first — and
			// refuse the update if that fetch fails, since the fallback
			// would carry the same silent-deletion risk. "replace" needs no
			// merge base — it discards existing fields by design.
			existingFields := originalDoc.CustomFields
			if document.CustomFieldsWriteMode != "replace" {
				// GetDocument would also work here, but it hard-fails on
				// auxiliary lookups (tags, correspondents, document types)
				// that this merge does not need — fetch just the fields.
				currentFields, err := client.getCurrentCustomFields(ctx, documentID)
				if err != nil {
					return fmt.Errorf("error updating document %d: could not fetch current custom fields for merge: %w", documentID, err)
				}
				existingFields = currentFields
			}
			finalCustomFields := slices.Clone(existingFields)
			originalCustomFieldsJSON, _ := json.Marshal(existingFields)

			switch document.CustomFieldsWriteMode {
			case "replace":
				finalCustomFields = []CustomFieldResponse{}
				for _, sf := range suggestedCustomFields {
					finalCustomFields = append(finalCustomFields, CustomFieldResponse{Field: sf.ID, Value: sf.Value})
				}
			case "update":
				existingFieldsMap := make(map[int]*CustomFieldResponse)
				for i := range finalCustomFields {
					existingFieldsMap[finalCustomFields[i].Field] = &finalCustomFields[i]
				}
				for _, sf := range suggestedCustomFields {
					if ef, ok := existingFieldsMap[sf.ID]; ok {
						ef.Value = sf.Value
					} else {
						finalCustomFields = append(finalCustomFields, CustomFieldResponse{Field: sf.ID, Value: sf.Value})
					}
				}
			case "append":
				existingFieldsMap := make(map[int]bool)
				for _, f := range finalCustomFields {
					existingFieldsMap[f.Field] = true
				}
				for _, sf := range suggestedCustomFields {
					if _, exists := existingFieldsMap[sf.ID]; !exists {
						finalCustomFields = append(finalCustomFields, CustomFieldResponse{Field: sf.ID, Value: sf.Value})
					}
				}
			}

			finalCustomFieldsJSON, _ := json.Marshal(finalCustomFields)
			if string(originalCustomFieldsJSON) != string(finalCustomFieldsJSON) {
				originalFields["custom_fields"] = string(originalCustomFieldsJSON)
				updatedFields["custom_fields"] = finalCustomFields
			}
		}

		if len(updatedFields) == 0 {
			log.Infof("No fields to update for document %d.", documentID)
			// Still need to remove the auto-tag if it exists
			if slices.ContainsFunc(originalDoc.Tags, isHandoverTag) {
				var finalTagIDs []int
				seenTagIDs := make(map[int]bool)
				appendTagID := func(tagName string) {
					_, tagID, exists := lookupTagID(availableTags, tagName)
					if !exists || seenTagIDs[tagID] {
						return
					}
					seenTagIDs[tagID] = true
					finalTagIDs = append(finalTagIDs, tagID)
				}
				for _, tagName := range originalDoc.Tags {
					if !isHandoverTag(tagName) {
						appendTagID(tagName)
					}
				}
				// Preserve tag IDs that had no name in the original
				// snapshot — the name loop above could never keep them.
				for _, tagID := range originalDoc.TagIDs {
					name, known := idToTagName[tagID]
					if (!known || !originalTagNames[name]) && !seenTagIDs[tagID] {
						seenTagIDs[tagID] = true
						finalTagIDs = append(finalTagIDs, tagID)
					}
				}
				// Tags paperless-gpt adds mechanically (AUTO_TAG_COMPLETE) have
				// to be applied on this path too, and after the trigger-tag
				// removal above so an added tag wins a name collision. Without
				// this, a document whose suggestions happened to match what it
				// already had would lose its trigger tag and never gain a
				// completion tag — it just looks unprocessed.
				for _, tagName := range document.AddTags {
					appendTagID(tagName)
				}
				// Mark that we need to remove tags
				// We'll send the tag update directly (even if empty) since there are no other field changes
				originalFields["tags"] = originalDoc.Tags
				if len(finalTagIDs) > 0 {
					updatedFields["tags"] = finalTagIDs
				} else {
					// Document only had auto/manual tags with no other changes
					// We need to send an empty tags array to remove the manual tag
					log.Infof("Document %d: Removing manual/auto tag (only tag present, no other changes)", documentID)
					updatedFields["tags"] = []int{}
				}
			} else {
				if err := client.addSummaryAfterSkippedMetadata(
					ctx,
					documentID,
					document.SuggestedSummary,
					db,
					isUndo,
					&firstPartial,
					partialDroppedFields,
				); err != nil {
					return fmt.Errorf("error adding summary note for document %d: %w", documentID, err)
				}
				continue
			}
		}

		log.Debugf("Document %d: Fields to update: %v", documentID, updatedFields)
		path := fmt.Sprintf("api/documents/%d/", documentID)

		patchSkipped, partialDroppedFields, err := client.patchDocumentWithValidationRecovery(ctx, path, documentID, updatedFields, originalFields, partialDroppedFields)
		if err != nil {
			return err
		}
		if patchSkipped {
			if err := client.addSummaryAfterSkippedMetadata(
				ctx,
				documentID,
				document.SuggestedSummary,
				db,
				isUndo,
				&firstPartial,
				partialDroppedFields,
			); err != nil {
				return fmt.Errorf("error adding summary note for document %d: %w", documentID, err)
			}
			continue
		}
		if len(partialDroppedFields) > 0 {
			log.Warnf("Document %d updated partially after dropping invalid fields: %v", documentID, partialDroppedFields)
			setPartialUpdate(&firstPartial, documentID, partialDroppedFields)
		}

		// Check if we need to remove auto/manual tags in a separate update
		// This happens when tags changed but resulted in an empty array (which we can't send)
		if _, hadTagChange := originalFields["tags"]; hadTagChange {
			if _, tagsSent := updatedFields["tags"]; !tagsSent {
				// We detected a tag change but didn't send it because it would be empty
				// Now we need to remove the auto/manual tag using the bulk edit API
				log.Infof("Document %d: Removing auto/manual tag in separate update", documentID)

				// Get the current document state to see what tags it has now
				currentDoc, err := client.GetDocument(ctx, documentID)
				if err != nil {
					log.Warnf("Failed to get current document state for tag removal: %v", err)
				} else {
					// Remove auto/manual tags from current tags. Work on tag
					// IDs rather than names so tags the API user cannot see
					// survive: they have no resolvable name and would
					// otherwise be dropped here.
					workflowTagIDs := make(map[int]bool)
					for tagName, tagID := range availableTags {
						if isHandoverTag(tagName) {
							workflowTagIDs[tagID] = true
						}
					}
					var remainingTagIDs []int
					var remainingTagNames []string
					for _, tagID := range currentDoc.TagIDs {
						if workflowTagIDs[tagID] {
							continue
						}
						remainingTagIDs = append(remainingTagIDs, tagID)
						if tagName, ok := idToTagName[tagID]; ok {
							remainingTagNames = append(remainingTagNames, tagName)
						}
					}

					// Always send the tag update to remove auto/manual tags, even if it results in an empty array
					// This is required - the auto/manual tag MUST be removed
					// Ensure we send an empty array [] instead of null
					if remainingTagIDs == nil {
						remainingTagIDs = []int{}
					}
					tagUpdateFields := map[string]interface{}{
						"tags": remainingTagIDs,
					}
					tagJsonData, err := json.Marshal(tagUpdateFields)
					if err == nil {
						tagPath := fmt.Sprintf("api/documents/%d/", documentID)
						tagResp, err := client.Do(ctx, "PATCH", tagPath, bytes.NewBuffer(tagJsonData))
						if err == nil {
							defer tagResp.Body.Close()
							if tagResp.StatusCode == http.StatusOK {
								log.Infof("Document %d: Successfully removed auto/manual tag", documentID)
								// Record this tag change with tag names for both PreviousValue and NewValue
								mod := ModificationHistory{
									DocumentID:    documentID,
									ModField:      "tags",
									PreviousValue: marshalTagsForHistory(originalDoc.Tags),
									NewValue:      marshalTagsForHistory(remainingTagNames),
								}
								if err := InsertModification(db, &mod); err != nil {
									log.Warnf("Error inserting tag modification record: %v", err)
								}
							} else {
								bodyBytes, _ := io.ReadAll(tagResp.Body)
								log.Warnf("Failed to remove auto/manual tag: %d, %s", tagResp.StatusCode, string(bodyBytes))
							}
						}
					}
				}
			}
		}

		for field, value := range originalFields {
			// Only record what was actually sent: fields stripped after a
			// paperless-ngx 400 were not applied. Tags handled separately
			// above fall into the same rule.
			if _, ok := updatedFields[field]; !ok {
				continue
			}
			if field == "content" {
				log.Debugf("Document %d: Updated %s from %v to %v", documentID, field, value, updatedFields[field])
			} else {
				log.Printf("Document %d: Updated %s from %v to %v", documentID, field, value, updatedFields[field])
			}
			// History must show human-readable names on both sides (fix #842).
			// updatedFields holds IDs for tags/correspondent/document_type
			// (needed for the PATCH), while originalFields holds names.
			var prevStr, newStr string
			switch field {
			case "tags":
				prevStr = marshalTagsForHistory(originalDoc.Tags)
				newStr = marshalTagsForHistory(appliedTagNames)
			case "correspondent":
				prevStr = fmt.Sprintf("%v", value)
				newStr = document.SuggestedCorrespondent
			case "document_type":
				prevStr = fmt.Sprintf("%v", value)
				newStr = document.SuggestedDocumentType
			case "custom_fields":
				prevStr = fmt.Sprintf("%v", value)
				if b, err := json.Marshal(updatedFields[field]); err == nil {
					newStr = string(b)
				} else {
					newStr = fmt.Sprintf("%v", updatedFields[field])
				}
			default:
				prevStr = fmt.Sprintf("%v", value)
				newStr = fmt.Sprintf("%v", updatedFields[field])
			}
			mod := ModificationHistory{
				DocumentID:    documentID,
				ModField:      field,
				PreviousValue: prevStr,
				NewValue:      newStr,
			}
			if err := InsertModification(db, &mod); err != nil {
				return fmt.Errorf("error inserting modification record for document %d: %w", documentID, err)
			}
		}
		client.addSummaryAfterMetadata(ctx, documentID, document.SuggestedSummary, db, isUndo, &firstPartial)
		log.Printf("Document %d updated successfully.", documentID)
	}
	if firstPartial != nil {
		return firstPartial
	}
	return nil
}

func setPartialUpdate(firstPartial **PartialUpdateError, documentID int, droppedFields []string) {
	if len(droppedFields) == 0 {
		return
	}

	if *firstPartial == nil {
		*firstPartial = &PartialUpdateError{DocumentID: documentID}
	}
	if (*firstPartial).DocumentID != documentID {
		return
	}

	for _, field := range droppedFields {
		if !slices.Contains((*firstPartial).DroppedFields, field) {
			(*firstPartial).DroppedFields = append((*firstPartial).DroppedFields, field)
		}
	}
}

func (client *PaperlessClient) addSummaryAfterSkippedMetadata(
	ctx context.Context,
	documentID int,
	summary string,
	db *gorm.DB,
	isUndo bool,
	firstPartial **PartialUpdateError,
	droppedFields []string,
) error {
	if isUndo || summary == "" {
		setPartialUpdate(firstPartial, documentID, droppedFields)
		return nil
	}
	if err := client.addSummaryNoteAndHistory(ctx, documentID, summary, db); err != nil {
		return err
	}

	setPartialUpdate(firstPartial, documentID, droppedFields)
	log.Printf("Document %d summary note added successfully.", documentID)
	return nil
}

func (client *PaperlessClient) addSummaryAfterMetadata(
	ctx context.Context,
	documentID int,
	summary string,
	db *gorm.DB,
	isUndo bool,
	firstPartial **PartialUpdateError,
) {
	if isUndo || summary == "" {
		return
	}
	if err := client.addSummaryNoteAndHistory(ctx, documentID, summary, db); err != nil {
		log.Warnf("Document %d metadata updated, but adding the summary note failed: %v", documentID, err)
		setPartialUpdate(firstPartial, documentID, []string{"summary"})
		return
	}

	log.Printf("Document %d summary note added successfully.", documentID)
}

func (client *PaperlessClient) addSummaryNoteAndHistory(ctx context.Context, documentID int, summary string, db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("modification history database is required")
	}

	createdNote, err := client.CreateDocumentNote(ctx, documentID, summary)
	if err != nil {
		return err
	}

	modification := ModificationHistory{
		DocumentID:    documentID,
		ModField:      "summary",
		PreviousValue: "",
		NewValue:      summary,
		RemoteID:      &createdNote.ID,
	}
	if err := InsertModification(db, &modification); err != nil {
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if rollbackErr := client.DeleteDocumentNote(rollbackCtx, documentID, createdNote.ID); rollbackErr != nil {
			return fmt.Errorf("persisting summary history failed: %w; deleting note %d also failed: %v", err, createdNote.ID, rollbackErr)
		}
		return fmt.Errorf("persisting summary history failed and note %d was deleted: %w", createdNote.ID, err)
	}

	return nil
}

// encodePageJPEG encodes a page image as JPEG within IMAGE_MAX_FILE_BYTES:
// moderate quality reduction first (85 down to 60, to avoid artifacts that
// hurt OCR), then a proportional resize as a last resort. It returns the
// encoded bytes and the image actually encoded, which differs from the input
// when it had to be resized.
func encodePageJPEG(img image.Image) (*bytes.Buffer, image.Image, error) {
	buf := &bytes.Buffer{}
	if err := jpeg.Encode(buf, img, &jpeg.Options{Quality: jpeg.DefaultQuality}); err != nil {
		return nil, nil, err
	}
	// A non-positive limit means "no limit". Without this guard the resize
	// below computes a scale of 0 and produces an empty image.
	if imageMaxFileBytes <= 0 {
		return buf, img, nil
	}

	quality := jpeg.DefaultQuality
	for q := 85; buf.Len() > imageMaxFileBytes && q >= 60; q -= 5 {
		quality = q
		buf.Reset()
		if err := jpeg.Encode(buf, img, &jpeg.Options{Quality: q}); err != nil {
			return nil, nil, err
		}
	}

	if buf.Len() > imageMaxFileBytes {
		scale := math.Sqrt(float64(imageMaxFileBytes) / float64(buf.Len()))
		img = imaging.Resize(img,
			int(float64(img.Bounds().Dx())*scale),
			int(float64(img.Bounds().Dy())*scale),
			imaging.Lanczos)
		buf.Reset()
		if err := jpeg.Encode(buf, img, &jpeg.Options{Quality: quality}); err != nil {
			return nil, nil, err
		}
	}
	return buf, img, nil
}

// writeOriginalImageAsPage stores an original image download (served instead
// of a PDF when paperless-ngx has archive generation disabled) as the single
// page image the OCR providers consume.
//
// It is always re-encoded as JPEG rather than written through as-is: the LLM
// OCR provider labels every page image/jpeg, and providers that validate the
// media type (Anthropic does) reject a PNG sent under that label. Re-encoding
// also applies the same IMAGE_MAX_* limits as rendered PDF pages, so a large
// phone photo cannot bypass them and trip a 413 at the provider.
func writeOriginalImageAsPage(documentID int, docDir string, data []byte) (string, error) {
	jpegData, err := normalizeOriginalImage(documentID, data)
	if err != nil {
		return "", err
	}
	imagePath := filepath.Join(docDir, "page000.jpg")
	if err := os.WriteFile(imagePath, jpegData, 0644); err != nil {
		return "", err
	}
	return imagePath, nil
}

// normalizeOriginalImage decodes an original image download (any format the
// image decoders understand, with EXIF orientation applied), caps it to the
// IMAGE_MAX_* limits and re-encodes it as JPEG. See writeOriginalImageAsPage
// for why this is always JPEG.
func normalizeOriginalImage(documentID int, data []byte) ([]byte, error) {
	img, err := imaging.Decode(bytes.NewReader(data), imaging.AutoOrientation(true))
	if err != nil {
		return nil, fmt.Errorf("document %d: decoding original image: %w", documentID, err)
	}

	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	scale := 1.0
	if longest := math.Max(float64(w), float64(h)); imageMaxPixelDimension > 0 && longest > float64(imageMaxPixelDimension) {
		scale = float64(imageMaxPixelDimension) / longest
	}
	if total := float64(w) * float64(h) * scale * scale; imageMaxTotalPixels > 0 && total > float64(imageMaxTotalPixels) {
		scale *= math.Sqrt(float64(imageMaxTotalPixels) / total)
	}
	if scale < 1.0 {
		img = imaging.Resize(img, int(float64(w)*scale), int(float64(h)*scale), imaging.Lanczos)
	}

	buf, img, err := encodePageJPEG(img)
	if err != nil {
		return nil, err
	}
	log.Infof("Document %d: original image normalised to %dx%d JPEG, %d bytes", documentID, img.Bounds().Dx(), img.Bounds().Dy(), buf.Len())
	return buf.Bytes(), nil
}

// isPDFData reports whether downloaded bytes look like a PDF by magic number.
// Paperless-ngx may serve the original file instead of an archived PDF (e.g.
// when PAPERLESS_ARCHIVE_FILE_GENERATION=never), so the download is not
// always a PDF.
func isPDFData(data []byte) bool {
	return len(data) >= 4 && bytes.HasPrefix(data, []byte("%PDF"))
}

// describeDownload builds a short description of downloaded bytes for errors.
func describeDownload(data []byte) string {
	mime := mimetype.Detect(data)
	return mime.String()
}

// DownloadDocumentAsImages downloads the specified document and converts it to images.
// Single images pass through as one page; other non-PDF downloads fail with a
// descriptive error instead of a PDF parsing failure.
// If limitPages > 0, only the first N pages will be processed
// Returns the image paths and the total number of pages in the original document
func (client *PaperlessClient) DownloadDocumentAsImages(ctx context.Context, documentID int, limitPages int) ([]string, int, error) {
	// Create a directory named after the document ID
	docDir := filepath.Join(client.GetCacheFolder(), fmt.Sprintf("document-%d", documentID))
	if _, err := os.Stat(docDir); os.IsNotExist(err) {
		err = os.MkdirAll(docDir, 0750)
		if err != nil {
			return nil, 0, err
		}
	}

	// Proceed with downloading the document to get the total page count
	path := fmt.Sprintf("api/documents/%d/download/", documentID)
	resp, err := client.Do(ctx, "GET", path, nil)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, 0, fmt.Errorf("error downloading document %d: %d, %s", documentID, resp.StatusCode, string(bodyBytes))
	}

	pdfData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, err
	}

	// The download is not always a PDF: with
	// PAPERLESS_ARCHIVE_FILE_GENERATION=never paperless-ngx serves the
	// original file, which may be an image or another type.
	if !isPDFData(pdfData) {
		mime := describeDownload(pdfData)
		if strings.HasPrefix(mime, "image/") {
			log.Warnf("Document %d download is %s, not a PDF; processing as a single image", documentID, mime)
			imagePath, err := writeOriginalImageAsPage(documentID, docDir, pdfData)
			if err != nil {
				return nil, 0, err
			}
			return []string{imagePath}, 1, nil
		}
		return nil, 0, fmt.Errorf("document %d download is %s (%d bytes), not a PDF (hint: with PAPERLESS_ARCHIVE_FILE_GENERATION=never paperless-ngx serves the original file); image OCR mode supports images, PDF modes require a PDF", documentID, mime, len(pdfData))
	}

	doc, err := pdfrender.Open(ctx, pdfData)
	if err != nil {
		return nil, 0, err
	}
	defer doc.Close()

	totalPages := doc.NumPages()
	pagesToProcess := totalPages

	if limitPages > 0 && limitPages < totalPages {
		pagesToProcess = limitPages
	}

	// Check if images already exist
	var imagePaths []string
	for n := 0; n < pagesToProcess; n++ {
		imagePath := filepath.Join(docDir, fmt.Sprintf("page%03d.jpg", n))
		if _, err := os.Stat(imagePath); err == nil {
			// File exists
			imagePaths = append(imagePaths, imagePath)
		}
	}

	// If all images exist, return them
	if len(imagePaths) == pagesToProcess {
		return imagePaths, totalPages, nil
	}

	// Clear existing images to ensure consistency
	imagePaths = []string{}

	var mu sync.Mutex
	var g errgroup.Group

	for n := 0; n < pagesToProcess; n++ {
		n := n // capture loop variable
		g.Go(func() error {
			// DPI calculation constants
			const minDPI = 72 // Minimum DPI to ensure readable text

			mu.Lock() // a PDFium document is not safe for concurrent use
			wPts, hPts, err := doc.PageSize(n)
			if err != nil {
				mu.Unlock()
				return err
			}

			// Calculate DPI limits based on maximum allowed dimension and total pixels
			dpiSide := float64(imageMaxPixelDimension*72) / math.Max(wPts, hPts)
			dpiArea := math.Sqrt(float64(imageMaxTotalPixels) * 72 * 72 / (wPts * hPts))

			// Use the more restrictive of the two limits
			dpi := math.Min(dpiSide, dpiArea)

			// Ensure DPI stays within acceptable bounds
			dpi = math.Min(dpi, float64(imageMaxRenderDPI))
			dpi = math.Max(dpi, float64(minDPI))

			// Render the page at calculated DPI
			var img image.Image
			img, err = doc.RenderDPI(n, dpi)
			mu.Unlock()
			if err != nil {
				return err
			}

			buf, img, err := encodePageJPEG(img)
			if err != nil {
				return err
			}

			log.Infof("Document %d page %d: final image dimensions %dx%d, size %d bytes, DPI %.0f", documentID, n, img.Bounds().Dx(), img.Bounds().Dy(), buf.Len(), dpi)

			// Save image to file
			imagePath := filepath.Join(docDir, fmt.Sprintf("page%03d.jpg", n))
			// #nosec G304 -- imagePath is generated from an app-controlled cache directory and page index.
			f, err := os.Create(imagePath)
			if err != nil {
				return err
			}

			if _, err := f.Write(buf.Bytes()); err != nil {
				if closeErr := f.Close(); closeErr != nil {
					log.WithError(closeErr).WithField("image_path", imagePath).Warn("Failed to close image file after write error")
				}
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}

			// Verify the JPEG file
			// #nosec G304 -- imagePath is generated from an app-controlled cache directory and page index.
			file, err := os.Open(imagePath)
			if err != nil {
				return err
			}
			defer file.Close()

			_, err = jpeg.Decode(file)
			if err != nil {
				return fmt.Errorf("invalid JPEG file: %s", imagePath)
			}

			mu.Lock()
			imagePaths = append(imagePaths, imagePath)
			mu.Unlock()

			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, totalPages, err
	}

	// sort the image paths to ensure they are in order
	slices.Sort(imagePaths)

	return imagePaths, totalPages, nil
}

// DownloadDocumentAsPDF downloads the original file of the specified document and splits it into individual PDFs if needed.
// Non-PDF originals fail with a descriptive error (images pass through only
// when split=false, for whole-document consumers that sniff the content)
// If limitPages > 0, only the first N pages will be processed
// Returns the PDF paths, original PDF data, and the total number of pages in the original document
func (client *PaperlessClient) DownloadDocumentAsPDF(ctx context.Context, documentID int, limitPages int, split bool) ([]string, []byte, int, error) {
	// Create a directory named after the document ID
	docDir := filepath.Join(client.GetCacheFolder(), fmt.Sprintf("document-%d-pdf", documentID))
	if _, err := os.Stat(docDir); os.IsNotExist(err) {
		err = os.MkdirAll(docDir, 0750)
		if err != nil {
			return nil, nil, 0, err
		}
	}

	// Proceed with downloading the document
	path := fmt.Sprintf("api/documents/%d/download/?original=true", documentID)
	resp, err := client.Do(ctx, "GET", path, nil)
	if err != nil {
		return nil, nil, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, nil, 0, fmt.Errorf("error downloading document %d: %d, %s", documentID, resp.StatusCode, string(bodyBytes))
	}

	pdfData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, 0, err
	}

	// The original file is not necessarily a PDF (images, office docs, ...).
	if !isPDFData(pdfData) {
		mime := describeDownload(pdfData)
		if strings.HasPrefix(mime, "image/") && !split {
			// Whole-document consumers accept image bytes, so let them
			// proceed with a single page — normalised to JPEG for the same
			// media-type and size reasons as in image mode.
			log.Warnf("Document %d original is %s, not a PDF; proceeding with a single page", documentID, mime)
			jpegData, err := normalizeOriginalImage(documentID, pdfData)
			if err != nil {
				return nil, nil, 0, err
			}
			return []string{}, jpegData, 1, nil
		}
		return nil, nil, 0, fmt.Errorf("document %d original is %s (%d bytes), not a PDF; PDF OCR modes require a PDF, use image mode for images", documentID, mime, len(pdfData))
	}

	// Save the original PDF
	originalPDFPath := filepath.Join(docDir, "original.pdf")
	err = os.WriteFile(originalPDFPath, pdfData, 0600)
	if err != nil {
		return nil, nil, 0, err
	}

	// Get the number of pages in the PDF
	doc, err := pdfrender.Open(ctx, pdfData)
	if err != nil {
		return nil, nil, 0, err
	}
	totalPages := doc.NumPages()
	// Only the page count is needed here; free the PDFium instance early.
	doc.Close()
	pagesToProcess := totalPages

	if limitPages > 0 && limitPages < totalPages {
		pagesToProcess = limitPages
	}

	// If skipping splitting is requested, return early with empty paths array
	if !split {
		return []string{}, pdfData, totalPages, nil
	}

	// Continue with splitting logic only if we're not skipping it
	// Check if PDFs already exist (check for legacy formats for backward compatibility)
	var pdfPaths []string
	for n := 0; n < pagesToProcess; n++ {
		// Standardized format (preferred): original_001.pdf
		pdfPathStandard := filepath.Join(docDir, fmt.Sprintf("original_%03d.pdf", n+1))
		// Legacy format: original_1.pdf (from older versions or pdfcpu default output)
		pdfPathLegacy := filepath.Join(docDir, fmt.Sprintf("original_%d.pdf", n+1))

		if _, err := os.Stat(pdfPathStandard); err == nil {
			// Standardized format exists
			pdfPaths = append(pdfPaths, pdfPathStandard)
		} else if _, err := os.Stat(pdfPathLegacy); err == nil {
			// Legacy format exists
			pdfPaths = append(pdfPaths, pdfPathLegacy)
		}
	}

	// If all PDFs exist, return them
	if len(pdfPaths) == pagesToProcess {
		return pdfPaths, pdfData, totalPages, nil
	}

	// Clear existing PDFs to ensure consistency when regenerating
	files, err := filepath.Glob(filepath.Join(docDir, "original_*.pdf"))
	if err == nil {
		for _, file := range files {
			if removeErr := os.Remove(file); removeErr != nil {
				log.WithError(removeErr).WithField("path", file).Warn("Failed to remove stale split PDF")
			}
		}
	}

	// Limit the split source before splitting so OCR_LIMIT_PAGES also bounds
	// pdfcpu CPU, memory, and disk work for large source documents.
	splitSourcePath := originalPDFPath
	if pagesToProcess < totalPages {
		trimDir, err := os.MkdirTemp("", "pgpt-trim-*")
		if err != nil {
			return nil, nil, 0, fmt.Errorf("error creating temp dir for page-limited trim: %w", err)
		}
		defer func() {
			if removeErr := os.RemoveAll(trimDir); removeErr != nil {
				log.WithError(removeErr).WithField("path", trimDir).Warn("Failed to remove page-limit trim directory")
			}
		}()

		// Preserve the basename so pdfcpu emits original_1.pdf, etc.
		splitSourcePath = filepath.Join(trimDir, "original.pdf")
		selection := []string{fmt.Sprintf("1-%d", pagesToProcess)}
		if err := api.TrimFile(originalPDFPath, splitSourcePath, selection, nil); err != nil {
			return nil, nil, 0, fmt.Errorf("error trimming PDF to page limit: %w", err)
		}
	}

	err = api.SplitFile(splitSourcePath, docDir, 1, nil)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("error splitting PDF: %w", err)
	}

	// pdfcpu creates files with names like "original_1.pdf", "original_2.pdf", etc. (without leading zeros)
	// Rename them to our standardized format (original_001.pdf, original_002.pdf, etc.)
	pdfPaths = []string{}
	for n := 0; n < pagesToProcess; n++ {
		// pdfcpu default output format
		legacyFilePath := filepath.Join(docDir, fmt.Sprintf("original_%d.pdf", n+1))
		// Our standardized format
		standardFilePath := filepath.Join(docDir, fmt.Sprintf("original_%03d.pdf", n+1))

		// Check if the legacy file exists and rename it to standard format
		if _, err := os.Stat(legacyFilePath); err == nil {
			err = os.Rename(legacyFilePath, standardFilePath)
			if err != nil {
				return nil, nil, 0, fmt.Errorf("error renaming PDF to standard format: %w", err)
			}
			pdfPaths = append(pdfPaths, standardFilePath)
		} else {
			return nil, nil, 0, fmt.Errorf("expected split PDF not found: %s", legacyFilePath)
		}
	}

	// Sort the PDF paths to ensure they are in order
	sort.SliceStable(pdfPaths, func(i, j int) bool {
		// Extract the number from the filename (e.g., "original_001.pdf" -> 1, "original_1.pdf" -> 1)
		iBasename := filepath.Base(pdfPaths[i])
		jBasename := filepath.Base(pdfPaths[j])

		iParts := strings.Split(strings.TrimSuffix(iBasename, ".pdf"), "_")
		jParts := strings.Split(strings.TrimSuffix(jBasename, ".pdf"), "_")

		if len(iParts) < 2 || len(jParts) < 2 {
			return pdfPaths[i] < pdfPaths[j] // fallback to string comparison
		}

		// Parse the page numbers (handles both "001" and "1" formats)
		ni, errI := strconv.Atoi(iParts[1])
		nj, errJ := strconv.Atoi(jParts[1])

		if errI != nil || errJ != nil {
			return pdfPaths[i] < pdfPaths[j] // fallback to string comparison
		}

		return ni < nj
	})

	return pdfPaths, pdfData, totalPages, nil
}

// GetCacheFolder returns the cache folder for the PaperlessClient
func (client *PaperlessClient) GetCacheFolder() string {
	if client.CacheFolder == "" {
		client.CacheFolder = filepath.Join(os.TempDir(), "paperless-gpt")
	}
	return client.CacheFolder
}

// urlEncode encodes a string for safe URL usage
func urlEncode(s string) string {
	return strings.ReplaceAll(s, " ", "+")
}

// instantiateCorrespondent creates a new Correspondent object with default values
func instantiateCorrespondent(name string) Correspondent {
	return Correspondent{
		Name:              name,
		MatchingAlgorithm: 1,
		Match:             "",
		IsInsensitive:     true,
		Owner:             nil,
		SetPermissions:    nil,
	}
}

// CreateOrGetCorrespondent creates a new correspondent or returns existing one if name already exists
func (client *PaperlessClient) CreateOrGetCorrespondent(ctx context.Context, correspondent Correspondent, objPerms *ObjPermissions) (int, error) {
	// First try to find existing correspondent
	correspondents, err := client.GetAllCorrespondents(ctx)
	if err != nil {
		return 0, fmt.Errorf("error fetching correspondents: %w", err)
	}

	// Check if correspondent already exists; paperless-ngx names are unique
	// regardless of case.
	if _, id, exists := lookupNamedID("Correspondent", correspondents, correspondent.Name); exists {
		log.Infof("Using existing correspondent with name %s and ID %d", correspondent.Name, id)
		return id, nil
	}

	if objPerms != nil {
		correspondent.Owner = objPerms.Owner
		correspondent.SetPermissions = objPerms.SetPermissions
	}

	// If not found, create new correspondent
	url := "api/correspondents/"
	jsonData, err := json.Marshal(correspondent)
	if err != nil {
		return 0, err
	}

	resp, err := client.Do(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("error creating correspondent: %d, %s", resp.StatusCode, string(bodyBytes))
	}

	var createdCorrespondent struct {
		ID int `json:"id"`
	}
	err = json.NewDecoder(resp.Body).Decode(&createdCorrespondent)
	if err != nil {
		return 0, err
	}

	return createdCorrespondent.ID, nil
}

// CorrespondentResponse represents the response structure for correspondents
type CorrespondentResponse struct {
	Results []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"results"`
}

// GetAllCorrespondents retrieves all correspondents from the Paperless-NGX API
func (client *PaperlessClient) GetAllCorrespondents(ctx context.Context) (map[string]int, error) {
	correspondentIDMapping := make(map[string]int)
	path := "api/correspondents/?page_size=9999"

	resp, err := client.Do(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("error fetching correspondents: %d, %s", resp.StatusCode, string(bodyBytes))
	}

	var correspondentsResponse CorrespondentResponse

	err = json.NewDecoder(resp.Body).Decode(&correspondentsResponse)
	if err != nil {
		return nil, err
	}

	for _, correspondent := range correspondentsResponse.Results {
		correspondentIDMapping[correspondent.Name] = correspondent.ID
	}

	return correspondentIDMapping, nil
}

// GetAllDocumentTypes retrieves all document types from the Paperless-NGX API
func (client *PaperlessClient) GetAllDocumentTypes(ctx context.Context) ([]DocumentType, error) {
	var allDocumentTypes []DocumentType
	path := "api/document_types/?page_size=1000" // Assuming a reasonable limit

	resp, err := client.Do(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("error fetching document types: %d, %s", resp.StatusCode, string(bodyBytes))
	}

	var response struct {
		Results []DocumentType `json:"results"`
	}

	err = json.NewDecoder(resp.Body).Decode(&response)
	if err != nil {
		return nil, err
	}

	allDocumentTypes = append(allDocumentTypes, response.Results...)

	return allDocumentTypes, nil
}

// GetCustomFields retrieves all custom fields from the Paperless-NGX API
func (client *PaperlessClient) GetCustomFields(ctx context.Context) ([]CustomField, error) {
	var customFields []CustomField
	path := "api/custom_fields/"

	for path != "" {
		resp, err := client.Do(ctx, "GET", path, nil)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			bodyBytes, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("error fetching custom fields: %d, %s", resp.StatusCode, string(bodyBytes))
		}

		var response struct {
			Results []CustomField `json:"results"`
			Next    string        `json:"next"`
		}

		err = json.NewDecoder(resp.Body).Decode(&response)
		if err != nil {
			return nil, err
		}

		customFields = append(customFields, response.Results...)

		if response.Next != "" {
			nextURL, err := url.Parse(response.Next)
			if err != nil {
				return nil, fmt.Errorf("failed to parse next URL for custom fields: %w", err)
			}
			path = nextURL.Path + "?" + nextURL.RawQuery
		} else {
			path = ""
		}
	}

	return customFields, nil
}

// DeleteDocument deletes a document by its ID
func (client *PaperlessClient) DeleteDocument(ctx context.Context, documentID int) error {
	path := fmt.Sprintf("api/documents/%d/", documentID)
	resp, err := client.Do(ctx, "DELETE", path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("error deleting document %d: %d, %s", documentID, resp.StatusCode, string(bodyBytes))
	}

	return nil
}

func parseCreatedDocumentNote(body []byte, expectedNote string) (DocumentNote, error) {
	var candidates []DocumentNote
	if err := json.Unmarshal(body, &candidates); err != nil {
		var envelope struct {
			Notes   []DocumentNote `json:"notes"`
			Results []DocumentNote `json:"results"`
		}
		if envelopeErr := json.Unmarshal(body, &envelope); envelopeErr == nil {
			candidates = append(envelope.Notes, envelope.Results...)
		}

		if len(candidates) == 0 {
			var single DocumentNote
			if singleErr := json.Unmarshal(body, &single); singleErr == nil {
				candidates = append(candidates, single)
			}
		}
	}

	var created DocumentNote
	for _, candidate := range candidates {
		if candidate.ID > created.ID && candidate.Note == expectedNote {
			created = candidate
		}
	}
	if created.ID <= 0 {
		return DocumentNote{}, fmt.Errorf("created note was not present in the Paperless response")
	}

	return created, nil
}

// CreateDocumentNote adds a note through Paperless' dedicated notes endpoint.
func (client *PaperlessClient) CreateDocumentNote(ctx context.Context, documentID int, note string) (DocumentNote, error) {
	if documentID <= 0 {
		return DocumentNote{}, fmt.Errorf("invalid document ID %d", documentID)
	}

	requestBody, err := json.Marshal(struct {
		Note string `json:"note"`
	}{Note: note})
	if err != nil {
		return DocumentNote{}, fmt.Errorf("error marshaling document note request: %w", err)
	}

	path := fmt.Sprintf("api/documents/%d/notes/", documentID)
	resp, err := client.Do(ctx, http.MethodPost, path, bytes.NewReader(requestBody))
	if err != nil {
		return DocumentNote{}, fmt.Errorf("error creating note for document %d: %w", documentID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return DocumentNote{}, fmt.Errorf("error creating note for document %d: status %d", documentID, resp.StatusCode)
	}

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return DocumentNote{}, fmt.Errorf("error reading created note response for document %d: %w", documentID, err)
	}
	createdNote, err := parseCreatedDocumentNote(responseBody, note)
	if err != nil {
		return DocumentNote{}, fmt.Errorf("error parsing created note response for document %d: %w", documentID, err)
	}

	return createdNote, nil
}

// DeleteDocumentNote removes one specific note created by paperless-gpt.
func (client *PaperlessClient) DeleteDocumentNote(ctx context.Context, documentID int, noteID int) error {
	if documentID <= 0 {
		return fmt.Errorf("invalid document ID %d", documentID)
	}
	if noteID <= 0 {
		return fmt.Errorf("invalid note ID %d", noteID)
	}

	path := fmt.Sprintf("api/documents/%d/notes/?id=%d", documentID, noteID)
	resp, err := client.Do(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return fmt.Errorf("error deleting note %d from document %d: %w", noteID, documentID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("error deleting note %d from document %d: status %d", noteID, documentID, resp.StatusCode)
	}

	return nil
}

// GetTaskStatus checks the status of a document processing task
func (client *PaperlessClient) GetTaskStatus(ctx context.Context, taskID string) (map[string]interface{}, error) {
	path := fmt.Sprintf("api/tasks/?task_id=%s", taskID)
	resp, err := client.Do(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("error checking task status: %d, %s", resp.StatusCode, string(bodyBytes))
	}

	var raw interface{}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("error parsing response: %w", err)
	}

	return taskFromTasksResponse(raw, taskID)
}

// taskFromTasksResponse returns the task object from a /api/tasks/?task_id=
// response. paperless-ngx has answered with a bare object, a JSON list, and
// (3.0) a paginated {"count", "results": [...]} envelope; status values have
// been upper case ("SUCCESS") and lower case ("success").
func taskFromTasksResponse(raw interface{}, taskID string) (map[string]interface{}, error) {
	switch v := raw.(type) {
	case []interface{}:
		if len(v) == 0 {
			return nil, fmt.Errorf("task %s not found", taskID)
		}
		task, ok := v[0].(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("unexpected task list entry for task %s", taskID)
		}
		return task, nil
	case map[string]interface{}:
		results, paginated := v["results"].([]interface{})
		if !paginated {
			return v, nil
		}
		return taskFromTasksResponse(results, taskID)
	default:
		return nil, fmt.Errorf("unexpected task status response for task %s", taskID)
	}
}

// CreateTag creates a new tag and returns its ID
func (client *PaperlessClient) CreateTag(ctx context.Context, tagName string, objPerms *ObjPermissions) (int, error) {
	tagRequest := map[string]interface{}{"name": tagName}
	if objPerms != nil {
		tagRequest["owner"] = objPerms.Owner
		if objPerms.SetPermissions != nil {
			tagRequest["set_permissions"] = objPerms.SetPermissions
		}
	}

	requestBody, err := json.Marshal(tagRequest)
	if err != nil {
		return 0, fmt.Errorf("error marshaling tag request: %w", err)
	}

	resp, err := client.Do(ctx, "POST", "api/tags/", bytes.NewBuffer(requestBody))
	if err != nil {
		return 0, fmt.Errorf("error creating tag: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("error creating tag: %d, %s", resp.StatusCode, string(bodyBytes))
	}

	var createdTag struct {
		ID int `json:"id"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&createdTag); err != nil {
		return 0, fmt.Errorf("error parsing created tag response: %w", err)
	}

	return createdTag.ID, nil
}

// EnsureTagExists creates an internal marker tag when it is not already present.
func (client *PaperlessClient) EnsureTagExists(ctx context.Context, tagName string) error {
	if tagName == "" {
		return nil
	}

	tags, err := client.GetAllTags(ctx)
	if err != nil {
		return fmt.Errorf("error fetching tags: %w", err)
	}
	if _, exists := tags[tagName]; exists {
		return nil
	}

	objPerms := client.getPermissionsOrWarn(ctx, nil, "Could not resolve permissions for internal tag %q", tagName)
	if _, err := client.CreateTag(ctx, tagName, objPerms); err != nil {
		return fmt.Errorf("error creating tag %q: %w", tagName, err)
	}
	return nil
}

// UploadDocument uploads a document to paperless-ngx
func (client *PaperlessClient) UploadDocument(ctx context.Context, pdfData []byte, filename string, metadata map[string]interface{}) (string, error) {
	// Create a new multipart form
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Add the document file part
	part, err := writer.CreateFormFile("document", filename)
	if err != nil {
		return "", fmt.Errorf("error creating form file: %w", err)
	}
	if _, err := io.Copy(part, bytes.NewReader(pdfData)); err != nil {
		return "", fmt.Errorf("error copying file data: %w", err)
	}

	// Add metadata fields
	for key, value := range metadata {
		if value == nil {
			continue
		}

		var strValue string
		switch v := value.(type) {
		case string:
			strValue = v
		case int:
			strValue = strconv.Itoa(v)
		case []int:
			for _, tagID := range v {
				if err := writer.WriteField("tags", strconv.Itoa(tagID)); err != nil {
					return "", fmt.Errorf("error adding tag ID: %w", err)
				}
			}
			continue
		default:
			continue
		}

		if err := writer.WriteField(key, strValue); err != nil {
			return "", fmt.Errorf("error adding metadata field %s: %w", key, err)
		}
	}

	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("error closing multipart writer: %w", err)
	}

	// Create the request
	req, err := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("%s/api/documents/post_document/", client.BaseURL), body)
	if err != nil {
		return "", fmt.Errorf("error creating request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", fmt.Sprintf("Token %s", client.APIToken))

	// Send the request
	resp, err := client.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("error sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("error uploading document: %d, %s", resp.StatusCode, string(bodyBytes))
	}

	// Read the response directly as a string (per Swagger docs)
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("error reading response: %w", err)
	}

	// Trim any whitespace and quotes
	taskID := strings.Trim(strings.TrimSpace(string(bodyBytes)), "\"")
	if taskID == "" {
		return "", fmt.Errorf("empty task ID returned")
	}

	log.Infof("Successfully uploaded document, received task ID: %s", taskID)
	return taskID, nil
}

// maxVersionLabelLength mirrors paperless-ngx's DocumentVersionSerializer.
const maxVersionLabelLength = 64

// UploadDocumentVersion adds data as a new version of an existing document
// (paperless-ngx 3.0+, POST /api/documents/{id}/update_version/). The document
// keeps its id, metadata and earlier versions; downloads serve the newest one.
// Returns the consume task ID.
func (client *PaperlessClient) UploadDocumentVersion(ctx context.Context, documentID int, data []byte, filename string, versionLabel string) (string, error) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	part, err := writer.CreateFormFile("document", filename)
	if err != nil {
		return "", fmt.Errorf("error creating form file: %w", err)
	}
	if _, err := io.Copy(part, bytes.NewReader(data)); err != nil {
		return "", fmt.Errorf("error copying file data: %w", err)
	}

	if versionLabel != "" {
		if runes := []rune(versionLabel); len(runes) > maxVersionLabelLength {
			versionLabel = string(runes[:maxVersionLabelLength])
		}
		if err := writer.WriteField("version_label", versionLabel); err != nil {
			return "", fmt.Errorf("error adding version label: %w", err)
		}
	}

	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("error closing multipart writer: %w", err)
	}

	endpoint := fmt.Sprintf("%s/api/documents/%d/update_version/", client.BaseURL, documentID)
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, body)
	if err != nil {
		return "", fmt.Errorf("error creating request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", fmt.Sprintf("Token %s", client.APIToken))

	resp, err := client.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("error sending request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("error reading response: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("error uploading document version: document %d not found, or paperless-ngx is older than 3.0 (no version support): %s", documentID, string(bodyBytes))
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("error uploading document version: %d, %s", resp.StatusCode, string(bodyBytes))
	}

	taskID := strings.Trim(strings.TrimSpace(string(bodyBytes)), "\"")
	if taskID == "" {
		return "", fmt.Errorf("empty task ID returned")
	}

	log.Infof("Successfully uploaded new version of document %d, received task ID: %s", documentID, taskID)
	return taskID, nil
}

// keptSystemTags returns the tags in names that record a state paperless-gpt
// set and suggestions must not erase: PDF_OCR_COMPLETE_TAG, FAIL_TAG,
// AUTO_TAG_COMPLETE and AI workflow completion tags, compared
// case-insensitively. Trigger tags (MANUAL_TAG, AUTO_TAG, AUTO_OCR_TAG,
// workflow triggers) are not kept: replacing the tags has always taken them
// off, and callers rely on that.
func keptSystemTags(names []string) []string {
	markers := []string{pdfOCRCompleteTag, failTag, autoTagComplete}
	for _, wf := range workflows.List() {
		markers = append(markers, wf.CompletionTag)
	}
	var kept []string
	for _, name := range names {
		if slices.ContainsFunc(markers, func(t string) bool { return t != "" && strings.EqualFold(t, name) }) {
			kept = append(kept, name)
		}
	}
	return kept
}
