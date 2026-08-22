package mail

import (
	netmail "net/mail"
	"strings"
)

// ParseAddress accepts "Name <a@b>" or "a@b". A value that will not parse
// comes back as an Address whose Email is the trimmed input, so a malformed
// From header still displays rather than vanishing.
func ParseAddress(s string) Address {
	s = strings.TrimSpace(s)
	if s == "" {
		return Address{}
	}
	if a, err := netmail.ParseAddress(s); err == nil {
		return Address{Name: a.Name, Email: a.Address}
	}
	return Address{Email: strings.Trim(s, "<>")}
}

// ParseAddressList splits a header's worth of addresses.
func ParseAddressList(s string) []Address {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if list, err := netmail.ParseAddressList(s); err == nil {
		out := make([]Address, 0, len(list))
		for _, a := range list {
			out = append(out, Address{Name: a.Name, Email: a.Address})
		}
		return out
	}
	var out []Address
	for part := range strings.SplitSeq(s, ",") {
		if a := ParseAddress(part); a.Email != "" {
			out = append(out, a)
		}
	}
	return out
}

// String renders "Name <email>" or the bare email.
func (a Address) String() string {
	if a.Name != "" && a.Name != a.Email {
		return a.Name + " <" + a.Email + ">"
	}
	return a.Email
}

// Domain is the registrable-ish tail of the address, so
// no-reply@mail.corp.co.uk still matches notifications@corp.co.uk.
func (a Address) Domain() string {
	_, host, ok := strings.Cut(a.Email, "@")
	if !ok {
		return ""
	}
	host = strings.ToLower(strings.TrimRight(host, ">."))
	parts := strings.Split(host, ".")
	if len(parts) > 2 {
		switch parts[len(parts)-2] {
		case "co", "com", "org", "net", "ac", "gov":
			return strings.Join(parts[len(parts)-3:], ".")
		}
	}
	if len(parts) >= 2 {
		return strings.Join(parts[len(parts)-2:], ".")
	}
	return host
}

// matchesSelector reports whether the address matches a Criteria selector:
// an exact address, or a bare domain the address sits under.
func (a Address) matchesSelector(sel string) bool {
	sel = strings.ToLower(strings.TrimSpace(sel))
	email := strings.ToLower(a.Email)
	if sel == "" || email == "" {
		return false
	}
	if strings.Contains(sel, "@") {
		return email == sel
	}
	_, host, _ := strings.Cut(email, "@")
	return host == sel || strings.HasSuffix(host, "."+sel)
}

// Joined renders a list for output.
func Joined(as []Address) string {
	parts := make([]string, 0, len(as))
	for _, a := range as {
		parts = append(parts, a.String())
	}
	return strings.Join(parts, ", ")
}
