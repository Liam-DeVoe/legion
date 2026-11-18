package cireconcile

import "context"

// MappingRepairs is mappingRepairs, the fields the pass's WARN names beside a refused mapped name,
// for the route test (mapping_route_test.go). That test is of package cireconcile_test because it
// mounts the settings route, and package api imports this one.
func (r *Reconciler) MappingRepairs(ctx context.Context, row string) []any {
	return r.mappingRepairs(ctx, row)
}
