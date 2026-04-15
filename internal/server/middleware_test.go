package server

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestContentTypeWrapper verifies that Content-Type is only set if missing
// when the status code matches the trigger status.
func TestContentTypeWrapper(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		handler         http.HandlerFunc
		triggerStatus   int
		wantStatus      int
		wantContentType string
		wantBody        string
	}{
		{
			name: "SetsContentTypeOnTriggerStatusMissingHeader",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
				w.Write([]byte(`{"error":"timeout"}`))
			},
			triggerStatus:   http.StatusServiceUnavailable,
			wantStatus:      http.StatusServiceUnavailable,
			wantContentType: "application/json",
			wantBody:        `{"error":"timeout"}`,
		},
		{
			name: "RespectsExistingContentTypeOnTriggerStatus",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/plain")
				w.WriteHeader(http.StatusServiceUnavailable)
				w.Write([]byte("timeout error"))
			},
			triggerStatus:   http.StatusServiceUnavailable,
			wantStatus:      http.StatusServiceUnavailable,
			wantContentType: "text/plain",
			wantBody:        "timeout error",
		},
		{
			name: "IgnoresNonTriggerStatus",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("ok"))
			},
			triggerStatus:   http.StatusServiceUnavailable,
			wantStatus:      http.StatusOK,
			wantContentType: "", // Not set by wrapper
			wantBody:        "ok",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			wrapper := &contentTypeWrapper{
				ResponseWriter: w,
				contentType:    "application/json",
				triggerStatus:  tt.triggerStatus,
			}

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			tt.handler(wrapper, req)

			assertRecorderStatus(t, w, tt.wantStatus)

			resp := w.Result()
			defer resp.Body.Close()

			gotCT := resp.Header.Get("Content-Type")
			if tt.wantContentType != "" {
				if gotCT != tt.wantContentType {
					t.Errorf("Content-Type = %q, want %q", gotCT, tt.wantContentType)
				}
			} else if gotCT == "application/json" {
				// Wrapper shouldn't improperly force its Content-Type
				t.Errorf("Content-Type = %q, unexpectedly forced by wrapper", gotCT)
			}

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("failed to read body: %v", err)
			}
			if string(body) != tt.wantBody {
				t.Errorf("body = %q, want %q", string(body), tt.wantBody)
			}
		})
	}
}

// TestMiddlewareTimeout verifies that API routes are wrapped with timeout
// middleware (which produces a 503 JSON timeout response when the handler
// exceeds the configured duration) and that export/SPA routes are NOT wrapped.
func TestMiddlewareTimeout(t *testing.T) {
	t.Parallel()

	srv := testServer(
		t, 10*time.Millisecond,
		withHandlerDelay(100*time.Millisecond),
	)
	// Use a real listener to discover the bound port, then
	// rebuild Handler() with the correct port in the Host
	// allowlist.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	srv.SetPort(port)
	ts := httptest.NewUnstartedServer(srv.Handler())
	ts.Listener = ln
	ts.Start()
	t.Cleanup(ts.Close)

	tests := []struct {
		name        string
		path        string
		wantTimeout bool
		wantStatus  int // Only checked if wantTimeout is false
	}{
		{"Wrapped_ListSessions", "/api/v1/sessions", true, 0},
		{"Wrapped_GetStats", "/api/v1/stats", true, 0},
		{"Unwrapped_ExportSession", "/api/v1/sessions/invalid-id/export", false, http.StatusNotFound},
		{"Unwrapped_SPA", "/", false, http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resp, err := ts.Client().Get(ts.URL + tt.path)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			if tt.wantTimeout {
				assertTimeoutResponse(t, resp)
			} else {
				if isTimeoutResponse(t, resp) {
					t.Errorf("%s: unexpected timeout for unwrapped route", tt.path)
				}
				if resp.StatusCode != tt.wantStatus {
					t.Errorf("%s: status = %d, want %d", tt.path, resp.StatusCode, tt.wantStatus)
				}
			}
		})
	}
}

