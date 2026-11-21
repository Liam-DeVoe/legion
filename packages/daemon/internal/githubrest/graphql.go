package githubrest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// GraphQLAPI is GitHub's GraphQL endpoint under endpoint, GitHub's API root; empty is
// https://api.github.com.
func GraphQLAPI(endpoint string) string {
	if endpoint == "" {
		endpoint = "https://api.github.com"
	}
	return strings.TrimRight(endpoint, "/") + "/graphql"
}

// GraphQL posts query, with variables, to GitHub's GraphQL endpoint under endpoint (GraphQLAPI)
// with an installation token, and reads the answer's data into data. An answer other than 2xx is an
// *Answer, as a REST call's is. GitHub answers a query it could not run with 200 and an errors list,
// and such an answer is an error naming each one: whatever data it carries beside them is partial,
// and a caller would read a missing list as an empty one.
func GraphQL(ctx context.Context, endpoint, token, query string, variables map[string]any, data any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return fmt.Errorf("encode a GraphQL query: %w", err)
	}
	var answer struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if _, _, err := (Client{Token: token}).call(ctx, http.MethodPost, GraphQLAPI(endpoint), body, &answer); err != nil {
		return err
	}
	if len(answer.Errors) > 0 {
		said := make([]string, 0, len(answer.Errors))
		for _, e := range answer.Errors {
			said = append(said, strings.TrimPrefix(e.Type+": "+e.Message, ": "))
		}
		return fmt.Errorf("GitHub answered a GraphQL query with errors: %s", strings.Join(said, "; "))
	}
	if err := json.Unmarshal(answer.Data, data); err != nil {
		return fmt.Errorf("decode the data GitHub answered a GraphQL query with: %w", err)
	}
	return nil
}
