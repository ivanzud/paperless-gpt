package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// ocrFailureTracker counts consecutive OCR-processing failures per document so
// the poll loop can stop retrying a document that keeps failing (and re-paying
// the OCR provider for it every cycle). Counts are in-memory only: a restart
// resets them, which at worst grants a persistently failing document another
// ocrMaxRetries attempts. The zero value is ready to use.
type ocrFailureTracker struct {
	mu       sync.Mutex
	failures map[int]int
}

// recordFailure increments and returns the failure count for a document.
func (t *ocrFailureTracker) recordFailure(documentID int) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.failures == nil {
		t.failures = make(map[int]int)
	}
	t.failures[documentID]++
	return t.failures[documentID]
}

// reset clears the failure count for a document. Entries for documents whose
// trigger tag was removed externally before reaching the limit are never
// cleaned up; that leak is bounded by the number of such documents and
// accepted for simplicity.
func (t *ocrFailureTracker) reset(documentID int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.failures, documentID)
}

// This is our interface, allowing us to enable proper testing
type BackgroundProcessor interface {
	processAutoOcrTagDocuments(ctx context.Context) (int, error)
	processAutoTagDocuments(ctx context.Context) (int, error)
	isOcrEnabled() bool
}

func getBackgroundDocumentTimeout() time.Duration {
	const defaultTimeout = 15 * time.Minute

	raw := strings.TrimSpace(os.Getenv("BACKGROUND_DOCUMENT_TIMEOUT"))
	if raw == "" {
		return defaultTimeout
	}
	if raw == "0" {
		return 0
	}
	if seconds, err := strconv.Atoi(raw); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}

	timeout, err := time.ParseDuration(raw)
	if err != nil || timeout < 0 {
		log.Warnf("Invalid BACKGROUND_DOCUMENT_TIMEOUT value %q, using default %s", raw, defaultTimeout)
		return defaultTimeout
	}
	return timeout
}

func withBackgroundDocumentTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	timeout := getBackgroundDocumentTimeout()
	if timeout <= 0 {
		return parent, func() {}
	}
	return context.WithTimeout(parent, timeout)
}

func waitForBackgroundDelay(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// StartBackgroundTasks starts the background worker and returns a channel that
// closes only after the worker has observed cancellation and exited.
func StartBackgroundTasks(ctx context.Context, app BackgroundProcessor) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)

		minBackoffDuration := 10 * time.Second
		maxBackoffDuration := time.Hour
		pollingInterval := 10 * time.Second

		backoffDuration := minBackoffDuration

		for {
			select {
			case <-ctx.Done():
				log.Infoln("Background tasks shutting down")
				return
			default: // needed to make this non-blocking
			}

			processedCount, err := func() (count int, err error) {
				count = 0

				// If OCR is enabled, run OCR tagging first
				if app.isOcrEnabled() {
					ocrCount, err := app.processAutoOcrTagDocuments(ctx)
					if err != nil {
						return 0, fmt.Errorf("error in processAutoOcrTagDocuments: %w", err)
					}
					count += ocrCount
				}

				// Run auto-tagging after OCR
				autoCount, err := app.processAutoTagDocuments(ctx)
				if err != nil {
					return 0, fmt.Errorf("error in processAutoTagDocuments: %w", err)
				}
				count += autoCount

				return count, nil
			}()

			if err != nil {
				log.Errorf("Error in background tagging: %v", err)
				if !waitForBackgroundDelay(ctx, backoffDuration) {
					log.Infoln("Background tasks shutting down")
					return
				}

				// Exponential backoff logic
				backoffDuration *= 2
				if backoffDuration > maxBackoffDuration {
					log.Warnf("Max backoff duration reached. Using %v", maxBackoffDuration)
					backoffDuration = maxBackoffDuration
				}
			} else {
				// Reset backoff when processing succeeds
				backoffDuration = minBackoffDuration
			}

			// If nothing was processed, pause before next cycle
			if processedCount == 0 {
				if !waitForBackgroundDelay(ctx, pollingInterval) {
					log.Infoln("Background tasks shutting down")
					return
				}
			}
		}
	}()
	return done
}

