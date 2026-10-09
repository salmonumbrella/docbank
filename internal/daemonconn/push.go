package daemonconn

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// NewPushConnection connects only to the explicitly selected keyed daemon.
// Redirects are errors: a daemon must not forward credentials to another origin.
func NewPushConnection(baseURL, key string) (*Connection, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, errors.New("push requires an http(s) daemon origin")
	}
	if key == "" || strings.ContainsAny(key, "\r\n") {
		return nil, errors.New("push requires a nonempty API key")
	}
	connection := New(strings.TrimRight(baseURL, "/"), key)
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("push requires the default HTTP transport")
	}
	transport := defaultTransport.Clone()
	transport.ResponseHeaderTimeout = 30 * time.Second
	connection.hc = &http.Client{Transport: transport, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	return connection, nil
}
