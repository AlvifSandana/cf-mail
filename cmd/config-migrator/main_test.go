package main

import "testing"

func TestDefaultOutPath(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "config.yml", want: "config.migrated.yml"},
		{in: "./configs/prod.yaml", want: "./configs/prod.migrated.yaml"},
		{in: "./config", want: "./config.migrated.yml"},
	}

	for _, tc := range tests {
		if got := defaultOutPath(tc.in); got != tc.want {
			t.Fatalf("defaultOutPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
