package main

import (
	"fmt"
	"regexp"
	"strings"
)

// normalizeMonetary turns an LLM-emitted monetary value into the
// Paperless-canonical form: optional 3-letter currency code immediately
// followed by a plain number with exactly two decimals and no thousands
// separators (for example, "USD1053.52" or "1053.52").
func normalizeMonetary(value string) string {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return value
	}

	currency, numeric := splitCurrencyAndNumber(raw)
	if numeric == "" {
		return value
	}

	parsed, ok := parseAmount(numeric)
	if !ok {
		return value
	}

	if currency != "" {
		return fmt.Sprintf("%s%s", currency, parsed)
	}
	return parsed
}

// normalizeCustomFieldValue applies type-aware normalization to a custom-field
// value. Only monetary string values are touched.
func normalizeCustomFieldValue(dataType string, value interface{}) interface{} {
	if dataType != "monetary" {
		return value
	}
	s, ok := value.(string)
	if !ok {
		return value
	}
	return normalizeMonetary(s)
}

var (
	currencyCodePrefixRe = regexp.MustCompile(`^([A-Za-z]{3})`)
	currencyCodeSuffixRe = regexp.MustCompile(`([A-Za-z]{3})$`)
	numericCharsRe       = regexp.MustCompile(`^[+\-]?[0-9.,\s]+$`)
)

var currencySymbolToCode = map[rune]string{
	'$': "USD",
	'€': "EUR",
	'£': "GBP",
	'¥': "JPY",
	'₹': "INR",
	'₩': "KRW",
	'₽': "RUB",
}

func splitCurrencyAndNumber(s string) (string, string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ""
	}

	runes := []rune(s)
	if code, ok := currencySymbolToCode[runes[0]]; ok {
		return code, strings.TrimSpace(string(runes[1:]))
	}
	if code, ok := currencySymbolToCode[runes[len(runes)-1]]; ok {
		return code, strings.TrimSpace(string(runes[:len(runes)-1]))
	}

	if m := currencyCodePrefixRe.FindString(s); m != "" {
		rest := strings.TrimSpace(s[len(m):])
		if rest != "" && looksNumeric(rest) {
			return strings.ToUpper(m), rest
		}
	}
	if m := currencyCodeSuffixRe.FindString(s); m != "" {
		rest := strings.TrimSpace(s[:len(s)-len(m)])
		if rest != "" && looksNumeric(rest) {
			return strings.ToUpper(m), rest
		}
	}

	if looksNumeric(s) {
		return "", s
	}
	return "", ""
}

func looksNumeric(s string) bool {
	return numericCharsRe.MatchString(strings.TrimSpace(s))
}

func parseAmount(s string) (string, bool) {
	s = strings.ReplaceAll(strings.TrimSpace(s), " ", "")
	if s == "" {
		return "", false
	}

	sign := ""
	switch s[0] {
	case '-':
		sign = "-"
		s = s[1:]
	case '+':
		s = s[1:]
	}
	if s == "" {
		return "", false
	}

	hasComma := strings.Contains(s, ",")
	hasDot := strings.Contains(s, ".")

	var intPart, fracPart string
	var ok bool

	switch {
	case hasComma && hasDot:
		intPart, fracPart, ok = parseBothSeparators(s)
	case hasComma:
		intPart, fracPart, ok = parseSingleSeparator(s, ',')
	case hasDot:
		intPart, fracPart, ok = parseSingleSeparator(s, '.')
	default:
		intPart, fracPart, ok = s, "", isDigits(s)
	}
	if !ok {
		return "", false
	}

	switch {
	case len(fracPart) == 0:
		fracPart = "00"
	case len(fracPart) == 1:
		fracPart += "0"
	case len(fracPart) > 2:
		fracPart = fracPart[:2]
	}

	intPart = strings.TrimLeft(intPart, "0")
	if intPart == "" {
		intPart = "0"
	}

	return sign + intPart + "." + fracPart, true
}

func parseBothSeparators(s string) (intPart, fracPart string, ok bool) {
	lastComma := strings.LastIndex(s, ",")
	lastDot := strings.LastIndex(s, ".")
	var decimalSep, thousandsSep string
	if lastComma > lastDot {
		decimalSep, thousandsSep = ",", "."
	} else {
		decimalSep, thousandsSep = ".", ","
	}
	stripped := strings.ReplaceAll(s, thousandsSep, "")
	parts := strings.Split(stripped, decimalSep)
	if len(parts) != 2 {
		return "", "", false
	}
	if !isDigits(parts[0]) || !isDigits(parts[1]) {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func parseSingleSeparator(s string, sep rune) (intPart, fracPart string, ok bool) {
	parts := strings.Split(s, string(sep))

	if len(parts) > 2 {
		if len(parts[0]) < 1 || len(parts[0]) > 3 || !isDigits(parts[0]) {
			return "", "", false
		}
		for _, p := range parts[1:] {
			if len(p) != 3 || !isDigits(p) {
				return "", "", false
			}
		}
		return strings.Join(parts, ""), "", true
	}

	left, right := parts[0], parts[1]
	if !isDigits(left) || !isDigits(right) {
		return "", "", false
	}

	if len(right) == 3 && len(left) >= 1 && len(left) <= 3 {
		return left + right, "", true
	}
	return left, right, true
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
