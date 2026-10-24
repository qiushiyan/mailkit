package mail

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// ParseRef turns what a person can hand over into a Message-ID: the header
// value with or without its angle brackets, or a message:// link dragged
// out of a mail client, whose percent-encoding (in either case) is undone.
func ParseRef(s string) (MessageID, error) {
	ref := strings.TrimSpace(s)
	if strings.HasPrefix(strings.ToLower(ref), "message://") {
		decoded, err := url.PathUnescape(ref[len("message://"):])
		if err != nil {
			return "", fmt.Errorf("message:// URL is not decodable: %w", err)
		}
		ref = decoded
	}
	ref = strings.Trim(ref, "<> ")
	if ref == "" {
		return "", errors.New("nothing to resolve -- pass a Message-ID or a message:// URL")
	}
	return MessageID(ref), nil
}
