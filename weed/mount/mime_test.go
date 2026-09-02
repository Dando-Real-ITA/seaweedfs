package mount

import "testing"

func TestDetectMountMimeType_PrefersLibraryThenFallbacks(t *testing.T) {
	testCases := []struct {
		name     string
		fileName string
		data     []byte
		want     string
	}{
		{
			name:     "playlist detected by library",
			fileName: "index.m3u8",
			data:     []byte("#EXTM3U\n#EXT-X-VERSION:3\n#EXTINF:10,\nsegment0.ts\n"),
			want:     "application/vnd.apple.mpegurl",
		},
		{
			name:     "plain text keeps detector result",
			fileName: "notes.txt",
			data:     []byte("hello world\n"),
			want:     "text/plain; charset=utf-8",
		},
		{
			name:     "empty playlist falls back to extension",
			fileName: "index.m3u8",
			want:     "application/vnd.apple.mpegurl",
		},
		{
			name:     "empty segment falls back to extension override",
			fileName: "segment.ts",
			want:     "video/mp2t",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := detectMountMimeType(testCase.fileName, testCase.data); got != testCase.want {
				t.Fatalf("detectMountMimeType(%q, %q) = %q, want %q", testCase.fileName, string(testCase.data), got, testCase.want)
			}
		})
	}
}

func TestSelectMimeTypeOnFlush_UsesHLSOverridesForWeakDetectedTypes(t *testing.T) {
	testCases := []struct {
		name     string
		fileName string
		detected string
		existing string
		want     string
	}{
		{
			name:     "playlist from text sniff",
			fileName: "index.m3u8",
			detected: "text/plain; charset=utf-8",
			want:     "application/vnd.apple.mpegurl",
		},
		{
			name:     "segment from octet sniff",
			fileName: "segment.ts",
			detected: "application/octet-stream",
			want:     "video/mp2t",
		},
		{
			name:     "empty new playlist",
			fileName: "index.m3u8",
			want:     "application/vnd.apple.mpegurl",
		},
		{
			name:     "replace weak existing mime on close",
			fileName: "segment.ts",
			existing: "text/plain; charset=utf-8",
			want:     "video/mp2t",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := selectMimeTypeOnFlush(testCase.fileName, testCase.detected, testCase.existing); got != testCase.want {
				t.Fatalf("selectMimeTypeOnFlush(%q, %q, %q) = %q, want %q", testCase.fileName, testCase.detected, testCase.existing, got, testCase.want)
			}
		})
	}
}

func TestSelectMimeTypeOnFlush_KeepsStrongDetectedOrExistingMime(t *testing.T) {
	testCases := []struct {
		name     string
		fileName string
		detected string
		existing string
		want     string
	}{
		{
			name:     "keep strong detected mime",
			fileName: "image.ts",
			detected: "image/png",
			want:     "image/png",
		},
		{
			name:     "preserve existing mime when no new detection",
			fileName: "notes.txt",
			existing: "text/markdown",
			want:     "text/markdown",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := selectMimeTypeOnFlush(testCase.fileName, testCase.detected, testCase.existing); got != testCase.want {
				t.Fatalf("selectMimeTypeOnFlush(%q, %q, %q) = %q, want %q", testCase.fileName, testCase.detected, testCase.existing, got, testCase.want)
			}
		})
	}
}