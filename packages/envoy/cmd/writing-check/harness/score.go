package harness

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sjawhar/envoy/cmd/writing-check/harness/claude"
	"github.com/sjawhar/envoy/cmd/writing-check/harness/llm"
)

// Change is one of R3's changes: what it makes of a case's text.
type Change interface {
	// Name is the change's number, as --change names it.
	Name() string
	// Apply is the text a person sees after the change, with what it cost.
	Apply(ctx context.Context, c *Case) (Applied, error)
}

// Applied is what a change made of one case's text.
type Applied struct {
	// Text is the text a person sees after the change, and Same says it is the agent's text as
	// the agent wrote it.
	Text string
	Same bool
	// Definition is the definition Define gave, for change 6.
	Definition string
	// WriterAnswer is the writer's answer to the side question the change asked, if it asked
	// one; the added-facts judgement counts it as an input.
	WriterAnswer string
	// WaitSeconds is the time from the write, or the press, to the text a person sees.
	WaitSeconds float64
	// Calls are the change's own model calls, and Trace what each of its steps did.
	Calls []Call
	Trace []TraceStep
}

// asSeen is change 1: the text as the person saw it. It calls no model and makes no person wait.
type asSeen struct{}

func (asSeen) Name() string { return "1" }

func (asSeen) Apply(_ context.Context, c *Case) (Applied, error) {
	return Applied{Text: c.Agent.Text, Same: true}, nil
}

// changes are the changes the harness scores with no model of their own, by --change's value; the
// others are changeRun's (changes.go).
var changes = map[string]Change{"1": asSeen{}}

// CaseResult is one case's line of a run's cases.jsonl: what the change made of its text, the
// fresh readers' answers and every judgement.
type CaseResult struct {
	CaseID        string `json:"case_id"`
	Group         string `json:"group"`
	Kind          string `json:"kind"`
	Part          string `json:"part"`
	SplitGroup    string `json:"split_group"`
	Conversation  bool   `json:"conversation"`
	SessionStatus string `json:"session_status"`
	// Session says the case has a session part, which the judgements that read one read.
	Session       bool   `json:"session"`
	SessionSHA256 string `json:"session_sha256,omitempty"`
	Change        string `json:"change"`
	// Text is the text the person sees after the change, the string TextSHA256 hashes: an ask's
	// question with its options, one line each, as the dataset writes them; any other kind's body.
	Text       string `json:"text"`
	TextSHA256 string `json:"text_sha256"`
	Words      int    `json:"words"`
	// Definition is Define's definition (change 6).
	Definition  string      `json:"definition,omitempty"`
	WaitSeconds float64     `json:"wait_seconds"`
	ChangeCalls []Call      `json:"change_calls,omitempty"`
	ChangeTrace []TraceStep `json:"change_trace,omitempty"`
	// Reader is the fresh reader's answer, and FirstParagraphReader the answer of the one who
	// read only the first paragraph of an answer case's text.
	Reader               *Answer              `json:"reader,omitempty"`
	FirstParagraphReader *Answer              `json:"first_paragraph_reader,omitempty"`
	Grades               map[Judgement]*Grade `json:"grades"`
	Error                string               `json:"error,omitempty"`
	Time                 string               `json:"time"`
}

// calls is every model call the result records as this run's: the change's, the readers' and the
// grader's. An answer or a grade the run took from another run is that run's call, not this one's.
func (r *CaseResult) calls() (change, measure []Call) {
	change = r.ChangeCalls
	for _, a := range []*Answer{r.Reader, r.FirstParagraphReader} {
		if a != nil && a.Call.Model != "" && !a.Reused {
			measure = append(measure, a.Call)
		}
	}
	for _, j := range Judgements {
		g := r.Grades[j]
		if g == nil || g.Reused {
			continue
		}
		measure = append(measure, g.Refused...)
		if g.Call != nil && g.Call.Model != "" {
			measure = append(measure, *g.Call)
		}
	}
	return change, measure
}

