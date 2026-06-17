package main

import "testing"

func TestNormalizeMonetary(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"usd_us_thousands", "USD1,053.52", "USD1053.52"},
		{"usd_us_thousands_with_space", "USD 1,053.52", "USD1053.52"},
		{"canonical_usd_large", "USD440000.00", "USD440000.00"},
		{"canonical_no_currency", "1053.52", "1053.52"},
		{"us_multiple_thousands", "1,053,000.50", "1053000.50"},
		{"us_dollar_symbol", "$1,053.52", "USD1053.52"},
		{"eur_code", "EUR1.053,52", "EUR1053.52"},
		{"eur_symbol", "€1.053,52", "EUR1053.52"},
		{"comma_decimal", "1053,52", "1053.52"},
		{"thousands_no_decimal", "1,053", "1053.00"},
		{"integer_pads", "1053", "1053.00"},
		{"one_decimal_pads", "1053.5", "1053.50"},
		{"three_decimals_truncate", "1053.525", "1053.52"},
		{"code_prefix_lowercase", "usd1053.52", "USD1053.52"},
		{"code_suffix_with_space", "1053.52 USD", "USD1053.52"},
		{"negative_us", "-1,053.52", "-1053.52"},
		{"explicit_positive", "+1053.52", "1053.52"},
		{"leading_zeros", "0001053.52", "1053.52"},
		{"sub_unit_eu", "0,05", "0.05"},
		{"empty", "", ""},
		{"garbage_passthrough", "abc", "abc"},
		{"non_thousands_group_truncates", "12,3456", "12.34"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeMonetary(tc.in)
			if got != tc.want {
				t.Errorf("normalizeMonetary(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeCustomFieldValue(t *testing.T) {
	cases := []struct {
		name     string
		dataType string
		in       interface{}
		want     interface{}
	}{
		{"monetary_string_normalized", "monetary", "USD1,053.52", "USD1053.52"},
		{"monetary_already_canonical", "monetary", "USD440000.00", "USD440000.00"},
		{"string_passes_through", "string", "USD1,053.52", "USD1,053.52"},
		{"date_passes_through", "date", "2026-02-29", "2026-02-29"},
		{"monetary_number_passes_through", "monetary", 1053.52, 1053.52},
		{"monetary_nil_passes_through", "monetary", nil, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeCustomFieldValue(tc.dataType, tc.in)
			if got != tc.want {
				t.Errorf("normalizeCustomFieldValue(%q, %#v) = %#v, want %#v", tc.dataType, tc.in, got, tc.want)
			}
		})
	}
}
