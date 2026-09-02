package mount

import (
	"net/http"
	"mime"
	"path/filepath"
	"strings"

	"github.com/gabriel-vasile/mimetype"
)

var mountMimeOverrides = map[string]string{
	".m3u8": "application/vnd.apple.mpegurl",
	".ts":   "video/mp2t",
}

func detectMountMimeType(filename string, data []byte) string {
	libraryType := strings.TrimSpace(mimetype.Detect(data).String())
	if !isWeakMountMimeType(libraryType) {
		return libraryType
	}

	stdlibType := strings.TrimSpace(http.DetectContentType(data))
	if !isWeakMountMimeType(stdlibType) {
		return stdlibType
	}

	if extensionType := mountMimeByExtension(filename); extensionType != "" {
		return extensionType
	}
	if stdlibType != "" {
		return stdlibType
	}
	return libraryType
}

func selectMimeTypeOnFlush(filename, detected, existing string) string {
	if extensionType := mountMimeByExtension(filename); extensionType != "" {
		if isWeakMountMimeType(detected) {
			if detected == "" && existing != "" && !isWeakMountMimeType(existing) {
				return existing
			}
			return extensionType
		}
		if detected == "" && isWeakMountMimeType(existing) {
			return extensionType
		}
	}

	if detected != "" {
		return detected
	}
	if existing != "" {
		return existing
	}
	return mountMimeByExtension(filename)
}

func mountMimeOverride(filename string) string {
	return mountMimeOverrides[strings.ToLower(filepath.Ext(filename))]
}

func mountMimeByExtension(filename string) string {
	if override := mountMimeOverride(filename); override != "" {
		return override
	}
	return mime.TypeByExtension(strings.ToLower(filepath.Ext(filename)))
}

func isWeakMountMimeType(value string) bool {
	if value == "" {
		return true
	}
	mediaType, _, err := mime.ParseMediaType(value)
	if err == nil {
		value = mediaType
	}
	value = strings.ToLower(strings.TrimSpace(value))
	return value == "application/octet-stream" || value == "text/plain"
}