// scorer scores the cases of one run.
type scorer struct {
	ds       *Dataset
	change   Change
	reader   *Reader
	grader   *Grader
	sessions map[string]claude.SessionText
	places   map[string]claude.Place
	// readersFrom are the results of the run --readers-from names, whose readers' answers
	// this run grades again.
	readersFrom map[string]CaseResult
	// gradesFrom are the results of each run --grades-from names, in the flag's order, whose
	// answers and grades this run takes for a case's same text and inputs.
	gradesFrom []map[string]CaseResult
	// vocabulary is the owner's own words (--vocabulary), nil for none.
	vocabulary *Vocabulary
	// only are the judgements the run gives; empty, it gives every one.
	only []Judgement
}

// wants is whether the run gives judgement j.
func (s *scorer) wants(j Judgement) bool { return len(s.only) == 0 || slices.Contains(s.only, j) }

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// scoreCase applies the change to one case and gives every judgement the case's group takes.
func (s *scorer) scoreCase(ctx context.Context, c *Case) CaseResult {
	session := s.sessions[c.ID]
	res := CaseResult{
		CaseID: c.ID, Group: c.Group, Kind: c.Kind, Part: c.Part, SplitGroup: c.SplitGroup,
		Conversation: c.Conversation, SessionStatus: s.places[c.ID].Status, Session: !session.Empty(),
		Change: s.change.Name(), Grades: map[Judgement]*Grade{},
	}
	if !session.Empty() {
		d := session.Digest()
		res.SessionSHA256 = hex.EncodeToString(d[:])
	}
	if res.SessionStatus == "" {
		res.SessionStatus = "no_place"
	}
	applied, err := s.change.Apply(ctx, c)
	res.ChangeCalls, res.WaitSeconds, res.ChangeTrace = applied.Calls, applied.WaitSeconds, applied.Trace
	if err != nil {
		res.Error = "change " + s.change.Name() + ": " + err.Error()
		res.Time = time.Now().UTC().Format(time.RFC3339)
		return res
	}
	text := applied.Text
	res.Text, res.TextSHA256, res.Words, res.Definition = text, sha256Hex([]byte(text)), Words(text), applied.Definition
	record := s.ds.RecordPart(c)
	question := c.PersonQuestion()
	reply := c.Group == GroupAnswer

	var mu sync.Mutex
	var wg sync.WaitGroup
	set := func(j Judgement, g Grade) {
		mu.Lock()
		res.Grades[j] = &g
		mu.Unlock()
	}
	run := func(f func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f()
		}()
	}
	// Every judgement gets the case's whole context; the grader's input modes decide what each
	// call reads of it.
	base := GradeInput{Reply: reply, Question: question, Original: c.Agent.Text, Text: text, Definition: applied.Definition,
		WriterAnswer: applied.WriterAnswer, Record: record, Session: session}
	if s.vocabulary != nil && s.wants(JUndefined) {
		// A case with no time gets no words: none can be placed before it.
		if at, ok := c.writtenAt(); ok {
			base.OwnWords = s.vocabulary.Known(text, at)
		}
	}
	// earlier are what the runs --grades-from names hold for this case's same text: the readers'
	// answers and each judgement's grades. The reader and the grader take one of them where its
	// inputs were theirs, and call no model.
	var earlierReaders, earlierFirstReaders []*Answer
	var earlier map[Judgement][]*Grade
	for _, results := range s.gradesFrom {
		r, ok := results[c.ID]
		if !ok || r.TextSHA256 != res.TextSHA256 {
			continue
		}
		earlierReaders, earlierFirstReaders = append(earlierReaders, r.Reader), append(earlierFirstReaders, r.FirstParagraphReader)
		if earlier == nil {
			earlier = map[Judgement][]*Grade{}
		}
		for j, g := range r.Grades {
			earlier[j] = append(earlier[j], g)
		}
	}
	prior := s.readersFrom[c.ID]
	if prior.TextSHA256 != res.TextSHA256 {
		prior = CaseResult{}
	}
	// readings are the fresh readers' reads of the case: of its text (answered), and of the
	// first paragraph of an answer case's text (first_paragraph). taken is the answer the case
	// takes in place of the reader's call, nil where the reader answers in this run: the answer
	// a run named by --readers-from gave to this case's same text, so two runs that compare
	// graders grade the same answers; else one of the answers of the runs --grades-from names
	// that read the same inputs.
	type reading struct {
		j     Judgement
		to    **Answer
		call  readerCall
		taken *Answer
	}
	var readings []reading
	read := func(j Judgement, to **Answer, done *Answer, same []*Answer, text string, firstParagraph bool) {
		r := reading{j: j, to: to}
		if done != nil && done.Error == "" {
			a := *done
			a.Reused = true
			r.taken = &a
		} else {
			r.call = s.reader.call(text, question, reply || firstParagraph, firstParagraph)
			r.taken = r.call.reused(same)
		}
		readings = append(readings, r)
	}
	if question != "" && s.wants(JAnswered) {
		read(JAnswered, &res.Reader, prior.Reader, earlierReaders, text, false)
	}
	if reply && question != "" && s.wants(JFirstParagraph) {
		first := FirstParagraph(text)
		if first == "" {
			first = text
		}
		read(JFirstParagraph, &res.FirstParagraphReader, prior.FirstParagraphReader, earlierFirstReaders, first, true)
	}
	// planned are the judgements the case gets, the readings' first, each with the reader's
	// answer where the case takes it. The grader plans their calls before the first is sent.
	var planned []GradeInput
	give := func(j Judgement, answer string) {
		in := base
		in.Judgement, in.Answer = j, answer
		planned = append(planned, in)
	}
	for _, r := range readings {
		answer := ""
		if r.taken != nil {
			answer = r.taken.Text
		}
		give(r.j, answer)
	}
	if reply && question != "" && s.wants(JAnswerStart) {
		give(JAnswerStart, "")
	}
	if c.Group == GroupPlain && s.wants(JHarder) {
		give(JHarder, "")
	}
	for _, j := range []Judgement{JPhrases, JUndefined} {
		if s.wants(j) {
			give(j, "")
		}
	}
	for _, j := range []Judgement{JLost, JAdded} {
		switch {
		case !s.wants(j):
		case applied.Same:
			set(j, Grade{ByConstruction: true})
		default:
			give(j, "")
		}
	}
	if applied.Definition != "" && s.wants(JDefine) {
		give(JDefine, "")
	}
	if len(planned) > 0 {
		s.grader.Plan(planned, earlier)
	}
	grade := func(in GradeInput) { set(in.Judgement, s.grader.GradeOrReuse(ctx, in, earlier[in.Judgement])) }
	for i, r := range readings {
		run(func() {
			a := r.taken
			if a == nil {
				answer := r.call.answer(ctx)
				a = &answer
			}
			mu.Lock()
			*r.to = a
			mu.Unlock()
			if a.Error == "" {
				in := planned[i]
				in.Answer = a.Text
				grade(in)
			}
		})
	}
	for _, in := range planned[len(readings):] {
		run(func() { grade(in) })
	}
	wg.Wait()

	var errs []string
	for _, a := range []*Answer{res.Reader, res.FirstParagraphReader} {
		if a != nil && a.Error != "" {
			errs = append(errs, a.Error)
		}
	}
	for _, j := range Judgements {
		if g := res.Grades[j]; g != nil && g.Error != "" {
			errs = append(errs, string(j)+": "+g.Error)
		}
	}
	res.Error = strings.Join(errs, "; ")
	res.Time = time.Now().UTC().Format(time.RFC3339)
	return res
}

