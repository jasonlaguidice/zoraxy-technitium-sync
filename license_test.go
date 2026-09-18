package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The release assets are bare binaries — Zoraxy's registry indexer builds
// direct download URLs, so nothing can be wrapped in an archive carrying a
// LICENSE beside it. The binary is therefore the whole of what a recipient
// gets, and AGPL §4, reached through §6, requires a copy of the License to
// reach them with it. Embedding is what satisfies that; this route is what
// makes the embedded copy retrievable by someone holding only the binary.
func TestLicenseRoutesServeTheLicenseTexts(t *testing.T) {
	for _, tc := range []struct {
		file, route, mustContain string
	}{
		{"LICENSE", uiPath + "/license", "GNU AFFERO GENERAL PUBLIC LICENSE"},
		{"NOTICE", uiPath + "/notice", "Copyright (C)"},
	} {
		rec := httptest.NewRecorder()
		licenseHandler(tc.file)(rec, httptest.NewRequest(http.MethodGet, tc.route, nil))

		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d, want 200 — the license text is not being served", tc.file, rec.Code)
			continue
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
			t.Errorf("%s: Content-Type %q, want text/plain", tc.file, ct)
		}
		if body := rec.Body.String(); !strings.Contains(body, tc.mustContain) {
			t.Errorf("%s: served %d bytes not containing %q — what is embedded is not the file at the repository root", tc.file, len(body), tc.mustContain)
		}
	}
}

// RegisterLicenseRoutes must wire both routes onto the same mux the rest of
// the plugin's handlers use, not the process-wide http.DefaultServeMux.
func TestRegisterLicenseRoutesUsesTheGivenMux(t *testing.T) {
	mux := http.NewServeMux()
	RegisterLicenseRoutes(uiPath, mux)

	for _, route := range []string{uiPath + "/license", uiPath + "/notice"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, route, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d, want 200", route, rec.Code)
		}
	}
}
