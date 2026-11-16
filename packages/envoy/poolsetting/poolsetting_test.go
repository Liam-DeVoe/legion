package poolsetting_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sjawhar/envoy/poolsetting"
)

// Named reads pool_max_conns as pgx reads the settings: in a URL's query, in a keyword/value
// string, and in the service file a `service=` entry names. Text inside a quoted value names
// nothing, and neither does a connection string pgx cannot parse.
func TestNamedReadsTheSettingsAsPgxDoes(t *testing.T) {
	service := filepath.Join(t.TempDir(), "pg_service.conf")
	if err := os.WriteFile(service, []byte("[poolsize]\npool_max_conns=7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PGSERVICEFILE", service)
	for _, tc := range []struct {
		name, connString string
		named            bool
	}{
		{"a postgres URL that names it", "postgres://u@h/db?sslmode=disable&pool_max_conns=7", true},
		{"a postgresql URL that names it", "postgresql://u@h/db?pool_max_conns=7", true},
		{"a URL that does not", "postgres://u@h/db?sslmode=disable", false},
		{"a URL that names another pool key", "postgres://u@h/db?pool_min_conns=2", false},
		{"a keyword/value string that names it", "host=h dbname=db pool_max_conns=7", true},
		{"a keyword/value string with spaces around the =", "host=h pool_max_conns = 7", true},
		{"a keyword/value string that does not", "host=h dbname=db", false},
		{"the key inside a quoted value", "host=localhost application_name='worker pool_max_conns=7'", false},
		{"a service whose file names it", "host=h service=poolsize", true},
		{"a string pgx cannot parse", "postgres://u@h:port/db?pool_max_conns=7", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := poolsetting.Named(tc.connString); got != tc.named {
				t.Errorf("Named(%q) = %t, want %t", tc.connString, got, tc.named)
			}
		})
	}
}
