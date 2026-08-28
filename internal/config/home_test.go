package config

import (
	"path/filepath"
	"testing"
)

func TestFridayHome(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    string
		wantErr bool
	}{
		{"explicit FRIDAY_HOME wins", map[string]string{"FRIDAY_HOME": "/custom/friday", "HOME": "/home/x"}, "/custom/friday", false},
		{"home default", map[string]string{"HOME": "/home/x"}, filepath.Join("/home/x", ".friday"), false},
		{"state fallback via FRIDAY_STATE_DIR", map[string]string{"FRIDAY_STATE_DIR": "/state/f"}, "/state/f", false},
		{"xdg fallback", map[string]string{"XDG_STATE_HOME": "/xdg"}, filepath.Join("/xdg", "friday"), false},
		{"nothing set is an error", map[string]string{}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			get := func(k string) string { return tt.env[k] }
			got, err := FridayHome(get)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("FridayHome = %q, want %q", got, tt.want)
			}
		})
	}
}
