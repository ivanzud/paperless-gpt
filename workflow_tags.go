package main

import (
	"sort"
	"strings"
)

// protectedWorkflowTags lists every tag paperless-gpt manages itself. It defers
// to systemTags so there is one source of truth: that set also covers each
// configured workflow's trigger and completion tags, which must stay out of
// the LLM's view just like the global ones, or suggesting one would send the
// document straight back into that workflow's queue.
func protectedWorkflowTags() []string {
	return systemTags()
}

func isProtectedWorkflowTag(tagName string) bool {
	_, protected := canonicalProtectedWorkflowTag(tagName)
	return protected
}

func canonicalProtectedWorkflowTag(tagName string) (string, bool) {
	for _, protectedTag := range protectedWorkflowTags() {
		if protectedTag != "" && strings.EqualFold(tagName, protectedTag) {
			return protectedTag, true
		}
	}
	return "", false
}

func filterProtectedWorkflowTags(tags []string) []string {
	filteredTags := make([]string, 0, len(tags))
	for _, tagName := range tags {
		if tagName != "" && !isProtectedWorkflowTag(tagName) {
			filteredTags = append(filteredTags, tagName)
		}
	}
	return filteredTags
}

func containsTagCaseInsensitive(tags []string, tagName string) bool {
	for _, candidate := range tags {
		if strings.EqualFold(candidate, tagName) {
			return true
		}
	}
	return false
}

func compactTagNamesCaseInsensitive(tags []string) []string {
	compactedTags := make([]string, 0, len(tags))
	seenTags := make(map[string]struct{}, len(tags))

	for _, tagName := range tags {
		tagName = strings.TrimSpace(tagName)
		key := strings.ToLower(tagName)
		if key == "" {
			continue
		}
		if _, exists := seenTags[key]; exists {
			continue
		}
		seenTags[key] = struct{}{}
		compactedTags = append(compactedTags, tagName)
	}

	sort.Slice(compactedTags, func(i, j int) bool {
		return strings.ToLower(compactedTags[i]) < strings.ToLower(compactedTags[j])
	})
	return compactedTags
}