// RunParams are what a run's figures depend on, and the pace of its own model calls. A run that
// continues in an output directory must have the parameters the directory's run.json records.
type RunParams struct {
	Change string `json:"change"`
	// Changes are the changes a combination holds, and Model the candidate the change calls; each
	// is absent for change 1.
	Changes []string `json:"changes,omitempty"`
	Model   string   `json:"model,omitempty"`
	Part    string   `json:"part"`
	Groups  []string `json:"groups"`
	// Judgements are the judgements the run gives (--judgements), absent when it gives all.
	Judgements []string `json:"judgements,omitempty"`
	// Sample is the stratified sample the run scores, absent for the whole part.
	Sample *Sample           `json:"sample,omitempty"`
	Models map[string]string `json:"models"`
	// GraderInputs are the grader's input modes (--grader-inputs, every judgement named), and
	// ReadersFrom the run whose readers' answers this run grades again.
	GraderInputs string `json:"grader_inputs"`
	ReadersFrom  string `json:"readers_from,omitempty"`
	// GradesFrom are the runs whose answers and grades this run takes for the same text and
	// inputs, and VocabularySHA256 the SHA-256 of the owner's texts the undefined judgement
	// reads his own words from.
	GradesFrom       []string `json:"grades_from,omitempty"`
	VocabularySHA256 string   `json:"vocabulary_sha256,omitempty"`
	PromptsSHA256    string   `json:"prompts_sha256"`
	// WriterSHA256 is the writer's prompts' digest, for a change that plays the writer.
	WriterSHA256 string `json:"writer_sha256,omitempty"`
	CasesSHA256  string `json:"cases_sha256"`
	SessionsSHA  string `json:"sessions_counts_sha256"`
	SessionChars int    `json:"session_chars"`
	// Rounds and Unedited are change 3's bound on its reviews and what posts with no writer
	// (--rounds, --unedited); Local the local Dispatch's settings the change reads.
	Rounds   int               `json:"rounds,omitempty"`
	Unedited string            `json:"unedited,omitempty"`
	Local    map[string]string `json:"local,omitempty"`
	// UncachedInputPerMinute is the pace of the run's own model calls in uncached input tokens a
	// minute (--uncached-input-per-minute), 0 for no pacing. So every part of a run that
	// continues in a directory was paced the same.
	UncachedInputPerMinute int `json:"uncached_input_per_minute"`
}