// applyFailTagAfterPartialSuccess applies the fail tag to a document whose
// update succeeded only after paperless-gpt had to drop one or more fields
// rejected by paperless-ngx (see UpdateDocuments' strip-and-retry path).
//
// The document's tags in paperless-ngx have already been updated by the
// successful retry to whatever the LLM suggested (the auto tag is no longer
// present). To avoid clobbering those LLM-suggested tags, this function
// re-fetches the document's current state, then PATCHes only the tags field
// to append the fail tag.
//
// This is best-effort: if the re-fetch or the PATCH fails, the dropped-field
// information is logged but the document is left with no fail tag. The loop
// is still broken (the successful retry removed the auto tag) — only the
// user-visible marker is missing.
func applyFailTagAfterPartialSuccess(ctx context.Context, client ClientInterface, db *gorm.DB, documentID int, droppedFields []string) {
	docLogger := documentLogger(documentID)
	if failTag == "" {
		docLogger.Warnf("Document %d update succeeded after paperless-ngx rejected fields %v; no FAIL_TAG is configured, so the document is not marked for review.", documentID, droppedFields)
		return
	}
	currentDoc, err := client.GetDocument(ctx, documentID)
	if err != nil {
		docLogger.Errorf("Document %d update succeeded after dropping fields %v, but fetching current state to apply fail tag failed: %v", documentID, droppedFields, err)
		return
	}
	if containsTagCaseInsensitive(currentDoc.Tags, failTag) {
		docLogger.Warnf("Document %d update succeeded after dropping fields %v; fail tag %q is already present.", documentID, droppedFields, failTag)
		return
	}
	suggestion := DocumentSuggestion{
		ID:               documentID,
		OriginalDocument: currentDoc,
		SuggestedTags:    []string{failTag},
		KeepOriginalTags: true,
	}
	if err := client.UpdateDocuments(ctx, []DocumentSuggestion{suggestion}, db, false); err != nil {
		docLogger.Errorf("Document %d update succeeded after dropping fields %v, but applying fail tag %q failed: %v", documentID, droppedFields, failTag, err)
		return
	}
	docLogger.Warnf("Document %d update succeeded after paperless-ngx rejected fields %v; fail tag %q applied for user review.", documentID, droppedFields, failTag)
}

// markProcessingFailed performs a minimal tag-only PATCH that removes the
// trigger tag a document was picked up by (so the document is not
// re-processed on every poll cycle, which can cost real money on paid
// LLM/OCR providers) and, if failTag is configured, adds it as a marker so
// the user can find and review failed documents.
//
// The PATCH only manipulates tags and therefore should succeed even when the
// failure that led here was a field-validation rejection (e.g. an
// LLM-suggested date that is not a real calendar date). The reason string is
// used in log output only (e.g. "update failed", "OCR failed 3 times").
func markProcessingFailed(ctx context.Context, client ClientInterface, db *gorm.DB, document Document, removeTag string, reason string) error {
	docLogger := documentLogger(document.ID)
	recoveryFields := DocumentSuggestion{
		ID:               document.ID,
		OriginalDocument: document,
		RemoveTags:       []string{removeTag},
	}
	if failTag != "" {
		recoveryFields.SuggestedTags = []string{failTag}
		recoveryFields.KeepOriginalTags = true
	}
	if err := client.UpdateDocuments(ctx, []DocumentSuggestion{recoveryFields}, db, false); err != nil {
		return err
	}
	if failTag != "" {
		docLogger.Warnf("Document %d %s; %q tag removed and %q tag applied to break the processing loop.", document.ID, reason, removeTag, failTag)
	} else {
		docLogger.Warnf("Document %d %s; %q tag removed to break the processing loop (no failTag configured).", document.ID, reason, removeTag)
	}
	return nil
}

// recoverFromFailedUpdate is called when an UpdateDocuments call has failed for
// a document picked up by the auto-tagging or auto-OCR poll.
//
// On its own failure, this function logs at error level but does not return
// the error to the caller — the caller has already recorded the original
// update failure and the recovery is best-effort.
func recoverFromFailedUpdate(ctx context.Context, client ClientInterface, db *gorm.DB, document Document, removeTag string) {
	if err := markProcessingFailed(ctx, client, db, document, removeTag, "update failed"); err != nil {
		documentLogger(document.ID).Errorf("Recovery update for failed document %d also failed: %v. The %q tag may still be present and the document may be re-processed on the next poll cycle.", document.ID, err, removeTag)
	}
}

