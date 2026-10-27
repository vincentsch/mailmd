// Package presets knows the IMAP settings of common mail providers, so a user
// can connect with only an email address.
package presets

import "strings"

// Preset holds the IMAP settings of one provider.
type Preset struct {
	// Name is the provider's name, like "Gmail". Short is a lower-case name
	// that works as an account name, like "gmail".
	Name, Short string
	Domains     []string
	Host        string
	Port        int
	TLS         string
	// Password tells the user which password to use, for example that the
	// provider needs an app password.
	Password string
	// UseOutlook is set for providers that no longer accept passwords over
	// IMAP. Those accounts connect with "mailmd connect outlook".
	UseOutlook bool
}

const appPassword = "needs an app password. Your normal password doesn't work over IMAP."

// List holds the known providers.
var List = []Preset{
	{Name: "Gmail", Short: "gmail", Domains: []string{"gmail.com", "googlemail.com"}, Host: "imap.gmail.com", Port: 993, TLS: "implicit",
		Password: "Gmail " + appPassword + " Turn on 2-Step Verification, then create one at https://myaccount.google.com/apppasswords"},
	{Name: "iCloud Mail", Short: "icloud", Domains: []string{"icloud.com", "me.com", "mac.com"}, Host: "imap.mail.me.com", Port: 993, TLS: "implicit",
		Password: "iCloud Mail " + appPassword + " Create one at https://account.apple.com under \"Sign-In and Security\", then \"App-Specific Passwords\"."},
	{Name: "Fastmail", Short: "fastmail", Domains: []string{"fastmail.com", "fastmail.fm"}, Host: "imap.fastmail.com", Port: 993, TLS: "implicit",
		Password: "Fastmail " + appPassword + " Create one in Fastmail's settings under \"Privacy & Security\"."},
	{Name: "Yahoo Mail", Short: "yahoo", Domains: []string{"yahoo.com", "ymail.com", "rocketmail.com", "yahoo.co.uk", "yahoo.de", "yahoo.fr", "yahoo.it", "yahoo.es"}, Host: "imap.mail.yahoo.com", Port: 993, TLS: "implicit",
		Password: "Yahoo Mail " + appPassword + " Create one under \"Account security\", then \"Generate app password\"."},
	{Name: "AOL Mail", Short: "aol", Domains: []string{"aol.com"}, Host: "imap.aol.com", Port: 993, TLS: "implicit",
		Password: "AOL Mail " + appPassword + " Create one under \"Account security\", then \"Generate app password\"."},
	{Name: "Outlook.com", Short: "outlook", Domains: []string{"outlook.com", "hotmail.com", "live.com", "msn.com", "outlook.de", "hotmail.de", "hotmail.co.uk"}, Host: "outlook.office365.com", Port: 993, TLS: "implicit",
		UseOutlook: true},
	{Name: "GMX", Short: "gmx", Domains: []string{"gmx.de", "gmx.net", "gmx.at", "gmx.ch"}, Host: "imap.gmx.net", Port: 993, TLS: "implicit",
		Password: "Use your GMX password. Turn on IMAP access first in the GMX web mail settings, under \"POP3/IMAP\"."},
	{Name: "GMX", Short: "gmx", Domains: []string{"gmx.com"}, Host: "imap.gmx.com", Port: 993, TLS: "implicit",
		Password: "Use your GMX password. Turn on IMAP access first in the GMX web mail settings, under \"POP3/IMAP\"."},
	{Name: "WEB.DE", Short: "webde", Domains: []string{"web.de"}, Host: "imap.web.de", Port: 993, TLS: "implicit",
		Password: "Use your WEB.DE password. Turn on IMAP access first in the WEB.DE web mail settings, under \"POP3/IMAP\"."},
	{Name: "Posteo", Short: "posteo", Domains: []string{"posteo.de", "posteo.net", "posteo.eu", "posteo.at", "posteo.ch", "posteo.org"}, Host: "posteo.de", Port: 993, TLS: "implicit",
		Password: "Use your Posteo password."},
	{Name: "mailbox.org", Short: "mailboxorg", Domains: []string{"mailbox.org"}, Host: "imap.mailbox.org", Port: 993, TLS: "implicit",
		Password: "Use your mailbox.org password, or an app password if you turned on two-factor login."},
	{Name: "Zoho Mail", Short: "zoho", Domains: []string{"zoho.com", "zohomail.com"}, Host: "imap.zoho.com", Port: 993, TLS: "implicit",
		Password: "Turn on IMAP access in Zoho Mail's settings first. With two-factor login, Zoho Mail needs an app password."},
	{Name: "Zoho Mail", Short: "zoho", Domains: []string{"zoho.eu", "zohomail.eu"}, Host: "imap.zoho.eu", Port: 993, TLS: "implicit",
		Password: "Turn on IMAP access in Zoho Mail's settings first. With two-factor login, Zoho Mail needs an app password."},
	{Name: "Telekom Mail", Short: "tonline", Domains: []string{"t-online.de"}, Host: "secureimap.t-online.de", Port: 993, TLS: "implicit",
		Password: "Use your email password from the Telekom mail settings. It is not your Telekom login password."},
	{Name: "Yandex Mail", Short: "yandex", Domains: []string{"yandex.com", "yandex.ru"}, Host: "imap.yandex.com", Port: 993, TLS: "implicit",
		Password: "Yandex Mail " + appPassword + " Create one in your Yandex ID security settings."},
}

// ForEmail returns the preset for the domain of an email address.
func ForEmail(email string) (Preset, bool) {
	_, domain, ok := strings.Cut(strings.ToLower(strings.TrimSpace(email)), "@")
	if !ok {
		return Preset{}, false
	}
	return ForDomain(domain)
}

// ForDomain returns the preset for a mail domain.
func ForDomain(domain string) (Preset, bool) {
	domain = strings.ToLower(strings.TrimSuffix(domain, "."))
	for _, p := range List {
		for _, d := range p.Domains {
			if d == domain {
				return p, true
			}
		}
	}
	return Preset{}, false
}

// ForHost returns the preset that uses an IMAP host, so connect can show
// the right password hint when the user gave only --host.
func ForHost(host string) (Preset, bool) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, p := range List {
		if p.Host == host {
			return p, true
		}
	}
	return Preset{}, false
}
