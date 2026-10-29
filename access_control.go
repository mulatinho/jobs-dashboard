package main

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type accessControl struct {
	authURL      string
	emailDomain  string
	publicOrigin string
	client       *http.Client
	enabled      bool
}

func newAccessControl(authURL, emailDomain, publicURL string) (*accessControl, error) {
	authEndpoint, err := url.Parse(authURL)
	if err != nil || (authEndpoint.Scheme != "http" && authEndpoint.Scheme != "https") ||
		authEndpoint.Host == "" || authEndpoint.User != nil ||
		authEndpoint.RawQuery != "" || authEndpoint.Fragment != "" {
		return nil, fmt.Errorf("OAUTH2_PROXY_AUTH_URL must be an absolute HTTP(S) URL without credentials, query, or fragment")
	}

	emailDomain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(emailDomain)), ".")
	if !validEmailDomain(emailDomain) {
		return nil, fmt.Errorf("AUTH_EMAIL_DOMAIN must be a valid DNS domain")
	}

	publicEndpoint, err := url.Parse(publicURL)
	if err != nil || (publicEndpoint.Scheme != "https" && publicEndpoint.Scheme != "http") || publicEndpoint.Host == "" ||
		publicEndpoint.User != nil || publicEndpoint.Path != "" && publicEndpoint.Path != "/" ||
		publicEndpoint.RawQuery != "" || publicEndpoint.Fragment != "" {
		return nil, fmt.Errorf("PUBLIC_URL must be an HTTP(S) origin without a path, query, or fragment")
	}

	publicHost := strings.ToLower(publicEndpoint.Hostname())
	if publicEndpoint.Scheme != "https" &&
		publicHost != "localhost" &&
		(net.ParseIP(publicHost) == nil || !net.ParseIP(publicHost).IsLoopback()) {
		return nil, fmt.Errorf("PUBLIC_URL must use HTTPS except for localhost")
	}
	if strings.Contains(publicHost, ":") {
		publicHost = "[" + publicHost + "]"
	}
	defaultPort := "443"
	if publicEndpoint.Scheme == "http" {
		defaultPort = "80"
	}
	if port := publicEndpoint.Port(); port != "" && port != defaultPort {
		publicHost += ":" + port
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil

	return &accessControl{
		authURL:      authEndpoint.String(),
		emailDomain:  emailDomain,
		publicOrigin: publicEndpoint.Scheme + "://" + publicHost,
		enabled:      true,
		client: &http.Client{
			Transport: transport,
			Timeout:   3 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func validEmailDomain(domain string) bool {
	if domain == "" || len(domain) > 253 {
		return false
	}
	for _, label := range strings.Split(domain, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') &&
				(character < '0' || character > '9') &&
				character != '-' {
				return false
			}
		}
	}
	return true
}

func (a *accessControl) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Cache-Control", "no-store")

		if !isSafeMethod(r.Method) && r.Header.Get("Origin") != a.publicOrigin {
			http.Error(w, "request origin is not allowed", http.StatusForbidden)
			return
		}

		if !a.enabled {
			next.ServeHTTP(w, r)
			return
		}

		authRequest, err := http.NewRequestWithContext(r.Context(), http.MethodGet, a.authURL, nil)
		if err != nil {
			http.Error(w, "authentication service is unavailable", http.StatusServiceUnavailable)
			return
		}
		if cookie := r.Header.Get("Cookie"); cookie != "" {
			authRequest.Header.Set("Cookie", cookie)
		}
		authRequest.Header.Set("X-Forwarded-Uri", r.URL.RequestURI())
		authRequest.Header.Set("X-Forwarded-Method", r.Method)

		authResponse, err := a.client.Do(authRequest)
		if err != nil {
			http.Error(w, "authentication service is unavailable", http.StatusServiceUnavailable)
			return
		}
		defer authResponse.Body.Close()

		if authResponse.StatusCode < http.StatusOK || authResponse.StatusCode >= http.StatusMultipleChoices {
			if authResponse.StatusCode == http.StatusUnauthorized ||
				authResponse.StatusCode == http.StatusForbidden {
				http.Error(w, "authentication required", http.StatusUnauthorized)
				return
			}
			http.Error(w, "authentication service is unavailable", http.StatusServiceUnavailable)
			return
		}

		email := strings.TrimSpace(authResponse.Header.Get("X-Auth-Request-Email"))
		localPart, domain, found := strings.Cut(email, "@")
		if !found || localPart == "" ||
			!strings.EqualFold(strings.TrimSuffix(domain, "."), a.emailDomain) {
			http.Error(w, "account is not authorized", http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func isSafeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}
