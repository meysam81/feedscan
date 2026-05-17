package scanner

import "testing"

func TestRegistrableDomain(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, host, want string
		wantErr          bool
	}{
		{"simple", "example.com", "example.com", false},
		{"subdomain", "blog.example.com", "example.com", false},
		{"co.uk", "blog.example.co.uk", "example.co.uk", false},
		{"empty", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := RegistrableDomain(tt.host)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tt.host)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("RegistrableDomain(%q) = %q, want %q", tt.host, got, tt.want)
			}
		})
	}
}
