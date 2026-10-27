package presets

import "testing"

func TestForEmail(t *testing.T) {
	for email, host := range map[string]string{
		"Ann@GMAIL.com":          "imap.gmail.com",
		" ann@googlemail.com ":   "imap.gmail.com",
		"ann@gmx.de":             "imap.gmx.net",
		"ann@gmx.com":            "imap.gmx.com",
		"ann@zohomail.eu":        "imap.zoho.eu",
		"ann@t-online.de":        "secureimap.t-online.de",
		"ann@hotmail.com":        "outlook.office365.com",
		"ann@example.com":        "",
		"not an address":         "",
		"ann@mail.example.co.uk": "",
	} {
		p, ok := ForEmail(email)
		if ok != (host != "") || p.Host != host {
			t.Errorf("ForEmail(%q) = %q, %v; want %q", email, p.Host, ok, host)
		}
	}
	if p, _ := ForEmail("ann@outlook.com"); !p.UseOutlook {
		t.Error("Outlook.com addresses must point to mailmd connect outlook")
	}
}

func TestForHost(t *testing.T) {
	if p, ok := ForHost("IMAP.gmail.com."); !ok || p.Short != "gmail" {
		t.Fatalf("ForHost found %+v, %v", p, ok)
	}
	if _, ok := ForHost("imap.example.com"); ok {
		t.Fatal("unknown host matched a preset")
	}
}

func TestPresetsAreComplete(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range List {
		if p.Name == "" || p.Short == "" || p.Host == "" || p.Port == 0 || len(p.Domains) == 0 {
			t.Errorf("incomplete preset %+v", p)
		}
		if p.Password == "" && !p.UseOutlook {
			t.Errorf("%s has no password hint", p.Name)
		}
		for _, d := range p.Domains {
			if seen[d] {
				t.Errorf("domain %s is listed twice", d)
			}
			seen[d] = true
		}
	}
}