// readResults reads the last line of each case in a run's cases.jsonl; a line a crash cut short
// is skipped.
func readResults(path string) (map[string]CaseResult, error) {
	out := map[string]CaseResult{}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			var res CaseResult
			if json.Unmarshal(line, &res) == nil && res.CaseID != "" {
				out[res.CaseID] = res
			}
		}
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// selectCases is the cases of the groups asked, in id order; with limit above 0, the first limit
// of them in the order of a SHA-256 of the seed and the case id, so a small run holds every group.
func selectCases(all []*Case, groups []string, limit int, seed uint64) []*Case {
	var out []*Case
	for _, c := range all {
		if slices.Contains(groups, c.Group) {
			out = append(out, c)
		}
	}
	if limit > 0 && limit < len(out) {
		key := func(c *Case) string { return sha256Hex([]byte(strconv.FormatUint(seed, 10) + "/" + c.ID)) }
		slices.SortFunc(out, func(a, b *Case) int { return strings.Compare(key(a), key(b)) })
		out = out[:limit]
		slices.SortFunc(out, func(a, b *Case) int { return strings.Compare(a.ID, b.ID) })
	}
	return out
}

// fileSHA256 is the SHA-256 of the file at path.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// writeJSON writes v to path as indented JSON, by a file beside it renamed over it.
func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// checkParams writes params to run.json in out, or refuses a run.json of other parameters.
func checkParams(out string, params RunParams) error {
	path := filepath.Join(out, "run.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return writeJSON(path, params)
	}
	if err != nil {
		return err
	}
	var prior RunParams
	if err := json.Unmarshal(data, &prior); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	a, _ := json.Marshal(prior)
	b, _ := json.Marshal(params)
	if string(a) != string(b) {
		return fmt.Errorf("%s holds a run of other parameters (%s); this run's are %s: give a new --out", path, a, b)
	}
	return nil
}

