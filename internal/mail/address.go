package mail

import netmail "net/mail"

// ParseAddress parses one RFC 5322 address. If the address can't be parsed,
// the whole text becomes the display name, so nothing gets lost.
func ParseAddress(raw string) Address {
	t := trimASCIISpace(raw)
	if t == "" {
		return Address{}
	}
	a, err := netmail.ParseAddress(t)
	if err != nil {
		return Address{Name: t}
	}
	return Address{Name: a.Name, Email: a.Address}
}

// ParseAddressList parses an RFC 5322 address list. If the list can't be
// parsed, it returns one address with the whole text as its display name.
func ParseAddressList(raw string) []Address {
	t := trimASCIISpace(raw)
	if t == "" {
		return []Address{}
	}
	list, err := netmail.ParseAddressList(t)
	if err != nil {
		return []Address{{Name: t}}
	}
	out := make([]Address, 0, len(list))
	for _, a := range list {
		out = append(out, Address{Name: a.Name, Email: a.Address})
	}
	return out
}

// FormatAddress formats one address for the frontmatter, like
// "Name <address>".
func FormatAddress(a Address) string {
	name := collapseASCIISpace(a.Name)
	email := trimASCIISpace(a.Email)

	switch {
	case name != "" && email != "":
		return name + " <" + email + ">"
	case email != "":
		return email
	case name != "":
		return name
	default:
		return ""
	}
}

// FormatAddressList formats addresses in input order and drops empty entries.
func FormatAddressList(list []Address) []string {
	out := make([]string, 0, len(list))
	for _, a := range list {
		if s := FormatAddress(a); s != "" {
			out = append(out, s)
		}
	}
	return out
}
