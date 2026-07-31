package main

import (
	"sort"
	"strings"
)

func protectedWorkflowTags() []string {
	return []string{
		manualTag,
		autoTag,
		autoOcrTag,
		pdfOCRCompleteTag,
		failTag,
		autoTagComplete,
	}
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

func findTagIDCaseInsensitive(tags map[string]int, tagName string) (string, int, bool) {
	for availableTagName, tagID := range tags {
		if strings.EqualFold(tagName, availableTagName) {
			return availableTagName, tagID, true
		}
	}
	return "", 0, false
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
