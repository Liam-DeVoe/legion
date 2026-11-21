// Package tlssites names the public TLS sites the image probe's TLS check reaches
// (cmd/legion's tls_probe.go) and the root each compromised site's chain ends at. A kind test
// serves stand-ins for the same names (internal/kindtest), so the one list lives here.
package tlssites

// Site is one site the probe reaches: its name in the probe's output, its URL, and, for a site
// whose chain the image's clients must refuse, the path of the root its chain ends at, which the
// worker image carries (packages/daemon/docker/tls-roots).
type Site struct {
	Name, URL, Root string
}

var (
	// EmSign and AAA serve chains that end at the emSign ECC Root CA G3 and at AAA Certificate
	// Services, which every client of the image must refuse.
	EmSign = Site{Name: "emSign", URL: "https://dv-g3b-emudhra.we-have-a-problem.com/", Root: "/usr/share/legion/tls-roots/emsign-ecc-root-ca-g3.pem"}
	AAA    = Site{Name: "AAA", URL: "https://dv-g3b-aaa-emudhra.we-have-a-problem.com/", Root: "/usr/share/legion/tls-roots/aaa-certificate-services.pem"}
	// Control is the site every client must reach, and Isolation the endpoint each pinned root
	// must not verify.
	Control   = Site{Name: "control", URL: "https://example.com/"}
	Isolation = Site{Name: "isolation", URL: "https://letsencrypt.org/"}
)
