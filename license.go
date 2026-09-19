package main

import "net/http"

// Serving the license texts is a compliance requirement, not a courtesy.
//
// AGPL §6 conveys object code under the conditions of §4, and §4 requires that
// whoever receives that object code receives a copy of the License along with
// it. The release assets here are bare binaries: Zoraxy's registry indexer
// builds direct download URLs, so they cannot be wrapped in an archive
// carrying a LICENSE beside them. The binary is therefore the whole of what a
// recipient gets, and the only place the text can travel is inside it.
//
// §13 is the half that applies to something like this even when nobody
// downloads anything: a user interacting with the program remotely over a
// network must be offered the Corresponding Source. The plugin's declared
// URL (see main.go's IntroSpec) is rendered by Zoraxy's plugin list as a
// working link, and that link is the offer.
//
// The obligation arrives with third_party/zoraxy/, the git submodule this
// plugin's Zoraxy SDK dependency lives in (pinned to v3.3.4 of Zoraxy's own
// AGPL source tree). It is inherited rather than chosen, and it is not ours
// to waive on tobychui's behalf.
func licenseHandler(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := content.ReadFile(name)
		if err != nil {
			// Unreachable while the go:embed directive in main.go names this
			// file — a build that lost it does not compile. Answering 500
			// rather than 404 keeps a license that failed to ship from
			// reading like a mistyped URL.
			http.Error(w, "license text unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write(b)
	}
}

// RegisterLicenseRoutes publishes the license texts under the plugin's UI
// path. They sit beside the panel rather than inside www/ so that the files
// served are the repository's own LICENSE and NOTICE, with no second copy to
// drift.
func RegisterLicenseRoutes(uiPath string, mux *http.ServeMux) {
	mux.HandleFunc(uiPath+"/license", licenseHandler("LICENSE"))
	mux.HandleFunc(uiPath+"/notice", licenseHandler("NOTICE"))
}
