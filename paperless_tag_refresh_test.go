package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A batch of documents is listed once and then processed one after the
// other, so the tags in a suggestion's snapshot can be minutes old when the
// update is written. UpdateDocuments must apply its additions and removals
// to the tags the document carries at that moment.
func TestUpdateDocuments_AppliesTagChangesToCurrentTags(t *testing.T) {
	const (
		idTrigger  = 10
		idKeep     = 20
		idComplete = 30
		idAdded    = 40 // added by someone else after the snapshot
		idRemoved  = 50 // removed by someone else after the snapshot
		idHidden   = 99 // not visible to the API user
	)

	tests := []struct {
		name        string
		snapshot    []string
		snapshotIDs []int
		current     []int // nil: the re-read fails
		wantTagIDs  []interface{}
		wantNoPatch bool
	}{
		{
			name:        "a tag added meanwhile is kept",
			snapshot:    []string{"trigger", "keepMe"},
			snapshotIDs: []int{idTrigger, idKeep},
			current:     []int{idTrigger, idKeep, idAdded},
			wantTagIDs:  []interface{}{float64(idKeep), float64(idAdded), float64(idComplete)},
		},
		{
			name:        "a tag removed meanwhile is not put back",
			snapshot:    []string{"trigger", "keepMe", "removedMeanwhile"},
			snapshotIDs: []int{idTrigger, idKeep, idRemoved},
			current:     []int{idTrigger, idKeep},
			wantTagIDs:  []interface{}{float64(idKeep), float64(idComplete)},
		},
		{
			name:        "an invisible tag added meanwhile is kept by ID",
			snapshot:    []string{"trigger", "keepMe"},
			snapshotIDs: []int{idTrigger, idKeep},
			current:     []int{idTrigger, idKeep, idHidden},
			wantTagIDs:  []interface{}{float64(idKeep), float64(idComplete), float64(idHidden)},
		},
		{
			name:        "nothing is sent when the change was already made",
			snapshot:    []string{"trigger", "keepMe"},
			snapshotIDs: []int{idTrigger, idKeep},
			current:     []int{idKeep, idComplete},
			wantNoPatch: true,
		},
		{
			name:        "the snapshot is used when the re-read fails",
			snapshot:    []string{"trigger", "keepMe"},
			snapshotIDs: []int{idTrigger, idKeep},
			current:     nil,
			wantTagIDs:  []interface{}{float64(idKeep), float64(idComplete)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newTestEnv(t)
			defer env.teardown()

			previousManualTag, previousAutoTag, previousAutoOcrTag := manualTag, autoTag, autoOcrTag
			manualTag, autoTag, autoOcrTag = "manual", "auto", "trigger"
			t.Cleanup(func() {
				manualTag, autoTag, autoOcrTag = previousManualTag, previousAutoTag, previousAutoOcrTag
			})

			env.setMockResponse("/api/tags/", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"results": []map[string]interface{}{
						{"id": idTrigger, "name": "trigger"},
						{"id": idKeep, "name": "keepMe"},
						{"id": idComplete, "name": "complete"},
						{"id": idAdded, "name": "addedMeanwhile"},
						{"id": idRemoved, "name": "removedMeanwhile"},
					},
					"next": nil,
				})
			})
			if tt.current != nil {
				env.currentTags[1] = tt.current
			}

			var patched map[string]interface{}
			patches := 0
			env.setMockResponse("/api/documents/1/", func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPatch {
					patches++
					_ = json.NewDecoder(r.Body).Decode(&patched)
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{}`))
			})

			doc := DocumentSuggestion{
				ID:               1,
				OriginalDocument: Document{ID: 1, Tags: tt.snapshot, TagIDs: tt.snapshotIDs},
				AddTags:          []string{"complete"},
				RemoveTags:       []string{"trigger"},
			}
			require.NoError(t, env.client.UpdateDocuments(context.Background(), []DocumentSuggestion{doc}, env.db, false))

			assert.Equal(t, 1, env.tagReReads[1], "the current tags are read once per document")
			if tt.wantNoPatch {
				assert.Zero(t, patches, "no update is needed, got %v", patched)
				return
			}
			require.Contains(t, patched, "tags", "the tag update must be sent")
			assert.ElementsMatch(t, tt.wantTagIDs, patched["tags"])
		})
	}
}