// classifyDocument generates metadata suggestions shared by auto-tagging and
// the optional OCR-then-classify path.
func (app *App) classifyDocument(ctx context.Context, document Document, logger *logrus.Entry) (*DocumentSuggestion, error) {
	settingsMutex.RLock()
	generateCustomFields := settings.CustomFieldsEnable
	settingsMutex.RUnlock()

	request := GenerateSuggestionsRequest{
		Documents:              []Document{document},
		GenerateTitles:         strings.ToLower(autoGenerateTitle) != "false",
		GenerateTags:           strings.ToLower(autoGenerateTags) != "false",
		GenerateCorrespondents: strings.ToLower(autoGenerateCorrespondents) != "false",
		GenerateDocumentTypes:  strings.ToLower(autoGenerateDocumentType) != "false",
		GenerateCreatedDate:    strings.ToLower(autoGenerateCreatedDate) != "false",
		GenerateCustomFields:   generateCustomFields,
		IsAutoProcessing:       true,
	}
	suggestions, err := app.generateDocumentSuggestions(ctx, request, logger)
	if err != nil {
		return nil, fmt.Errorf("error generating suggestions: %w", err)
	}
	if len(suggestions) == 0 {
		return nil, fmt.Errorf("no suggestions generated")
	}
	return &suggestions[0], nil
}

// processAutoTagDocuments handles the background auto-tagging of documents
func (app *App) processAutoTagDocuments(ctx context.Context) (int, error) {
	documents, err := app.Client.GetDocumentsByTag(ctx, autoTag, 25)
	if err != nil {
		return 0, fmt.Errorf("error fetching documents with autoTag: %w", err)
	}

	if len(documents) == 0 {
		log.Debugf("No documents with tag %s found", autoTag)
		return 0, nil // No documents to process
	}

	// Refresh the custom fields cache before processing, as we have documents
	refreshCustomFieldsCache(app.Client)

	log.Debugf("Found at least %d remaining documents with tag %s", len(documents), autoTag)

	var errs []error
	processedCount := 0

	for _, docSummary := range documents {
		docCtx, cancel := withBackgroundDocumentTimeout(ctx)

		// Get the full document details, including custom fields
		document, err := app.Client.GetDocument(docCtx, docSummary.ID)
		if err != nil {
			cancel()
			err = fmt.Errorf("error fetching full details for document %d: %w", docSummary.ID, err)
			documentLogger(docSummary.ID).Error(err.Error())
			errs = append(errs, err)
			continue
		}

		// Skip documents that have the autoOcrTag
		if containsTagCaseInsensitive(document.Tags, autoOcrTag) {
			cancel()
			log.Debugf("Skipping document %d as it has the OCR tag %s", document.ID, autoOcrTag)
			continue
		}

		docLogger := documentLogger(document.ID)
		docLogger.Info("Processing document for auto-tagging")

		settingsMutex.RLock()
		generateCustomFields := settings.CustomFieldsEnable
		settingsMutex.RUnlock()

		suggestionRequest := GenerateSuggestionsRequest{
			Documents:              []Document{document},
			GenerateTitles:         strings.ToLower(autoGenerateTitle) != "false",
			GenerateTags:           strings.ToLower(autoGenerateTags) != "false",
			GenerateCorrespondents: strings.ToLower(autoGenerateCorrespondents) != "false",
			GenerateDocumentTypes:  strings.ToLower(autoGenerateDocumentType) != "false",
			GenerateCreatedDate:    strings.ToLower(autoGenerateCreatedDate) != "false",
			GenerateCustomFields:   generateCustomFields,
			IsAutoProcessing:       true,
		}

		suggestions, err := app.generateDocumentSuggestions(docCtx, suggestionRequest, docLogger)
		if err != nil {
			cancel()
			err = fmt.Errorf("error generating suggestions for document %d: %w", document.ID, err)
			docLogger.Error(err.Error())
			errs = append(errs, err)
			continue
		}

		err = app.Client.UpdateDocuments(docCtx, suggestions, app.Database, false)
		if err != nil {
			var partial *PartialUpdateError
			if errors.As(err, &partial) {
				cancel()
				// Update went through but paperless-ngx rejected some fields,
				// which UpdateDocuments dropped in order to land the rest.
				// The auto tag is already gone (it was part of the successful
				// retry's tag update). Apply the fail tag so the user sees
				// that this document needs review.
				applyFailTagAfterPartialSuccess(ctx, app.Client, app.Database, partial.DocumentID, partial.DroppedFields)
				processedCount++
				continue
			}
			err = fmt.Errorf("error updating document %d: %w", document.ID, err)
			docLogger.Error(err.Error())
			errs = append(errs, err)
			recoverFromFailedUpdate(ctx, app.Client, app.Database, document, autoTag)
			cancel()
			continue
		}

		cancel()
		docLogger.Info("Successfully processed document")
		processedCount++
	}

	if len(errs) > 0 {
		return processedCount, errors.Join(errs...)
	}

	return processedCount, nil
}

