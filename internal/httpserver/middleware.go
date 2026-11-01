package httpserver

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// SecurityManager holds server CSRF state and validates incoming requests (Section 8.1).
type SecurityManager struct {
	port      string
	csrfToken string
}

// NewSecurityManager generates a cryptographically secure 256-bit CSRF token for the session.
func NewSecurityManager(listenAddr string) (*SecurityManager, error) {
	_, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		port = "8787"
	}

	var tokenBytes [32]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return nil, fmt.Errorf("generating csrf token: %w", err)
	}

	return &SecurityManager{
		port:      port,
		csrfToken: hex.EncodeToString(tokenBytes[:]),
	}, nil
}

// CSRFToken returns the active session CSRF token.
func (sm *SecurityManager) CSRFToken() string {
	return sm.csrfToken
}

// SecurityMiddleware enforces Host, Origin, Sec-Fetch-Site, CSRF, and CSP controls.
func (sm *SecurityManager) SecurityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. Host header validation (Anti-DNS Rebinding)
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if host != "127.0.0.1" && host != "localhost" && host != "::1" {
			http.Error(w, "Forbidden: Invalid Host header", http.StatusForbidden)
			return
		}

		// 2. Set strict response security headers
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Cache-Control", "no-store")

		// 3. Enforce request body limits (1MB for roadmap drafts, 64KB default).
		// Must run before the CSRF check: r.FormValue reads the body.
		maxBytes := int64(64 * 1024)
		if strings.HasPrefix(r.URL.Path, "/roadmaps/drafts") {
			maxBytes = 1048576 // 1MB
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBytes)

		// 4. For state-changing methods (POST, PUT, DELETE, PATCH), enforce Origin, Sec-Fetch-Site, and CSRF
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			// A. Check Sec-Fetch-Site (Fetch Metadata defense-in-depth)
			secFetchSite := r.Header.Get("Sec-Fetch-Site")
			if secFetchSite == "cross-site" || secFetchSite == "same-site" {
				http.Error(w, "Forbidden: Cross-site request rejected", http.StatusForbidden)
				return
			}

			// B. Check Origin if present
			origin := r.Header.Get("Origin")
			if origin != "" {
				if origin == "null" {
					http.Error(w, "Forbidden: Null origin is not allowed", http.StatusForbidden)
					return
				}

				validOrigins := []string{
					"http://127.0.0.1:" + sm.port,
					"http://localhost:" + sm.port,
					"http://127.0.0.1",
					"http://localhost",
				}

				originValid := false
				for _, vo := range validOrigins {
					if strings.EqualFold(origin, vo) {
						originValid = true
						break
					}
				}

				if !originValid {
					http.Error(w, "Forbidden: Invalid Origin header", http.StatusForbidden)
					return
				}
			}

			// C. CSRF Token verification
			// Check X-CSRF-Token header first (htmx), then fallback to form field
			submittedToken := r.Header.Get("X-CSRF-Token")
			if submittedToken == "" {
				submittedToken = r.FormValue("csrf_token")
			}

			if !sm.validateCSRF(submittedToken) {
				http.Error(w, "Forbidden: Invalid or missing CSRF token", http.StatusForbidden)
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

func (sm *SecurityManager) validateCSRF(token string) bool {
	if token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(sm.csrfToken)) == 1
}
