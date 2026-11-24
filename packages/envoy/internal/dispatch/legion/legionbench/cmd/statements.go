package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// The board's read of the stored facts as the server ran it, with the arguments it was bound with,
// taken from Postgres's statement log (log_min_duration_statement 0, in jsonlog), so the facts
// read's plan and time are measured on the statement a request of the board ran, with the board's
// keys and roots, as TestTheBoardsFactsReadUnderAGenericPlanStaysWithinItsBudget takes it from its
// traced pool. It is evals/evalbench's capture of #3976 (measure/capture.go), narrowed to one
// statement.

// factsRead is the board's read of the stored facts as the statement log holds it: its text and
// its parameters' literals, in order.
type factsRead struct {
	SQL    string   `json:"sql"`
	Params []string `json:"params"`
}

// factsReadStatement matches the board's read of the stored facts (legionfacts.Read): the three
// arms of issue_facts keyed on closed_at.
var factsReadStatement = regexp.MustCompile(`(?s)\bfrom issue_facts\b.*\bclosed_at is null\b.*\bunion all\b`)

// executed reads a duration line's statement: an extended-protocol execute or a simple query.
var executed = regexp.MustCompile(`(?s)^duration: [0-9.]+ ms  (?:execute [^:]*|statement): (.*)$`)

// setStatementLog turns Postgres's log of every statement, with its parameters, on or off. The
// role must be a superuser.
func setStatementLog(ctx context.Context, pool *store.Pool, on bool) error {
	change := `alter system reset log_min_duration_statement`
	if on {
		change = `alter system set log_min_duration_statement = 0`
	}
	if _, err := pool.Exec(ctx, change); err != nil {
		return fmt.Errorf("set the statement log (the role must be a superuser): %w", err)
	}
	if _, err := pool.Exec(ctx, `select pg_reload_conf()`); err != nil {
		return fmt.Errorf("reload Postgres's configuration: %w", err)
	}
	return nil
}

// captureFactsRead turns the statement log on, has read make the board's request, turns the log
// off, and answers the facts read the log holds from that request, waiting up to a minute for the
// logging collector to write it.
func captureFactsRead(ctx context.Context, pool *store.Pool, pgLog string, read func() error) (factsRead, error) {
	info, err := os.Stat(pgLog)
	if err != nil {
		return factsRead{}, fmt.Errorf("read the statement log's size: %w", err)
	}
	offset := info.Size()
	if err := setStatementLog(ctx, pool, true); err != nil {
		return factsRead{}, err
	}
	// The reload reaches each backend at its next command; a moment lets the server's pooled
	// connections take it before the request.
	time.Sleep(time.Second)
	readErr := read()
	if err := setStatementLog(ctx, pool, false); err != nil {
		return factsRead{}, err
	}
	if readErr != nil {
		return factsRead{}, readErr
	}
	deadline := time.Now().Add(time.Minute)
	for {
		found, err := readFactsRead(pgLog, offset)
		if err != nil || found != nil {
			if found == nil {
				return factsRead{}, err
			}
			return *found, err
		}
		if time.Now().After(deadline) {
			return factsRead{}, fmt.Errorf("the statement log %s holds no read of issue_facts by the board a minute after its request", pgLog)
		}
		time.Sleep(time.Second)
	}
}

// readFactsRead answers the last board read of the stored facts the jsonlog file at path holds
// after offset, nil where it holds none.
func readFactsRead(path string, offset int64) (*factsRead, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open the statement log: %w", err)
	}
	defer file.Close()
	if _, err := file.Seek(offset, 0); err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1<<20), 64<<20)
	var found *factsRead
	for scanner.Scan() {
		var line struct {
			Message string `json:"message"`
			Detail  string `json:"detail"`
		}
		if json.Unmarshal(scanner.Bytes(), &line) != nil {
			continue
		}
		match := executed.FindStringSubmatch(line.Message)
		if match == nil || !factsReadStatement.MatchString(match[1]) {
			continue
		}
		params, err := parseParameters(line.Detail)
		if err != nil {
			return nil, fmt.Errorf("read the parameters of the board's facts read: %w", err)
		}
		found = &factsRead{SQL: match[1], Params: params}
	}
	return found, scanner.Err()
}

// parseParameters reads a statement log's detail, `Parameters: $1 = '…', $2 = NULL`, into the
// parameters' literals in order, each as SQL writes it: quoted, or NULL.
func parseParameters(detail string) ([]string, error) {
	rest, found := strings.CutPrefix(detail, "Parameters: ")
	if !found {
		return nil, nil
	}
	var params []string
	for index := 1; rest != ""; index++ {
		head := "$" + strconv.Itoa(index) + " = "
		if !strings.HasPrefix(rest, head) {
			return nil, fmt.Errorf("parameter %d does not start %q", index, head)
		}
		rest = rest[len(head):]
		var literal string
		switch {
		case strings.HasPrefix(rest, "NULL"):
			literal, rest = "NULL", rest[len("NULL"):]
		case strings.HasPrefix(rest, "'"):
			end := 1
			for {
				next := strings.IndexByte(rest[end:], '\'')
				if next < 0 {
					return nil, fmt.Errorf("parameter %d has no closing quote", index)
				}
				end += next + 1
				if end < len(rest) && rest[end] == '\'' {
					end++
					continue
				}
				break
			}
			literal, rest = rest[:end], rest[end:]
		default:
			return nil, fmt.Errorf("parameter %d is neither quoted nor NULL", index)
		}
		params = append(params, literal)
		rest = strings.TrimPrefix(rest, ", ")
	}
	return params, nil
}
