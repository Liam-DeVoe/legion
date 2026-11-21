// Command sweepreader is the reader that the scenario sweeps of package specgraph judge, run in a
// process of its own: it holds only specgraph and the standard library, so no code of the sweeps'
// test binary, a test file's init, a writer, a cleanup or a goroutine among it, can reach its
// memory, the package's variables and those of the libraries that it calls among it. The test
// process builds it and starts it, though, so test code that runs before the build chooses the code
// that it is built from and the runtime settings that it runs with (specgraph's sweepReader).
//
// It reads one line per scenario from its standard input, a JSON list of the scenario's files in
// the order of their names, each {"name": <its path in the scenario>, "data": <its bytes, in
// base64>}, and writes one line for each to its standard output: {"states": {...}}, the state of
// each check, of each statement and its own state, and of each wait of each unit, at each subject
// of the scenario, by its place (sweepStates); or {"refused": true, "refusal": <the text>}, where
// the reader or the derivation refuses the scenario. A line that it cannot read (no JSON of that
// form, a file named twice, a line with no end) ends it with exit 2 and a line on its standard
// error; the end of its input ends it with exit 0.
package main

import (
	"bufio"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"testing/fstest"

	"github.com/sjawhar/envoy/internal/dispatch/specgraph"
)

// scenarioFile is one file of a scenario as a line of the input names it.
type scenarioFile struct {
	Name string `json:"name"`
	Data []byte `json:"data"`
}

// answer is the line that the reader writes for one scenario.
type answer struct {
	States  map[string]string `json:"states,omitempty"`
	Refused bool              `json:"refused,omitempty"`
	Refusal string            `json:"refusal,omitempty"`
}

func main() {
	if err := serve(bufio.NewReader(os.Stdin), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "sweepreader:", err)
		os.Exit(2)
	}
}

// serve answers each line of in on out, one line each, until the end of in.
func serve(in *bufio.Reader, out io.Writer) error {
	w := bufio.NewWriter(out)
	for n := 1; ; n++ {
		line, err := in.ReadBytes('\n')
		switch {
		case errors.Is(err, io.EOF) && len(line) == 0:
			return nil
		case errors.Is(err, io.EOF):
			return fmt.Errorf("line %d has no end", n)
		case err != nil:
			return err
		}
		fsys, err := scenarioOf(line)
		if err != nil {
			return fmt.Errorf("line %d: %w", n, err)
		}
		var a answer
		if a.States, err = sweepStates(fsys); err != nil {
			a.States, a.Refused, a.Refusal = nil, true, err.Error()
		}
		data, err := json.Marshal(a, json.Deterministic(true))
		if err != nil {
			return err
		}
		if _, err := w.Write(append(data, '\n')); err != nil {
			return err
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
}

// scenarioOf is the scenario that one line of the input names: each file by its name, with its
// bytes alone.
func scenarioOf(line []byte) (fstest.MapFS, error) {
	var files []scenarioFile
	if err := json.Unmarshal(line, &files, json.RejectUnknownMembers(true)); err != nil {
		return nil, err
	}
	fsys := make(fstest.MapFS, len(files))
	for _, f := range files {
		if _, twice := fsys[f.Name]; twice {
			return nil, fmt.Errorf("the file %q is named twice", f.Name)
		}
		fsys[f.Name] = &fstest.MapFile{Data: f.Data}
	}
	return fsys, nil
}

// sweepStates is the state of each check, of each statement and its own state, and of each wait of
// each unit, at each subject of the scenario fsys, by its place in the output: the subject by its
// place in subjects.json, not its value, since an input that leaves out a member of a subject, such
// as graph_tx, derives the states of that subject; or the refusal of the reader or of the derivation.
func sweepStates(fsys fs.FS) (map[string]string, error) {
	s, err := specgraph.ReadScenario(fsys)
	if err != nil {
		return nil, err
	}
	states := map[string]string{}
	for i := range s.Subjects {
		state, err := specgraph.Derive(s.Input(i))
		if err != nil {
			return nil, err
		}
		at := fmt.Sprintf("subject %d", i+1)
		for _, c := range state.Checks {
			states[at+" check "+c.ID] = c.State
		}
		for _, st := range state.Statements {
			states[at+" statement "+st.ID] = st.State
			states[at+" own state of "+st.ID] = st.Own
		}
		for _, u := range s.Units {
			for _, statement := range u.WaitsOn {
				states[at+" wait of "+u.ID+" on "+statement] = map[bool]string{true: "ends", false: "holds"}[state.WaitEnds(statement)]
			}
		}
	}
	return states, nil
}
