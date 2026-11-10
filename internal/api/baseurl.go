package api

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// APIPath is where the customer API lives under a host.
const APIPath = "/api/v1"

// NormalizeBaseURL turns what a user types ("https://api.talksasa.com",
// "api.talksasa.com/", ".../api/v1") into the API's base URL.
//
// Plain http is refused except on this machine: the token travels in every
// request.
func NormalizeBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("the API address is empty")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}

	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%q is not an address: %v", raw, err)
	}
	if u.Host == "" {
		return "", fmt.Errorf("%q has no host", raw)
	}
	if u.User != nil {
		return "", fmt.Errorf("put the token in TALKSASA_TOKEN or run login, not in the address")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("the API address takes no query or fragment")
	}

	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopback(u.Hostname()) {
			return "", fmt.Errorf("refusing plain http to %s: the token would cross the network unencrypted. Use https", u.Host)
		}
	default:
		return "", fmt.Errorf("the API address must start with https://")
	}

	path := strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(path, APIPath) {
		path += APIPath
	}

	return u.Scheme + "://" + u.Host + path, nil
}

// Origin is the scheme and host of a base URL: where the portal lives.
func Origin(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return baseURL
	}
	return u.Scheme + "://" + u.Host
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
