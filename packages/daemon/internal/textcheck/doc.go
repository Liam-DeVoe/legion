// Package textcheck holds the tests whose subject is text rather than behaviour: Go source
// parsed for a lint rule, a README, a guide, a prompt, a pinned constant. None of them needs a
// database or a container, so they are kept apart from the product packages whose suites do.
// Each reaches its subject by a relative path from this directory, but one:
// phase_entry_migrations_doc_test.go reads the daemon's migrations by importing
// internal/store/migrations, whose closure is those migrations and the rule of their names, so
// the guide's list of migrations is held to the set the daemon embeds and no other daemon file
// joins this job's filter.
package textcheck
