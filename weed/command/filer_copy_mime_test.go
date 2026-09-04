package command

import (
	"strings"
	"testing"
)

func TestDetectMimeTypeFromHeadPrefersJavaScriptExtensionOverPlainText(t *testing.T) {
	got := detectMimeTypeFromHead("app.js", []byte("console.log('hello')\n"))
	if got == "" {
		t.Fatal("expected a detected mime type")
	}
	if strings.HasPrefix(got, "text/plain") {
		t.Fatalf("detectMimeTypeFromHead returned weak mime %q", got)
	}
	if !strings.Contains(got, "javascript") {
		t.Fatalf("detectMimeTypeFromHead(%q) = %q, want a javascript mime type", "app.js", got)
	}
}
