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

func TestRenderStatusBar(t *testing.T) {
	tests := []struct {
		name         string
		width        int
		height       int
		info         StatusBarInfo
		prefixActive bool
	}{
		{
			name:   "basic bar",
			width:  80,
			height: 24,
			info: StatusBarInfo{
				Name:   "my-container",
				Branch: "main",
				Yolo:   false,
			},
			prefixActive: false,
		},
		{
			name:   "prefix active",
			width:  80,
			height: 24,
			info: StatusBarInfo{
				Name:   "my-container",
				Branch: "feature/test",
				Yolo:   true,
			},
			prefixActive: true,
		},
		{
			name:   "narrow terminal",
			width:  20,
			height: 10,
			info: StatusBarInfo{
				Name:   "test",
				Branch: "main",
			},
			prefixActive: false,
		},
		{
			name:   "empty info",
			width:  80,
			height: 24,
			info:   StatusBarInfo{},
		},
		{
			name:   "zero height",
			width:  80,
			height: 0,
			info:   StatusBarInfo{Name: "test"},
		},
		{
			name:   "height 1",
			width:  80,
			height: 1,
			info:   StatusBarInfo{Name: "test"},
		},
		{
			name:   "zero width",
			width:  0,
			height: 24,
			info:   StatusBarInfo{Name: "test"},
		},
		{
			name:   "very wide terminal",
			width:  200,
			height: 50,
			info: StatusBarInfo{
				Name:   "long-container-name",
				Branch: "feature/very-long-branch-name",
				Yolo:   true,
			},
			prefixActive: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			// Should not panic.
			renderStatusBar(&buf, tc.width, tc.height, tc.info, tc.prefixActive)

			// For zero/too-small dimensions, nothing should be written.
			if tc.height < 2 || tc.width < 1 {
				if buf.Len() != 0 {
					t.Errorf("expected no output for height=%d width=%d, got %d bytes",
						tc.height, tc.width, buf.Len())
				}
				return
			}

			// For valid dimensions, something should be written.
			if buf.Len() == 0 {
				t.Error("expected output for valid dimensions, got nothing")
			}
		})
	}
}

func TestSetScrollRegion(t *testing.T) {
	// These write to os.Stdout but should not panic.
	t.Run("normal height", func(t *testing.T) {
		setScrollRegion(24)
	})

	t.Run("small height", func(t *testing.T) {
		setScrollRegion(2)
	})

	t.Run("height 1", func(t *testing.T) {
		// Should be a no-op (height < 2).
		setScrollRegion(1)
	})

	t.Run("height 0", func(t *testing.T) {
		// Should be a no-op (height < 2).
		setScrollRegion(0)
	})

	t.Run("clear scroll region", func(t *testing.T) {
		clearScrollRegion()
	})
}
