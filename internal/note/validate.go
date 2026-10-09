package note

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxPathBytes    = 512
	maxSegmentBytes = 255
	maxTags         = 32
	maxTagRunes     = 64
	maxBodyBytes    = 1 << 20
)

// NormalizePath returns the canonical form of a note path: surrounding
// whitespace and slashes trimmed, repeated slashes collapsed. Paths are
// case-sensitive. Normalizing in one place means the unique-path index
// compares canonical strings only.
func NormalizePath(raw string) (string, error) {
	if !utf8.ValidString(raw) {
		return "", fmt.Errorf("%w: path is not valid UTF-8", ErrInvalid)
	}
	trimmed := strings.TrimFunc(raw, isSlashOrSpace)

	var segments []string
	for _, segment := range strings.Split(trimmed, "/") {
		if segment == "" {
			continue // collapses "a//b"
		}
		switch {
		case segment == "." || segment == "..":
			return "", fmt.Errorf("%w: path segment %q is not allowed", ErrInvalid, segment)
		case len(segment) > maxSegmentBytes:
			return "", fmt.Errorf("%w: path segment exceeds %d bytes", ErrInvalid, maxSegmentBytes)
		case hasControl(segment):
			return "", fmt.Errorf("%w: path contains control characters", ErrInvalid)
		}
		segments = append(segments, segment)
	}
	if len(segments) == 0 {
		return "", fmt.Errorf("%w: path is empty", ErrInvalid)
	}

	normalized := strings.Join(segments, "/")
	if len(normalized) > maxPathBytes {
		return "", fmt.Errorf("%w: path exceeds %d bytes", ErrInvalid, maxPathBytes)
	}
	return normalized, nil
}

// NormalizeFolderPrefix canonicalizes a folder argument. "" means the root;
// anything else is normalized and gets a trailing slash so "eng" can never
// match a sibling folder such as "engineering/".
func NormalizeFolderPrefix(raw string) (string, error) {
	if strings.TrimFunc(raw, isSlashOrSpace) == "" {
		return "", nil
	}
	normalized, err := NormalizePath(raw)
	if err != nil {
		return "", err
	}
	return normalized + "/", nil
}

// NormalizeTags trims, lowercases, drops empties, dedupes, and sorts tags so
// equal tag sets always compare and store identically. The result is never nil.
func NormalizeTags(raw []string) ([]string, error) {
	tags := make([]string, 0, len(raw))
	for _, tag := range raw {
		normalized, err := normalizeTag(tag)
		if err != nil {
			return nil, err
		}
		if normalized != "" {
			tags = append(tags, normalized)
		}
	}
	slices.Sort(tags)
	tags = slices.Compact(tags)
	if len(tags) > maxTags {
		return nil, fmt.Errorf("%w: more than %d tags", ErrInvalid, maxTags)
	}
	return tags, nil
}

// normalizeTag returns "" for a blank tag, which callers drop.
func normalizeTag(raw string) (string, error) {
	// Validate before lowercasing: ToLower silently rewrites bad bytes to U+FFFD.
	if !utf8.ValidString(raw) {
		return "", fmt.Errorf("%w: tag is not valid UTF-8", ErrInvalid)
	}
	tag := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case utf8.RuneCountInString(tag) > maxTagRunes:
		return "", fmt.Errorf("%w: tag exceeds %d characters", ErrInvalid, maxTagRunes)
	case hasControl(tag):
		return "", fmt.Errorf("%w: tag contains control characters", ErrInvalid)
	}
	return tag, nil
}

func validateBody(body string) error {
	if len(body) > maxBodyBytes {
		return fmt.Errorf("%w: body exceeds %d bytes", ErrInvalid, maxBodyBytes)
	}
	if !utf8.ValidString(body) {
		return fmt.Errorf("%w: body is not valid UTF-8", ErrInvalid)
	}
	return nil
}

// isSlashOrSpace lets one trim pass strip mixed edges such as " / a.md / ".
func isSlashOrSpace(r rune) bool { return r == '/' || unicode.IsSpace(r) }

func hasControl(s string) bool {
	return strings.ContainsFunc(s, unicode.IsControl)
}
