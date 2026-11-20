package main

// STAND-IN for O-8a's E1 admission (feat/listener-edge-mode), written to the name and signature
// BuildO8a fixed: func EdgeAdmits(edgeProjects []string, caller brokertoken.Caller) error, nil when
// admitted, a refusal wrapping ErrEdgeNotAdmitted. The merge of feat/listener-edge-mode replaces
// this file with O-8a's own; nothing of it is pushed.

import (
	"errors"
	"fmt"
	"slices"

	"github.com/sjawhar/envoy/internal/brokertoken"
)

// ErrEdgeNotAdmitted is every refusal of EdgeAdmits.
var ErrEdgeNotAdmitted = errors.New("the edge admits only a token that lists its projects")

// EdgeAdmits is E1's rule for one caller.
func EdgeAdmits(edgeProjects []string, caller brokertoken.Caller) error {
	switch {
	case caller.ReadOnly:
		return fmt.Errorf("%w: a read-only token", ErrEdgeNotAdmitted)
	case caller.ProjectScope != brokertoken.ProjectScopeNamed && caller.ProjectScope != brokertoken.ProjectScopePerson:
		return fmt.Errorf("%w: the project scope %q", ErrEdgeNotAdmitted, caller.ProjectScope)
	case len(caller.Projects) == 0:
		return fmt.Errorf("%w: a token that lists no project", ErrEdgeNotAdmitted)
	}
	for _, project := range caller.Projects {
		if !slices.Contains(edgeProjects, project) {
			return fmt.Errorf("%w: the project %s is not one this edge serves", ErrEdgeNotAdmitted, project)
		}
	}
	return nil
}
