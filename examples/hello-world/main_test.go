package main

import "testing"

func TestGreet(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty name",
			input:    "",
			expected: "Hello, World!",
		},
		{
			name:     "with name",
			input:    "Alice",
			expected: "Hello, Alice!",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := Greet(tc.input)
			if result != tc.expected {
				t.Errorf("expected %q but got %q", tc.expected, result)
			}
		})
	}
}
