package cireconcile_test

import (
	"github.com/sjawhar/envoy/internal/dispatch/api"
	"github.com/sjawhar/envoy/internal/dispatch/cireconcile"
)

// The Reconciler a test of this package reads a WARN of names the repairs cmd/dispatch's names,
// api.MappingRepairs, whose route test (api/repo_project_repairs_test.go) sends each request it
// names through the settings route. Package api imports cireconcile, so only a file of the
// external test package can import api and hand the function over, which it does before any test
// runs.
func init() { cireconcile.UseMappingRepairs(api.MappingRepairs) }