// processAutoOcrTagDocuments handles the background auto-tagging of OCR documents
func (app *App) processAutoOcrTagDocuments(ctx context.Context) (int, error) {
	documents, err := app.Client.GetDocumentsByTag(ctx, autoOcrTag, 25)
	if err != nil {
		return 0, fmt.Errorf("error fetching documents with autoOcrTag: %w", err)
	}

	if len(documents) == 0 {
		log.Debugf("No documents with tag %s found", autoOcrTag)
		return 0, nil
	}

	log.Debugf("Found %d documents with tag %s", len(documents), autoOcrTag)

	successCount := 0
	var errs []error

	for _, document := range documents {
		docCtx, cancel := withBackgroundDocumentTimeout(ctx)
		docLogger := documentLogger(document.ID)
		docLogger.Info("Processing document for OCR")

		// Skip OCR if the document already has the OCR complete tag and tagging is enabled
		if app.pdfOCRTagging {
			if containsTagCaseInsensitive(document.Tags, app.pdfOCRCompleteTag) {
				docLogger.Infof("Document already has OCR complete tag '%s', skipping OCR processing", app.pdfOCRCompleteTag)

				// Remove only the autoOcrTag to take it out of the processing queue
				// while preserving the OCR complete tag
				err = app.Client.UpdateDocuments(docCtx, []DocumentSuggestion{
					{
						ID:               document.ID,
						OriginalDocument: document,
						RemoveTags:       []string{autoOcrTag},
					},
				}, app.Database, false)

				if err != nil {
					cancel()
					docLogger.Errorf("Update to remove autoOcrTag failed: %v", err)
					errs = append(errs, fmt.Errorf("document %d update error: %w", document.ID, err))
					continue
				}

				cancel()
				docLogger.Info("Successfully removed auto OCR tag")
				successCount++
				continue
			}
		}

		options := app.effectiveOCRDefaults()
		options.ExistingContent = document.Content

		// Every auto run is tracked exactly like a manual one: registered in
		// the in-memory job store (live progress) and persisted as an OCR Run
		// (Activity log), so hands-off users can audit what happened.
		jobID := generateJobID()
		jobStore.addJob(&Job{
			ID:         jobID,
			DocumentID: document.ID,
			Status:     "in_progress",
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
			Options:    options,
		})
		if err := CreateOCRRun(app.Database, &OCRRun{
			JobID:           jobID,
			DocumentID:      document.ID,
			DocumentTitle:   document.Title,
			Trigger:         "auto",
			LimitPages:      options.LimitPages,
			ProcessMode:     options.ProcessMode,
			UploadPDF:       options.UploadPDF,
			ReplaceOriginal: options.ReplaceOriginal,
			CopyMetadata:    options.CopyMetadata,
			Provider:        app.ocrProviderLabel,
			PDFAction:       "none",
		}); err != nil {
			docLogger.Warnf("Failed to persist auto OCR run: %v", err)
		}

		// Use the DocumentProcessor interface instead of calling the method directly
		var processedDoc *ProcessedDocument
		var err error
		if app.docProcessor != nil {
			// Use injected processor if available
			processedDoc, err = app.docProcessor.ProcessDocumentOCR(docCtx, document.ID, options, jobID)
		} else {
			// Use the app's own implementation if no processor is injected
			processedDoc, err = app.ProcessDocumentOCR(docCtx, document.ID, options, jobID)
		}

		pagesDone, totalPages := jobStore.progress(jobID)
		if err != nil {
			docLogger.Errorf("OCR processing failed: %v", err)
			jobStore.updateJobStatus(jobID, "failed", err.Error())
			finishOCRRunLogged(app, jobID, "failed", err.Error(), pagesDone, totalPages, "", "")

			if ocrSkipFailedDocuments && ctx.Err() == nil {
				reason := "OCR failed and OCR_SKIP_FAILED_DOCUMENTS is enabled"
				recErr := markProcessingFailed(ctx, app.Client, app.Database, document, autoOcrTag, reason)
				cancel()
				if recErr != nil {
					docLogger.Errorf("Removing %q tag after OCR failure also failed: %v", autoOcrTag, recErr)
					errs = append(errs, fmt.Errorf("document %d OCR error: %w", document.ID, err))
					continue
				}
				app.ocrFailures.reset(document.ID)
				successCount++
				continue
			}

			// A canceled/expired poll-loop context is a shutdown, not a
			// document problem — never count it against the document.
			// Deliberately checked on ctx rather than via errors.Is on err:
			// providers wrap their calls in their own timeout contexts
			// (e.g. Azure, ocr/azure_provider.go), and those per-document
			// timeouts must count toward the limit.
			if ocrMaxRetries > 0 && ctx.Err() == nil {
				attempts := app.ocrFailures.recordFailure(document.ID)
				if attempts >= ocrMaxRetries {
					reason := fmt.Sprintf("OCR failed %d times", attempts)
					if recErr := markProcessingFailed(ctx, app.Client, app.Database, document, autoOcrTag, reason); recErr != nil {
						cancel()
						// Keep the failure count so the tag removal is
						// retried on the next poll cycle.
						docLogger.Errorf("Removing %q tag after repeated OCR failures also failed: %v. The document will be retried on the next poll cycle.", autoOcrTag, recErr)
						errs = append(errs, fmt.Errorf("document %d OCR error: %w", document.ID, err))
						continue
					}
					app.ocrFailures.reset(document.ID)
					cancel()
					// The document is out of the queue now; don't let its
					// handled error keep the whole loop in backoff.
					continue
				}
			}
			cancel()
			errs = append(errs, fmt.Errorf("document %d OCR error: %w", document.ID, err))
			continue
		}
		// OCR itself succeeded — a previous failure streak is over,
		// whatever the update below does.
		app.ocrFailures.reset(document.ID)
		if processedDoc == nil {
			cancel()
			docLogger.Info("OCR processing skipped for document")
			jobStore.updateJobStatus(jobID, "completed", "Skipped (already processed)")
			finishOCRRunLogged(app, jobID, "completed", "", pagesDone, totalPages, "none", "Skipped (already processed)")
			continue
		}
		jobStore.updateJobStatus(jobID, "completed", processedDoc.Text)
		finishOCRRunLogged(app, jobID, "completed", "", pagesDone, totalPages, processedDoc.PDFAction, processedDoc.PDFDetail)
		if err := PruneOCRRuns(app.Database, document.ID); err != nil {
			docLogger.Warnf("Failed to prune OCR runs: %v", err)
		}
		docLogger.Debug("OCR processing completed")

		documentSuggestion := DocumentSuggestion{
			ID:               document.ID,
			OriginalDocument: document,
			SuggestedContent: processedDoc.Text,
			RemoveTags:       []string{autoOcrTag},
			// Add OCR complete tag if tagging is enabled and PDF wasn't uploaded (upload handles tagging)
			AddTags: func() []string {
				if app.pdfOCRTagging && !options.UploadPDF {
					return []string{app.pdfOCRCompleteTag}
				}
				return nil
			}(),
		}

		if (app.pdfOCRTagging) && app.pdfOCRCompleteTag != "" {
			// Add the OCR complete tag if tagging is enabled
			documentSuggestion.SuggestedTags = []string{app.pdfOCRCompleteTag}
			documentSuggestion.KeepOriginalTags = true
			docLogger.Infof("Adding OCR complete tag '%s'", app.pdfOCRCompleteTag)
		}

		if autoOcrThenClassify {
			docLogger.Info("Chaining into classification after OCR")
			classifyDoc, fetchErr := app.Client.GetDocument(docCtx, document.ID)
			if fetchErr != nil {
				docLogger.Errorf("Failed to fetch full document for classification; OCR content will still be saved: %v", fetchErr)
			} else {
				classifyDoc.Content = processedDoc.Text
				refreshCustomFieldsCache(app.Client)

				classifySuggestion, classifyErr := app.classifyDocument(docCtx, classifyDoc, docLogger)
				if classifyErr != nil {
					docLogger.Errorf("Classification after OCR failed; OCR content will still be saved: %v", classifyErr)
				} else {
					documentSuggestion.SuggestedTitle = classifySuggestion.SuggestedTitle
					documentSuggestion.SuggestedCorrespondent = classifySuggestion.SuggestedCorrespondent
					documentSuggestion.SuggestedDocumentType = classifySuggestion.SuggestedDocumentType
					documentSuggestion.SuggestedCreatedDate = classifySuggestion.SuggestedCreatedDate
					documentSuggestion.SuggestedCustomFields = classifySuggestion.SuggestedCustomFields
					documentSuggestion.SuggestedSummary = classifySuggestion.SuggestedSummary
					documentSuggestion.CustomFieldsWriteMode = classifySuggestion.CustomFieldsWriteMode
					documentSuggestion.CustomFieldsEnable = classifySuggestion.CustomFieldsEnable

					for _, tag := range classifySuggestion.SuggestedTags {
						if !containsTagCaseInsensitive(documentSuggestion.SuggestedTags, tag) {
							documentSuggestion.SuggestedTags = append(documentSuggestion.SuggestedTags, tag)
						}
					}
					documentSuggestion.KeepOriginalTags = true

					for _, tag := range append(classifySuggestion.RemoveTags, autoTag) {
						if !containsTagCaseInsensitive(documentSuggestion.RemoveTags, tag) {
							documentSuggestion.RemoveTags = append(documentSuggestion.RemoveTags, tag)
						}
					}
					for _, tag := range classifySuggestion.AddTags {
						if !containsTagCaseInsensitive(documentSuggestion.AddTags, tag) {
							documentSuggestion.AddTags = append(documentSuggestion.AddTags, tag)
						}
					}
					docLogger.Info("Classification after OCR completed successfully")
				}
			}
		}

		// Skip updating the original document if it was actually replaced (deleted) during OCR.
		// The replacement document will be processed as a new document on the next cycle.
		if options.ReplaceOriginal && processedDoc != nil && processedDoc.ReplacedOriginal {
			docLogger.Info("Skipping tag update for replaced document (original was deleted)")
		} else {
			err = app.Client.UpdateDocuments(docCtx, []DocumentSuggestion{
				documentSuggestion,
			}, app.Database, false)
			if err != nil {
				var partial *PartialUpdateError
				if errors.As(err, &partial) {
					cancel()
					applyFailTagAfterPartialSuccess(ctx, app.Client, app.Database, partial.DocumentID, partial.DroppedFields)
					// Treat as a (partial) success: tag was removed, fail tag applied.
					docLogger.Info("Successfully processed document OCR (with partial-update fail-tag marker)")
					successCount++
					continue
				}
				docLogger.Errorf("Update after OCR failed: %v", err)
				errs = append(errs, fmt.Errorf("document %d update error: %w", document.ID, err))
				recoverFromFailedUpdate(ctx, app.Client, app.Database, document, autoOcrTag)
				cancel()
				continue
			}
		}

		cancel()
		docLogger.Info("Successfully processed document OCR")
		successCount++
	}

	if len(errs) > 0 {
		return successCount, fmt.Errorf("one or more errors occurred: %w", errors.Join(errs...))
	}

	return successCount, nil
}