func TestCSPMiddlewareSetsHeaderOnNonAPIRoutes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		path          string
		host          string
		port          int
		publicOrigins []string
		bindAllIPs    map[string]bool
		wantCSP       bool
		wantParts     []string
		wantAbsent    []string // substrings that must NOT appear
	}{
		{
			name:    "SPA_root_gets_CSP_with_pinned_origin",
			path:    "/",
			host:    "127.0.0.1",
			port:    8081,
			wantCSP: true,
			wantParts: []string{
				"script-src 'self' http://127.0.0.1:8081",
				"default-src 'self' http://127.0.0.1:8081",
				"connect-src 'self' http://127.0.0.1:8081",
				"ws://127.0.0.1:8081",
				"style-src 'self' http://127.0.0.1:8081 'unsafe-inline' https://fonts.googleapis.com",
				"font-src 'self' http://127.0.0.1:8081 data: https://fonts.gstatic.com",
				"frame-ancestors 'none'",
			},
		},
		{
			name:    "SPA_subpath_gets_CSP",
			path:    "/sessions/abc",
			host:    "127.0.0.1",
			port:    9090,
			wantCSP: true,
			wantParts: []string{
				"http://127.0.0.1:9090",
				"ws://127.0.0.1:9090",
			},
		},
		{
			name:    "API_route_no_CSP",
			path:    "/api/v1/sessions",
			host:    "127.0.0.1",
			port:    8081,
			wantCSP: false,
		},
		{
			name:    "API_subpath_no_CSP",
			path:    "/api/v1/stats",
			host:    "127.0.0.1",
			port:    8081,
			wantCSP: false,
		},
		{
			name:    "IPv6_loopback_brackets",
			path:    "/",
			host:    "::1",
			port:    8081,
			wantCSP: true,
			wantParts: []string{
				"script-src 'self' http://[::1]:8081",
				"connect-src",
				"ws://[::1]:8081",
				"http://127.0.0.1:8081",
			},
		},
		{
			name: "BindAll_connect_src_includes_LAN_IPs",
			path: "/",
			host: "0.0.0.0",
			port: 8080,
			bindAllIPs: map[string]bool{
				"127.0.0.1":   true,
				"::1":         true,
				"192.168.1.5": true,
			},
			wantCSP: true,
			wantParts: []string{
				// Pinned origin in all directives
				"script-src 'self' http://0.0.0.0:8080",
				// LAN IPs in connect-src
				"http://192.168.1.5:8080",
				"ws://192.168.1.5:8080",
				"http://127.0.0.1:8080",
				"http://localhost:8080",
			},
			wantAbsent: []string{
				// LAN IPs must NOT be in script-src
				"script-src 'self' http://0.0.0.0:8080 http://192",
			},
		},
		{
			name:          "PublicOrigin_in_connect_src_only",
			path:          "/",
			host:          "127.0.0.1",
			port:          8081,
			publicOrigins: []string{"https://view.example.com"},
			wantCSP:       true,
			wantParts: []string{
				// Pinned origin in script-src
				"script-src 'self' http://127.0.0.1:8081",
				// Public origin in connect-src
				"https://view.example.com",
				"wss://view.example.com",
			},
			wantAbsent: []string{
				// Public origin must NOT be in script-src
				"script-src 'self' http://127.0.0.1:8081 https://view",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
			handler := cspMiddleware(tt.host, tt.port, tt.publicOrigins, tt.bindAllIPs, "", inner)

			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			csp := w.Header().Get("Content-Security-Policy")
			if tt.wantCSP {
				if csp == "" {
					t.Fatal("expected CSP header, got empty")
				}
				for _, part := range tt.wantParts {
					if !strings.Contains(csp, part) {
						t.Errorf("CSP missing %q; got %q", part, csp)
					}
				}
				for _, absent := range tt.wantAbsent {
					if strings.Contains(csp, absent) {
						t.Errorf("CSP should not contain %q; got %q", absent, csp)
					}
				}
				xfo := w.Header().Get("X-Frame-Options")
				if xfo != "DENY" {
					t.Errorf("expected X-Frame-Options DENY, got %q", xfo)
				}
			} else {
				if csp != "" {
					t.Errorf("expected no CSP header on API route, got %q", csp)
				}
			}
		})
	}
}

func TestCORSMiddlewareMergesVaryHeader(t *testing.T) {
	t.Parallel()

	allowedOrigins := map[string]bool{
		"http://127.0.0.1:8080": true,
	}
	cors := corsMiddleware(
		allowedOrigins, false, 8080, nil,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Vary", "Accept-Encoding")
		cors.ServeHTTP(w, r)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/stats", nil)
	req.Header.Set("Origin", "http://127.0.0.1:8080")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assertRecorderStatus(t, w, http.StatusOK)
	vary := w.Header().Get("Vary")
	if !strings.Contains(vary, "Accept-Encoding") {
		t.Fatalf("expected Vary to include Accept-Encoding, got %q", vary)
	}
	if !strings.Contains(vary, "Origin") {
		t.Fatalf("expected Vary to include Origin, got %q", vary)
	}
}
