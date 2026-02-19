package proxy

import (
	"bytes"
	"testing"
)

func TestOptsValidation(t *testing.T) {
	err := Run(Opts{})
	if err == nil {
		t.Fatal("expected error for empty DockerArgs, got nil")
	}
	want := "proxy: DockerArgs must not be empty"
	if err.Error() != want {
		t.Fatalf("expected error %q, got %q", want, err.Error())
	}
}

func TestSetTitle(t *testing.T) {
	tests := []struct {
		name         string
		info         StatusBarInfo
		prefixActive bool
		wantContains []string
	}{
		{
			name: "basic title",
			info: StatusBarInfo{
				Name:   "my-container",
				Branch: "main",
			},
			prefixActive: false,
			wantContains: []string{"my-container", "main", "^B for options"},
		},
		{
			name: "prefix active",
			info: StatusBarInfo{
				Name:   "my-container",
				Branch: "feature/test",
				Yolo:   true,
			},
			prefixActive: true,
			wantContains: []string{"my-container", "feature/test", "yolo", "d:detach"},
		},
		{
			name:         "empty info",
			info:         StatusBarInfo{},
			prefixActive: false,
			wantContains: []string{"^B for options"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			setTitle(&buf, tc.info, tc.prefixActive)

			got := buf.String()
			if got == "" {
				t.Error("expected output, got nothing")
			}

			// Should start with OSC and end with BEL.
			if got[0] != '\033' {
				t.Errorf("expected ESC at start, got %q", got[0])
			}
			if got[len(got)-1] != '\007' {
				t.Errorf("expected BEL at end, got %q", got[len(got)-1])
			}

			for _, want := range tc.wantContains {
				if !bytes.Contains(buf.Bytes(), []byte(want)) {
					t.Errorf("title missing %q in %q", want, got)
				}
			}
		})
	}
}