func runScore(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("writing-check harness score", flag.ContinueOnError)
	fs.SetOutput(stderr)
	changeFlag := fs.String("change", "1", "the change to score: 1 to 6, 7a, or a combination of combinations.yaml")
	modelFlag := fs.String("model", "", "the candidate of candidates.yaml the change calls (changes 3 to 7a, and a combination that holds one)")
	localPath := fs.String("local", "", "the local.json of `harness local`: the local Dispatch the change calls (changes 2 to 6)")
	loadedPath := fs.String("loaded", "", "the loaded.jsonl of `harness load` into that local Dispatch")
	rounds := fs.Int("rounds", 2, "change 3: the reviews of one text, the first and those of the writer's edits; the last edit posts")
	unedited := fs.String("unedited", uneditedRewrite, "change 3 with no writer session: what posts, as DISPATCH_REVIEWER_UNEDITED says: rewrite or as_written")
	casesDir := fs.String("cases", "", "the dataset build directory: cases.jsonl.gz and documents.jsonl.gz (required)")
	sessionsDir := fs.String("sessions", "", "the directory `writing-check dataset sessions` wrote: session-rows.jsonl.gz, places.jsonl, sessions-counts.json (required)")
	out := fs.String("out", "", "the run's output directory; a run continues the cases it holds no result for (required; keep it under $TMPDIR)")
	part := fs.String("part", PartDevelopment, "the part to score: development, or held_out, which the exposure log records")
	groupsFlag := fs.String("groups", strings.Join(Groups, ","), "the case groups to score, comma-separated")
	sampleSize := fs.Int("sample", 0, "score a stratified sample of about this many cases, by case group and conversation flag, each split group whole (0: the whole part)")
	sampleSeed := fs.Uint64("sample-seed", DefaultSeed, "the seed of --sample's draw")
	seed := fs.Uint64("seed", DefaultSeed, "the seed of the bootstrap")
	draws := fs.Int("draws", DefaultDraws, "the bootstrap's resamples")
	sessionChars := fs.Int("session-chars", 30000, "the bytes of a case's session, its newest, that a grading call reads")
	exposureLog := fs.String("exposure-log", DefaultExposureLog(), "the exposure log a held-out run appends to")
	commit := fs.String("commit", "", "the commit of a held-out run, for the exposure log (default: git rev-parse HEAD)")
	newRun := fs.String("new-run", "", "the reason for a second held-out run at one commit")
	figuresOnly := fs.Bool("figures-only", false, "compute the figures from the results the output directory holds, calling no model")
	inputsFlag := fs.String("grader-inputs", "final", "the grader's input modes: final, full or trimmed, then any ,<judgement>=full|trimmed")
	graderFlag := fs.String("grader", "", "the grading model of this run in place of candidates.yaml's roles.grader, with the same fallbacks: a Claude model with a row in prices.yaml that is no candidate and plays no other role (default: roles.grader)")
	readersFrom := fs.String("readers-from", "", "a run directory whose readers' answers this run grades again, for the cases whose text is the same")
	gradesFrom := fs.String("grades-from", "", "run directories, comma-separated, whose answers and grades this run takes, calling no model, for each case whose text and whose reader's and grader's inputs are the same")
	vocabulary := fs.String("vocabulary", "", "the owner-texts.jsonl.gz that writing-check dataset owner-texts wrote: the undefined judgement reads the words of each text that the owner wrote himself before the case")
	repeatability := fs.String("repeatability", "", "a repeatability.json (harness compare) whose agreement scores.md gives beside each score")
	judgementsFlag := fs.String("judgements", "", "the judgements to give, comma-separated (default: every one), for a measurement of a few")
	mf := addModelFlags(fs)
	mf.addPaceFlag(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	usage := func(msg string) int {
		fmt.Fprintf(stderr, "writing-check harness score: %s\n", msg)
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "writing-check harness score: %v\n", err)
		return 1
	}
	change, plan, err := resolveChange(*changeFlag)
	if err != nil {
		return usage(err.Error())
	}
	groups := strings.Split(*groupsFlag, ",")
	groupsSet := false
	fs.Visit(func(f *flag.Flag) { groupsSet = groupsSet || f.Name == "groups" })
	if plan != nil && plan.has(changeDefine) && !groupsSet {
		groups = []string{GroupQuestion}
	}
	switch {
	case *casesDir == "" || *sessionsDir == "" || *out == "" || fs.NArg() > 0:
		return usage("--cases, --sessions and --out are required, and nothing else")
	case *part != PartDevelopment && *part != PartHeldOut:
		return usage("--part is development or held_out")
	case slices.ContainsFunc(groups, func(g string) bool { return !slices.Contains(Groups, g) }):
		return usage("--groups names a group that is not one of " + strings.Join(Groups, ", "))
	case plan != nil && plan.has(changeDefine) && !slices.Equal(groups, []string{GroupQuestion}):
		return usage("Define (change 6) defines the words of the person's question: it scores the question cases alone (--groups question)")
	case *judgementsFlag != "" && slices.ContainsFunc(strings.Split(*judgementsFlag, ","), func(j string) bool { return !slices.Contains(Judgements, Judgement(j)) }):
		return usage("--judgements names a judgement that is not one of the judgements of R2")
	case *draws < 1 || *sessionChars < 1 || *sampleSize < 0:
		return usage("--draws and --session-chars must be above 0, and --sample not below 0")
	case *part != PartHeldOut && (*newRun != "" || *commit != ""):
		return usage("--commit and --new-run are for a held-out run")
	case plan == nil && (*modelFlag != "" || *localPath != "" || *loadedPath != ""):
		return usage("change 1 calls no model and reads no local Dispatch: give no --model, --local or --loaded")
	case plan != nil && plan.candidate() != (*modelFlag != ""):
		return usage(fmt.Sprintf("change %s: --model names the candidate of the reviewer, Rewrite, Define and the detector's reader, which the change calls exactly when it holds 3, 4, 5, 6 or 7a", change.Name()))
	case plan != nil && plan.local() && (*localPath == "" || *loadedPath == ""):
		return usage(fmt.Sprintf("change %s calls the local Dispatch: give --local and --loaded", change.Name()))
	case *rounds < 1 || (*unedited != uneditedRewrite && *unedited != uneditedAsWritten):
		return usage("--rounds must be at least 1, and --unedited rewrite or as_written")
	case mf.uncachedPerMinute < 0:
		return usage("--uncached-input-per-minute must not be below 0")
	}
	if err := outsideRepository("--out", *out); err != nil {
		return usage(err.Error())
	}
	inputs, err := ParseGraderInputs(*inputsFlag)
	if err != nil {
		return usage(err.Error())
	}
	var priorReaders map[string]CaseResult
	if *readersFrom != "" {
		if priorReaders, err = readResults(filepath.Join(*readersFrom, "cases.jsonl")); err != nil || len(priorReaders) == 0 {
			return usage(fmt.Sprintf("--readers-from %s holds no results (%v)", *readersFrom, err))
		}
	}
	var priorGrades []map[string]CaseResult
	var gradesDirs []string
	for _, dir := range strings.Split(*gradesFrom, ",") {
		if dir = strings.TrimSpace(dir); dir == "" {
			continue
		}
		results, err := readResults(filepath.Join(dir, "cases.jsonl"))
		if err != nil || len(results) == 0 {
			return usage(fmt.Sprintf("--grades-from %s holds no results (%v)", dir, err))
		}
		priorGrades, gradesDirs = append(priorGrades, results), append(gradesDirs, dir)
	}
	var ownWords *Vocabulary
	var vocabularySHA string
	if *vocabulary != "" {
		if ownWords, err = ReadVocabulary(*vocabulary); err != nil {
			return usage(fmt.Sprintf("--vocabulary: %v", err))
		}
		if vocabularySHA, err = fileSHA256(*vocabulary); err != nil {
			return fail(err)
		}
	}
	var agreement *Repeatability
	if *repeatability != "" {
		if agreement, err = readRepeatability(*repeatability); err != nil {
			return usage(err.Error())
		}
	}
	cfg, err := LoadConfig()
	if err != nil {
		return fail(err)
	}
	if cfg, err = cfg.WithGrader(*graderFlag, false); err != nil {
		return usage(err.Error())
	}
	started := time.Now()
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return fail(err)
	}
	casesSHA, err := fileSHA256(filepath.Join(*casesDir, casesFile))
	if err != nil {
		return fail(err)
	}
	sessionsSHA, err := fileSHA256(filepath.Join(*sessionsDir, "sessions-counts.json"))
	if err != nil {
		return fail(err)
	}
	var fallbacks []string
	for _, f := range cfg.Models.Roles.GraderFallbacks {
		fallbacks = append(fallbacks, f.ID)
	}
	models := map[string]string{RoleGrader: cfg.Models.Roles.Grader.ID, RoleGraderFallback: strings.Join(fallbacks, ","), RoleReader: cfg.Models.Roles.Reader.ID}
	var run *changeRun
	var inst LocalInstance
	if plan != nil {
		run = change.(*changeRun)
		run.cfg, run.rounds, run.unedited, run.tries, run.backoff, run.poll = cfg, *rounds, *unedited, changeTries, changeBackoff, changePoll
		if plan.writer() {
			models[RoleWriter] = cfg.Models.Roles.Writer.ID
		}
		if *modelFlag != "" {
			if !slices.ContainsFunc(cfg.Models.Candidates, func(m ModelEntry) bool { return m.ID == *modelFlag }) {
				return usage(fmt.Sprintf("--model %q is no candidate of candidates.yaml", *modelFlag))
			}
			run.candidate, models[RoleCandidate] = *modelFlag, *modelFlag
		}
		if plan.local() {
			if inst, err = readLocalInstance(*localPath); err != nil {
				return usage(err.Error())
			}
			if run.loaded, err = readLoaded(*loadedPath); err != nil {
				return usage(err.Error())
			}
			if run.rules, err = instanceRules(inst, *plan); err != nil {
				return usage(err.Error())
			}
		}
	}
	// A held-out run is in the exposure log before it reads a case of the part, so a run that
	// stops after this line, for any reason, still counts as one that read it.
	if *part == PartHeldOut && !*figuresOnly {
		c := *commit
		if c == "" {
			if c, err = headCommit(); err != nil {
				return fail(fmt.Errorf("the commit of a held-out run: %w; give --commit", err))
			}
		}
		changed := []string{change.Name()}
		if plan != nil {
			changed = plan.changes()
		}
		e := Exposure{Commit: c, Time: started.UTC().Format(time.RFC3339), Changes: changed, Models: models, Reason: *newRun, Out: *out}
		if err := ClaimHeldOut(*exposureLog, e); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stderr, "writing-check harness score: the exposure log %s records this held-out run at %s\n", *exposureLog, c)
	}
	ds, err := LoadDataset(*casesDir, *part, docLimit)
	if err != nil {
		return fail(err)
	}
	selected := selectCases(ds.Cases, groups, 0, 0)
	params := RunParams{
		Change: change.Name(), Part: *part, Groups: groups, Models: models, GraderInputs: inputs.String(), ReadersFrom: *readersFrom,
		GradesFrom: gradesDirs, VocabularySHA256: vocabularySHA,
		PromptsSHA256: PromptsDigest(), CasesSHA256: casesSHA, SessionsSHA: sessionsSHA, SessionChars: *sessionChars,
		UncachedInputPerMinute: mf.uncachedPerMinute,
	}
	if plan != nil {
		params.Model = run.candidate
		if len(plan.changes()) > 1 || plan.name != plan.steps[0] {
			params.Changes = plan.changes()
		}
		if plan.writer() {
			params.WriterSHA256 = WriterDigest()
		}
		if plan.has(changeFindings) {
			params.Rounds, params.Unedited = *rounds, *unedited
		}
		if plan.local() {
			params.Local = inst.readSettings()
		}
	}
	var only []Judgement
	if *judgementsFlag != "" {
		for _, j := range Judgements {
			if slices.Contains(strings.Split(*judgementsFlag, ","), string(j)) {
				only = append(only, j)
				params.Judgements = append(params.Judgements, string(j))
			}
		}
	}
	if *sampleSize > 0 {
		var sample Sample
		selected, sample = drawSample(selected, *sampleSize, *sampleSeed)
		params.Sample = &sample
	}
	if err := checkParams(*out, params); err != nil {
		return fail(err)
	}
	ids := make([]string, len(selected))
	for i, c := range selected {
		ids[i] = c.ID
	}
	places, err := claude.ReadPlaces(*sessionsDir)
	if err != nil {
		return fail(err)
	}
	resultsPath := filepath.Join(*out, "cases.jsonl")
	done, err := readResults(resultsPath)
	if err != nil {
		return fail(err)
	}
	var todo []*Case
	for _, c := range selected {
		if r, ok := done[c.ID]; !ok || r.Error != "" {
			todo = append(todo, c)
		}
	}
	fmt.Fprintf(stderr, "writing-check harness score: change %s, part %s: %d cases, %d with a result, %d to score\n",
		change.Name(), *part, len(selected), len(selected)-len(todo), len(todo))
	scored, failed := 0, 0
	var uncached llm.PaceUse
	if !*figuresOnly && len(todo) > 0 {
		sessions, err := claude.CachedExcerpts(*sessionsDir, ids, *sessionChars, filepath.Join(*out, "session-excerpts.jsonl.gz"))
		if err != nil {
			return fail(fmt.Errorf("the session parts: %w", err))
		}
		runner, err := mf.runner()
		if err != nil {
			return usage(err.Error())
		}
		grader, err := mf.grader(cfg, inputs, runner)
		if err != nil {
			return usage(err.Error())
		}
		readerModel, err := mf.claudeClient(cfg, cfg.Models.Roles.Reader, runner)
		if err != nil {
			return usage(err.Error())
		}
		if run != nil {
			if err := run.connect(ctx, mf, inst, runner, sessions, ds, selected); err != nil {
				return fail(err)
			}
		}
		s := &scorer{ds: ds, change: change, reader: &Reader{Model: readerModel}, grader: grader,
			sessions: sessions, places: places, readersFrom: priorReaders, gradesFrom: priorGrades, vocabulary: ownWords, only: only}
		scored, failed, err = scoreAll(ctx, s, todo, resultsPath, mf.concurrency, stderr, started)
		if err != nil {
			return fail(err)
		}
		uncached = runner.Pacer.Use()
		if done, err = readResults(resultsPath); err != nil {
			return fail(err)
		}
	}
	var results []CaseResult
	for _, c := range selected {
		if r, ok := done[c.ID]; ok && r.Error == "" {
			results = append(results, r)
		}
	}
	figures := computeFigures(params, results, len(selected), *seed, *draws, cfg)
	figures.Run.WallSeconds = time.Since(started).Seconds()
	figures.Run.ScoredThisRun, figures.Run.FailedThisRun = scored, failed
	figures.Run.UncachedInput = uncached
	figures.Repeatability = agreement
	if err := writeJSON(filepath.Join(*out, "scores.json"), figures); err != nil {
		return fail(err)
	}
	md := figures.markdown()
	if err := os.WriteFile(filepath.Join(*out, "scores.md"), []byte(md), 0o600); err != nil {
		return fail(err)
	}
	fmt.Fprint(stdout, md)
	if ctx.Err() != nil {
		return fail(fmt.Errorf("stopped: %w; run it again to continue", ctx.Err()))
	}
	if missing := len(selected) - len(results); missing > 0 {
		return fail(fmt.Errorf("%d of %d cases have no result; run it again to score them", missing, len(selected)))
	}
	return 0
}

