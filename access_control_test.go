package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func newTestAccessControl(t *testing.T, authURL string) *accessControl {
	t.Helper()
	access, err := newAccessControl(authURL, "MULATINHO.NET", "https://jobs.example.com")
	if err != nil {
		t.Fatalf("create access control: %v", err)
	}
	return access
}

func TestAccessControlProtectsEveryRoute(t *testing.T) {
	var authCalls atomic.Int32
	authProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authCalls.Add(1)
		if r.Header.Get("Cookie") != "oauth_session=valid" {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		w.Header().Set("X-Auth-Request-Email", "operator@mulatinho.net")
		w.WriteHeader(http.StatusAccepted)
	}))
	defer authProxy.Close()

	access := newTestAccessControl(t, authProxy.URL+"/oauth2/auth")
	var reached atomic.Int32
	protected := access.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))

	for _, path := range []string{
		"/",
		"/partials/dashboard",
		"/api/config",
		"/api/cronjobs",
		"/api/jobs/example",
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.AddCookie(&http.Cookie{Name: "oauth_session", Value: "valid"})
		response := httptest.NewRecorder()
		protected.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent {
			t.Errorf("GET %s returned status %d: %s", path, response.Code, response.Body.String())
		}
	}

	if reached.Load() != 5 || authCalls.Load() != 5 {
		t.Fatalf("protected routes reached=%d auth calls=%d, want 5 each", reached.Load(), authCalls.Load())
	}
}

func TestAccessControlCanBeDisabled(t *testing.T) {
	var authCalls atomic.Int32
	authProxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		authCalls.Add(1)
	}))
	defer authProxy.Close()

	access := newTestAccessControl(t, authProxy.URL+"/oauth2/auth")
	access.enabled = false
	protected := access.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	protected.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("GET with authentication disabled returned status %d", response.Code)
	}
	if authCalls.Load() != 0 {
		t.Fatalf("auth endpoint called %d times with authentication disabled", authCalls.Load())
	}
	if response.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatal("security headers were not applied with authentication disabled")
	}

	request = httptest.NewRequest(http.MethodPatch, "/api/cronjobs/nightly", strings.NewReader(`{"suspend":true}`))
	response = httptest.NewRecorder()
	protected.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("PATCH without Origin returned status %d, want 403", response.Code)
	}

	request = httptest.NewRequest(http.MethodPatch, "/api/cronjobs/nightly", strings.NewReader(`{"suspend":true}`))
	request.Header.Set("Origin", "https://jobs.example.com")
	response = httptest.NewRecorder()
	protected.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("same-origin PATCH with authentication disabled returned status %d", response.Code)
	}
	if authCalls.Load() != 0 {
		t.Fatalf("auth endpoint called %d times with authentication disabled", authCalls.Load())
	}
}

func TestAccessControlDoesNotTrustClientIdentityHeaders(t *testing.T) {
	authProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" {
			t.Error("unexpected cookie sent to auth endpoint")
		}
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
	}))
	defer authProxy.Close()

	access := newTestAccessControl(t, authProxy.URL+"/oauth2/auth")
	protected := access.middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("unauthenticated request reached handler")
	}))
	request := httptest.NewRequest(http.MethodGet, "/api/cronjobs", nil)
	request.Header.Set("X-Auth-Request-Email", "operator@mulatinho.net")
	request.Header.Set("X-Forwarded-Email", "operator@mulatinho.net")
	response := httptest.NewRecorder()

	protected.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("spoofed identity header returned status %d, want 401", response.Code)
	}
}

func TestAccessControlRejectsOtherEmailDomains(t *testing.T) {
	authProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Auth-Request-Email", "operator@example.org")
		w.WriteHeader(http.StatusAccepted)
	}))
	defer authProxy.Close()

	access := newTestAccessControl(t, authProxy.URL+"/oauth2/auth")
	protected := access.middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("unauthorized domain reached handler")
	}))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(&http.Cookie{Name: "oauth_session", Value: "valid"})
	response := httptest.NewRecorder()

	protected.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("unauthorized domain returned status %d, want 403", response.Code)
	}
}

