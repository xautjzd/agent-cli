package version

import "testing"

func TestStableModuleVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		raw  string
		want string
		ok   bool
	}{
		{raw: "v0.1.1", want: "0.1.1", ok: true},
		{raw: "1.20.300", want: "1.20.300", ok: true},
		{raw: "(devel)"},
		{raw: "v0.1.1-0.20260728"},
		{raw: "v0.1.1+dirty"},
		{raw: "v0.01.1"},
		{raw: "latest"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.raw, func(t *testing.T) {
			t.Parallel()
			got, ok := stableModuleVersion(tt.raw)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("stableModuleVersion(%q) = %q, %v; want %q, %v", tt.raw, got, ok, tt.want, tt.ok)
			}
		})
	}
}