// scoreAll scores cases, workers at once, appending each result to path as it comes. It answers
// how many it scored and how many of those ended with an error.
func scoreAll(ctx context.Context, s *scorer, cases []*Case, path string, workers int, progress io.Writer, started time.Time) (scored, failed int, err error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	var mu sync.Mutex
	var writeErr error
	feed := make(chan *Case)
	var wg sync.WaitGroup
	for range max(workers, 1) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range feed {
				res := s.scoreCase(ctx, c)
				if ctx.Err() != nil {
					return
				}
				line, err := json.Marshal(res)
				mu.Lock()
				if err == nil && writeErr == nil {
					_, writeErr = f.Write(append(line, '\n'))
				} else if writeErr == nil {
					writeErr = err
				}
				scored++
				if res.Error != "" {
					failed++
					fmt.Fprintf(progress, "writing-check harness score: case %s: %s\n", res.CaseID, res.Error)
				}
				if scored%50 == 0 || scored == len(cases) {
					fmt.Fprintf(progress, "writing-check harness score: %d of %d cases (%d failed), %s\n", scored, len(cases), failed, time.Since(started).Round(time.Second))
				}
				mu.Unlock()
			}
		}()
	}
feeding:
	for _, c := range cases {
		select {
		case feed <- c:
		case <-ctx.Done():
			break feeding
		}
	}
	close(feed)
	wg.Wait()
	return scored, failed, writeErr
}