func TestAccessControlRequiresSameOriginForMutations(t *testing.T) {
	var authCalls atomic.Int32
	authProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		authCalls.Add(1)
		w.Header().Set("X-Auth-Request-Email", "operator@mulatinho.net")
		w.WriteHeader(http.StatusAccepted)
	}))
	defer authProxy.Close()

	access := newTestAccessControl(t, authProxy.URL+"/oauth2/auth")
	protected := access.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for _, origin := range []string{"", "https://attacker.example"} {
		request := httptest.NewRequest(http.MethodPatch, "/api/cronjobs/nightly", strings.NewReader(`{"suspend":true}`))
		request.AddCookie(&http.Cookie{Name: "oauth_session", Value: "valid"})
		if origin != "" {
			request.Header.Set("Origin", origin)
		}
		response := httptest.NewRecorder()
		protected.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Errorf("PATCH with Origin %q returned status %d, want 403", origin, response.Code)
		}
	}
	if authCalls.Load() != 0 {
		t.Fatalf("auth endpoint called %d times for invalid-origin requests", authCalls.Load())
	}

	request := httptest.NewRequest(http.MethodPatch, "/api/cronjobs/nightly", strings.NewReader(`{"suspend":true}`))
	request.AddCookie(&http.Cookie{Name: "oauth_session", Value: "valid"})
	request.Header.Set("Origin", "https://jobs.example.com")
	response := httptest.NewRecorder()
	protected.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("same-origin PATCH returned status %d", response.Code)
	}
}

func TestNewAccessControlRequiresSecurePublicURLAndValidDomain(t *testing.T) {
	for _, config := range [][3]string{
		{"http://oauth2-proxy/oauth2/auth", "example.com", "http://jobs.example.com"},
		{"http://oauth2-proxy/oauth2/auth", "not a domain", "https://jobs.example.com"},
		{"//oauth2-proxy/oauth2/auth", "example.com", "https://jobs.example.com"},
	} {
		if _, err := newAccessControl(config[0], config[1], config[2]); err == nil {
			t.Errorf("newAccessControl(%q, %q, %q) succeeded, want error", config[0], config[1], config[2])
		}
	}
}

func TestNewAccessControlNormalizesDefaultHTTPSPort(t *testing.T) {
	access, err := newAccessControl(
		"http://oauth2-proxy/oauth2/auth",
		"example.com",
		"https://JOBS.EXAMPLE.COM:443/",
	)
	if err != nil {
		t.Fatalf("newAccessControl: %v", err)
	}
	if access.publicOrigin != "https://jobs.example.com" {
		t.Fatalf("public origin = %q, want normalized origin", access.publicOrigin)
	}
}

func TestNewAccessControlAllowsHTTPOnlyForLocalhost(t *testing.T) {
	access, err := newAccessControl(
		"http://oauth2-proxy/oauth2/auth",
		"example.com",
		"http://localhost:8080",
	)
	if err != nil {
		t.Fatalf("newAccessControl with localhost origin: %v", err)
	}
	if access.publicOrigin != "http://localhost:8080" {
		t.Fatalf("public origin = %q, want http://localhost:8080", access.publicOrigin)
	}

	if _, err := newAccessControl(
		"http://oauth2-proxy/oauth2/auth",
		"example.com",
		"http://jobs.example.com",
	); err == nil {
		t.Fatal("newAccessControl accepted a non-local HTTP origin")
	}
}

func TestAccessControlFailsClosedWhenAuthServiceIsUnavailable(t *testing.T) {
	authProxy := httptest.NewServer(http.NotFoundHandler())
	authURL := authProxy.URL + "/oauth2/auth"
	authProxy.Close()

	access := newTestAccessControl(t, authURL)
	protected := access.middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("request reached handler while auth service was unavailable")
	}))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(&http.Cookie{Name: "oauth_session", Value: "valid"})
	response := httptest.NewRecorder()

	protected.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("auth service failure returned status %d, want 503", response.Code)
	}
}
