package wardryxproxy

import (
	"net/url"
	"testing"
)

// The upstream's authority is the host:port the default transport dials, with
// the default port of the scheme filled in when the URL names none. A wrong
// default would make the dial guard refuse wardryx itself.
func TestAuthorityFillsTheDefaultPortOfTheScheme(t *testing.T) {
	cases := map[string]string{
		"http://wardryx":          "wardryx:80",
		"https://wardryx":         "wardryx:443",
		"http://wardryx:8090":     "wardryx:8090",
		"https://wardryx:8443":    "wardryx:8443",
		"http://[::1]:8090":       "[::1]:8090",
		"http://[::1]":            "[::1]:80",
		"https://WARDRYX.example": "WARDRYX.example:443",
	}
	for raw, want := range cases {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if got := authority(u); got != want {
			t.Errorf("authority(%s) = %q, want %q", raw, got, want)
		}
	}
}
