#!/usr/bin/env bash
# Every download this repository's CI makes retries, and one rule holds that retry
# (dispatch://INFRA-1545, R2). This check is the enforcement half: nothing else refuses a fetch
# added with no retry, a retry below the floor, an attempt with no time bound, a second copy of
# the retry loop, or a fetch whose failure the pipeline around it swallows.
#
# THE FLOOR (R1): at least 5 attempts, the last starting at least 120 seconds after the first, and
# each attempt bounded in time, so an attempt against a host that accepts the connection and then
# sends nothing stops and the next one starts. Three mechanisms reach it, and a site invents no
# fourth:
#
#   1. `retry_fetch <what> <bound> -- <command...>`, the one helper, in
#      .github/scripts/ci-retry.sh: five attempts, sleeps of 10, 20, 40 and 80 seconds, each
#      attempt under `timeout <bound>`. A workflow `run:`, a composite action's `run:` and a
#      script source that file and call it.
#   2. curl's own options, `--retry 7 --retry-all-errors --max-time <bound>` with `--fail` —
#      measured on curl 8.14.1: eight attempts whose last starts 127 s after the first, with
#      `--max-time` reset for each one, so it bounds every attempt and not the whole transfer.
#      `--fail` is load-bearing and usually arrives as the `f` of `-fsSL`: without it curl exits 0
#      on a persistent 504 and writes the error body into the output file, which the checksum or
#      the unpack after it then reads. A curl that reads the status itself (`-w '%{http_code}'`
#      and a test of what it printed) answers for `--fail` and passes.
#
#      Two more options decide when that last attempt starts, and a curl carrying either is held
#      to the floor's 120 s by the ladder this check computes rather than by its `--retry` count
#      alone. The rule is curl's own, read in its documentation at curl 8.14.1
#      (docs/cmdline-opts/retry.md, retry-delay.md and retry-max-time.md of the curl-8_14_1 tag):
#      curl "first waits one second and then for all forthcoming retries it doubles the waiting
#      time until it reaches 10 minutes", `--retry-delay <n>` replaces that doubling with a fixed
#      wait of n seconds before every retry (and `--retry-delay 0` asks for the default back-off
#      again), and `--retry-max-time <n>` stops the ladder: a retry is made only while the timer
#      reset at the first attempt "has not reached this given limit", so the last attempt starts
#      no more than one wait past n (and `--retry-max-time 0` sets no limit at all). The ladder
#      this check computes is the sum of those waits with the attempts themselves taking no time,
#      which is the longest one the options allow; a curl whose longest ladder is under 120 s is
#      refused with the figure. `--retry 7 --retry-delay 1` is 7 s and `--retry 7
#      --retry-max-time 5` is 7 s, and both pass every other test of this floor. One site in
#      the tree writes one of the two: .github/scripts/ci-tools.sh's ci_tools_curl carries
#      `--retry-max-time 300`, which keeps its eighth attempt — the seventh wait is decided
#      with the timer at 63 s, inside that limit — so its ladder is the full 127 s against a
#      host that refuses at once, and five attempts over 256 s against one that stalls, each
#      clearing the floor; that file derives the 300 itself. No site writes `--retry-delay`.
#      Rule 8 reads this same ladder when it prices the curl's
#      attempts against the step's budget, so a curl's floor and its arithmetic are one
#      computation rather than two that can drift apart.
#   3. inside a Dockerfile, one canonical inline `retry()` definition, the same text in every
#      network `RUN` of the four files. A Dockerfile carries the loop inline because it can hold
#      no new file above a layer whose order its own comments set on purpose, and a COPY of a
#      helper there would throw that layer's cache away on every edit of the helper.
#
# WHAT IT READS: every file under .github/workflows/, every action.yml (or action.yaml) under
# .github/actions/, every *.sh under .github/scripts/ but the `.test.sh` harnesses, and the four
# Dockerfiles a CI job builds (DOCKERFILES below; a missing one fails this check, so a rename is
# seen, and a step building a Dockerfile it does not name is refused, so a fifth is seen too:
# rule 10). Files are discovered by walking the tree, not by asking git, so an unsnapshotted
# change cannot read green locally. Workflows and actions are parsed as YAML, never grepped, and
# only a step's `run:` is read for its fetches. This check reads its own file like any other
# script, which is why the comments here write no heredoc operator of their own: an operator no
# line of this file closes would blank every line after it for this file's own reading.
#
# Rule 8 reads one step across more than one of those files: a step that runs a script under
# .github/scripts/ carries that script's own retried calls, and those of every script it calls
# in turn. Three budgets of this tree were a one-line `run:` of a script
# call and nothing else — ci.yaml's `pr-title` and `ci-ok` steps and pr-title.yaml's, 1200 s,
# 600 s and 900 s of retries under a 20-, a 10- and a 15-minute budget — and a rule 8 that
# stopped at the `run:` saw the arithmetic of none of them.
#
# THE FORMS RULE 8 READS, AND NO OTHERS (dispatch://INFRA-1545, round 37). Each reading of rule
# 8 accepts the forms this tree writes, measured on the tree at d65e47ae with an instrumented copy
# of this check, and refuses every other form it finds by name where what it can hide fetches:
# the file, the line, the step or the script, the form, and a listed form to write instead. A
# form that is not on its list is not modelled, so it is charged nothing and refused by name
# where a reading finds it and what it can hide fetches. A form no reading finds is read as
# nothing: that is how round 37's probes found routes from text to a program that passed a
# 1650 s call under a one-minute key, five at 125acdb7 and 14 at aaab38b5, each now refused by
# its route (the reach under item 1), so this header claims no reading of a form it does not
# name. Until round 37 each reading also modelled shapes no step writes, and each round's newest
# model held the next undercount. The counts are the tree's.
#
#   1. A CALL OF A SCRIPT. SCRIPT_CALL finds each occurrence of a path naming a script under
#      .github/scripts/, outside a comment, in a workflow step's `run:`, a composite action
#      step's `run:` or a script this check reads, and call_of reads where in its command the
#      occurrence sits. A comment is the one bash reads: from a `#` that starts a word outside
#      quotes to the end of its physical line, which a backslash there does not continue. Three
#      forms are read:
#        - a RUN, 256: the path is the command word, spelled in any way ending in `/<name>.sh`
#          (`.github/scripts/x.sh`, `../../.github/scripts/x.sh`, `$script_dir/x.sh`,
#          `$GITHUB_WORKSPACE/.github/scripts/x.sh`, `$(dirname "${BASH_SOURCE[0]}")/x.sh`,
#          `$GITHUB_ACTION_PATH/../../scripts/x.sh`), with nothing before it but variable
#          assignments, at top level or as the whole command of a `$( … )`, outside a
#          function's body;
#        - a SOURCE, 128: `. <path>` or `source <path>` with nothing before it, at top level,
#          outside a function's body and outside any loop that runs it more than once;
#        - a NON-CALL, 16: an argument of `echo` or `grep` (NONCALL_COMMANDS) whose output no
#          shell runs, and a comment, charged nothing and refused nothing. Until round 37 a path
#          anywhere on a line that was not a comment line was a call, so two echo lines of
#          ci.yaml's `tool-fallback` step read as calls, 3843 s where its call is 1281 s. Such an
#          argument inside a `$( … )` or backticks is a non-call where what they print is an
#          assignment's value or an argument of a command that runs no text, as
#          ci-job-tool-fallback.yaml's `reader_lines=$(grep … ci-tools-proof.sh)` writes, and is
#          refused where it is the command word or an argument of `eval`, `.`, `source` or a
#          shell, which run it (dispatch://INFRA-1545, round 37). What such a command prints may
#          still run later, from a file it is written to, from a variable or through a pipe into
#          a shell; that run is refused where it happens, as a run whose script this check
#          cannot name (the reach below).
#      Every other place is refused where the script it names fetches, which is a script holding
#      a retried call or reaching one through its recognised calls: `bash <path>`, a path after
#      a reserved word, `timeout`, `env` or any other wrapper, inside a `( … )` subshell,
#      backticks, a process substitution or a `$( … )` inside another, in a variable assignment
#      or an array, an argument of `printf`, `eval`, a shell's `-c` text or any other command, a
#      pipe into a shell, a redirect, the words of a `for` or a `case`, a function's body, and a
#      source inside a loop. It is not charged, and the refusal stands where its figure would
#      be. One naming a script that fetches nothing is neither charged nor refused. A script is
#      named as lowercase letters, digits and `-` before `.sh` (SCRIPT_CALL), and a script under
#      .github/scripts/ named otherwise is refused by name where it fetches, since no call of it
#      is found (dispatch://INFRA-1545, round 37).
#      THE REACH OF THIS READING: rule 8 reads literal paths. It names a script by the path a run
#      or a source writes, and refuses a run whose target is built from data as the step runs
#      rather than guess what that data holds (unnamed_runs; dispatch://INFRA-1545, round 37).
#      The target is the operand of `bash`, `sh`, `dash`, `zsh`, `.` or `source`, or a command
#      word, and it is refused where it could be a script under .github/scripts/ this check
#      cannot name: its last path element is wholly an expansion (`"$p"`, `$(cmd)`), that
#      element holds an expansion and ends in `.sh` (`"$dir/$name.sh"`), its literal directory
#      is .github/scripts/ and that element holds an expansion, it names a script with no
#      directory (`bash x.sh`, `x.sh`), which bash finds through the working directory or PATH,
#      so after a `cd` into .github/scripts/ or with PATH reaching it, or it is a file the same
#      text writes (written_files): as the operand of a shell, `.` or `source`, any file it
#      writes, and as a command word, by any name, one holding text the step makes or a copy of
#      such a script (`cat > run.sh`, `echo <path> > run`, `… | tee run.sh`, `… | dd of=run.sh`,
#      then `bash run.sh` or `./run`), compared by its path and, where the text holds a `cd` or
#      a `pushd`, by its name alone. And a route from text to a program is refused whatever the
#      text: `eval`; a shell reading its program from its standard input, a here-string, a file
#      redirected into it or a process substitution, and a `.` or `source` of a process
#      substitution; output piped into a shell reading its standard input; and `find -exec`
#      and `xargs` where what they run on what they read could run such a script: a shell,
#      `eval`, `.` or `source`, a wrapper and nothing else (`xargs env`), or the placeholder
#      itself (`-exec {} \;`). Round 37's probe at aaab38b5 found 14 such routes, 13a to 13n,
#      each passing a 1650 s call under a one-minute key. Measured on the tree, the routes as
#      first written refused six lines, so each was narrowed to its fetching case: the tree's
#      three `xargs go test` and one `find … -exec rm -rf {} +` run a command named literally,
#      and install-jj's `"$HOME/.local/bin/mise"` is a tool `install` copied from a file named
#      literally, and each passes; install-jj's `"$HOME/.local/bin/jj" --version`, whose copy
#      came from a path `mise x` printed, was rewritten to `jj --version`, which PATH finds
#      there. Not refused, since none can be such a script or another reading holds it: a target
#      with no expansion that holds a `/` and is no file the text writes, which SCRIPT_CALL
#      reads, and a command word with no `/` and no `.sh`, a tool PATH finds; a last element
#      that is literal, since a script of that directory is a file directly in it and its name
#      is what SCRIPT_CALL reads, so `"$HOME/.local/bin/mise"` is a tool and
#      `"$script_dir/ci-retry.sh"` is that script; a
#      last element holding an expansion that is no `.sh` outside .github/scripts/, a data file
#      such as `"$state_dir/$name.state"`; a literal directory outside .github/scripts/, `bash
#      "scripts/e2e/$SCRIPT"`, the second limit below; the arguments a wrapper hands on, `"$@"`,
#      `"${@}"` and `"${cmd[@]}"`; a command word that is one variable's value in a text that
#      can see a function that fetches, a call through a variable, which the declarations below
#      read at the call; and one inside a function's body whose value the function's callers
#      hand it, a positional parameter, a variable the body sets to one, or, in a library some
#      text sources, a variable the library sets nowhere, read at those callers. Any other, such
#      as `s=$(…); "$s"` inside a function, is refused as at the top level:
#      the tree writes none, its three calls through a variable inside a function's body in a
#      text that sees no fetching function each being `"$@"` (dispatch://INFRA-1545, round 37). A
#      call of a function whose body runs one of its own positional parameters, or a variable
#      it sets to one (`local s=$1`, then `"$s"`), `run() { "$@" >> "$log"; }`, is a run of the
#      word it hands in that place, after the `shift`s the body writes first (runners_of): `run
#      "$p"` and `run "$(cmd)"` are refused, and `run grep …` and a literal path pass. A function
#      handing such a function its own parameter there, `wrap() { run "$@"; }`, runs it too, so
#      `wrap "$p"` is the call refused, and a script handing on its own arguments, `run "$@"` at
#      its top level, is refused, since this check does not follow its callers. The tree's seven
#      such calls, of check-plugin-names.sh's and check-private-names.sh's `run`, hand it `git`
#      or `grep`. A run another refusal already names, on any line of its command, is not refused
#      a second time. Until round 37 each way a printed or assembled path reached a run had a
#      model of its own, and round 37's survey found five that undercounted, each passing a
#      1650 s call under a one-minute key. The tree wrote the shape four times,
#      install-claude-code's `"$claude"`, install-omp's `"$omp"` and download-retry-proof.sh's two
#      `. "$file"`, and round 37 rewrote each to name its target literally, running what it ran
#      before.
#   2. A CALL OF A FUNCTION, 503: a command whose first word is the name of a function the same
#      text defines, outside that function's own body, after no word (480) or after `if` (9),
#      `if !` (9), `!` (4) or `elif !` (1) (CALL_PREFIXES). A call after any other word — a
#      `then` or a `do` on its line, an assignment prefix, `timeout`, `command` — is refused by
#      name where the function fetches. A function is read in one spelling, `name() { … }`,
#      which each of the tree's 170 definitions writes; every other spelling is refused by name
#      where a fetch is in its reach, as the list below says. Its lines run from its definition
#      to its body's last command, so a command after a `}` that starts its line is the text's
#      own; and a parameter expansion is one word to its closing `}`, as bash reads
#      `${name##* }`, which this tree writes at ci-tools-proof.sh:1190, 1206 and 1768 and
#      release-push.sh:1257. Until round 37 the function's lines ran to its `}`'s line, and the
#      expansion was split at its blank and its `}` read as the function's own, which ended
#      ci-tools-proof.sh's `probe` at line 1190 and `ladder_report` at line 1768; no fetch
#      or call this check charges sat after either, so no figure moved (dispatch://INFRA-1545,
#      round 37).
#   3. A LIBRARY'S FUNCTIONS: a library is a script a body sources, and a call of one of its
#      fetching functions is read as a call of a function is, with the same words before it.
#      This tree sources three, ci-retry.sh, ci-tools.sh and runner-shutdown.sh, and calls into
#      the last two 21 times. Refused by name: a name two of the libraries a body sources
#      define, or the body and one of them, where either definition fetches, since bash runs
#      the one it read last; and a call of a library's function that calls a function through
#      a variable, `run() { "$1"; }`, where the body can see a function that fetches, since such
#      a call runs whatever the body hands it and the library's own reading cannot see that. So
#      are a name two definitions share where one of them calls a function through a variable;
#      a library's call, by name, of a function it neither defines nor sources, which the
#      sourcing text or another library it sources defines and which fetches, since bash runs
#      that one; and a library one shell text sources more than once, as one it reaches along
#      two paths, since its own commands run at each source. A script that sources a library
#      and that one step runs more than once is a shell of its own at each run, each of which
#      can call the library's functions, while that library is charged once for the step; so
#      such a step is refused by name unless a DECLARED_LIBRARIES entry for that step, or for
#      the composite action whose step it is, writes what the library contributes to it
#      (dispatch://INFRA-1545, round 37). Until round 37's class search it was charged once and
#      refused nothing. This tree writes it once, as ci-job-tool-fallback.yaml's 'Seed mise's
#      install of jj with the staged binary, one byte changed' runs ci-tools-proof.sh three
#      times, whose entry writes the rig's one `ci_tools_fetch`, 1281 s: none of the three runs
#      calls a function that fetches.
#   4. A DECLARATION, keyed as the tree keys it: a DECLARED_LOOPS entry names a workflow step by
#      its file, its name and the verb, or a script by its file and the verb, with no step; a
#      DECLARED_LIBRARIES entry names a workflow step by its file and its name, or a script or a
#      composite action by its file, with no step; a DECLARED_CALLS entry names a script and
#      the variable. An entry keyed any other way matches nothing and fails this check, so two
#      steps of one action cannot be priced by two entries of one library, and one entry cannot
#      be charged to one of two steps of one name.
#   5. A LOOP'S PASSES: a `for x in <literal words>`, 19, of which dequeued.yaml's seven-pass
#      timeline poll is the one around a priced call, with a `sleep <n>` of that loop's body
#      as its wait. A `$(seq …)` list, a `for ((…))`, a `while`, and a `for` over anything else
#      is a loop whose passes this check does not read, refused where it holds a priced call
#      unless a DECLARED_LOOPS entry writes its seconds; the tree's four `$(seq …)` loops and
#      its one `for ((…))`, over check-pr-text.sh's `lists`, enter no figure. A loop runs from a
#      bare `for`, `while`, `until` or `select` opening a command to a bare `done` opening one,
#      and such a word in quotes or after a backslash, or a `case` pattern or an extglob
#      alternative that opens a command, `done)` on a line of its own or after `;;`, `(done)` or
#      `@(done|failed)`, is refused by name where the text fetches. A `done)` written after
#      `case … in` on that line opens no command: it is a word of the `case` command, which
#      neither opens nor closes a loop, for bash or for this check, so it is not refused and the
#      loops around it are read as written (dispatch://INFRA-1545, round 37: until round 37 this
#      paragraph said every `done)` was refused).
#   6. A BOUND: `retry_fetch <what> <seconds> --`, the helper's signature, 135 calls, the bound
#      read in that place alone. A call outside that signature, or behind `timeout`, another
#      retry or any other wrapper but `time`, fetches nothing — the helper refuses the first and
#      the wrapper cannot run a shell function — and is refused by name and charged nothing;
#      seconds written as a variable are refused as no number this check reads. And a curl's own
#      `--max-time <seconds>`, written once as two words after no option taking a value, which
#      this tree writes outside a Dockerfile as ci-tools.sh's `--max-time "$max_time"`,
#      declared. Every other spelling is refused by name, at a site an exception leaves
#      unretried too (curl_bound_refusal): a bound written more than once, since curl takes the
#      last; `-m<s>` or a cluster of short options ending in `m`; `--max-time=<s>`, which curl
#      rejects; one right after an option that takes a value, which is that value; one with no
#      value; and one that is no whole number of seconds, such as `1.5`. An excepted curl
#      writing no bound at all is refused too, since its step is then compared with nothing. A
#      `timeout` whose short options cluster before a value, `-vk 5`, is refused by name
#      (dispatch://INFRA-1545, round 37).
#   7. SHELL TEXT AND HEREDOCS: `bash -c '<text>'`, with `-c` the shell's one option and no
#      loop, function or alias in the text, one in this tree, dispatch-browser-run-reads.sh's
#      jobs read. Text in any other form — options before `-c`, `sh -c`, `eval` — is still read
#      for its sites, and refused by name where it fetches. A heredoc's body is data, as it is
#      for the consumers this tree feeds one to, `python3`, `cat` and `read`, and its operator
#      is one the Scanner reads outside quotes and comments (heredocs_of), so an `echo` of
#      GitHub's multi-line output syntax is no heredoc: until round 37 it was one, and every line
#      after it was read as data by every rule. Its terminator is its whole word with the quotes
#      and backslashes removed, as bash reads a word written `\EOF` or `"E"OF`. A heredoc fed to
#      anything else, to a command piped into a shell on its line, or unquoted with a command
#      substitution in its body, is refused by name where its body holds a fetch or names a
#      script that fetches. A body `cat` writes into a file, or `read` takes into a variable, is
#      data where it is written; the run of that file or that variable is a run whose target is
#      built from data or written as the step runs, refused where it happens (the reach under
#      item 1; dispatch://INFRA-1545, round 37).
#
# Each of these shapes is REFUSED BY NAME where a fetch, or a call of a script that fetches, is
# in its reach, with the file, the line, the step or the script, the shape and the form to write
# instead:
#   - a function defined with the `function` keyword, with a blank before or inside its `()`,
#     with a body that is not a `{ … }` group, after a reserved word on its line, inside
#     another function's body, under a name the same text defines again, or on the line another
#     function's body ends on, and a function that calls itself, directly or through another
#     (function_bodies, structure). A call in its body would be charged once for any number of
#     calls of the function. Its reach runs from its definition to the last line its body can
#     reach, and into the body of every function a call there calls. download-retry-proof.sh
#     defines two helpers inside the function calling them, and no fetch is written there, so
#     nothing is refused;
#   - two loops on one line and a loop opened after a reserved word on its line (`then for x in
#     1 2; do`), each refused where the text holding it fetches anywhere: a loop is read by the
#     line it opens on and only where its word opens a command;
#   - a script that reaches itself, running or sourcing a script that runs or sources it again,
#     refused at the call that leads back where it fetches (leads_back): a path stops at a
#     script it is already inside, so the run the shell makes on entering it again is charged
#     nothing;
#   - an `alias`, in the text defining it or at the source of a library defining one, a `{` or a
#     `}` that bash reads as a word of its command (in quotes, after a backslash, or after a
#     word that is no reserved word), and a loop's own word bash reads as another word, each
#     refused where the text fetches anywhere (whole_text);
#   - a command after the `}` that closes a function, on a line its body holds a command of,
#     which is read as the function's;
#   - a definition of a function named `retry_fetch` anywhere but in .github/scripts/ci-retry.sh,
#     and a `retry_fetch` call outside its signature or behind a wrapper, wherever written
#     (odd_command);
#   - a `timeout-minutes` of a step or a job written as anything but a whole number with no
#     leading zero or an expression, and a line of text below a pricing comment that reads as
#     `timeout-minutes: <n>` and is no key, wherever written;
#   - and, outside rule 8's shapes but for its arithmetic, a site an exception leaves unretried
#     that makes attempts of its own with no bound, or under a `retry_fetch` whose seconds are a
#     variable, which rule 4 would refuse anywhere else and whose wait no budget can be held
#     above, a curl it leaves unretried that writes no bound or writes one in a form
#     curl_bound_refusal names (dispatch://INFRA-1545, round 37).
# Three shapes this tree writes are REFUSED BY NAME UNLESS DECLARED, since a refusal alone would
# refuse the tree's own scripts (dispatch://INFRA-1545, round 37): a call of a function through a
# variable where the text can see a function that fetches (DECLARED_CALLS, or a DECLARED_LOOPS
# entry for each fetch it reaches), the variable being any word that is one parameter's value,
# `$fn`, `${fn}`, `$1`, `"$@"` or an array's `"${cmd[@]}"`, outside the operands of a
# `[[ … ]]`, and a `$x)` after a `|`, which may be a pattern's alternative or a pipeline's last
# command, refused by name where such a function is seen; a library a step reaches along more
# than one path, or along one path more than once, as a script sourcing it that the step runs in
# more than one place; and a library's fetching functions called more than once after sourcing it
# (DECLARED_LIBRARIES).
# Each declaration's figure enters the step's arithmetic, and WHAT IT COUNTS ONCE OR MISCOUNTS
# under rule 8 names the tree's instances.
# A `timeout` whose duration is not a whole number of seconds above 0 (`timeout 0`, a decimal, a
# variable) and a curl `--max-time 0` bound nothing, since a 0 is no limit at all, and the fetch
# behind `timeout`'s options stays a fetch (reduce_words, curl_options): rule 4 refuses such an
# attempt, and the refusals above read it as a call with no figure rather than as 0 s.
# And these are read without a model, as the shell's own operators, which charges a run written
# there in full or refuses its place where its script fetches, so a path written in one of them
# is never charged less than the step waits for:
# a path in a `[[ … ]]`, in an extglob pattern such as `@(a|b)` or in a `case` pattern, whose
# `(`, `)`, `|`, `<` and `>` read as a subshell, a pipe and a redirect, so the path is a run, an
# argument of the word before it, a redirect target or the words of the `case`. The one reading
# of a `[[ … ]]` this check makes is which commands are its operands, for a call through a
# variable: the command an unquoted `[[` opens and those `||` or `&&` split off up to an unquoted
# `]]`, where `"$fn" ]]` is a call (variable_calls; dispatch://INFRA-1545, round 37).
#
# The recognised calls are then charged (shell_arithmetic). A RUN is charged once for every
# time the step runs it: the loops around it, the calls of the function holding it and the
# loops around those, so a step that runs a.sh and then b.sh, each running c.sh, waits for
# c.sh twice. A library a step SOURCES — itself, through another library, or inside any script
# it runs — is charged once for that step, at the most expensive path to it, and the three shapes
# where that charge falls short, more than one path to it, one path to it walked more than once
# and more than one call of its functions, are refused unless declared, as the paragraph above
# says. A step is one shell: a workflow step, or one step of a composite action, which rule 8
# already reads as steps of their own, so install-jj, which sources .github/scripts/ci-tools.sh
# in two of its steps, is charged that library twice. A script that reaches itself is refused by
# name, as the list above says.
#
# Two limits are left in that reading set, and each is a limit of what this check can read, a
# script it cannot name, rather than a shape it reads and miscounts (dispatch://INFRA-1545,
# round 37). A run whose target is built from data as the step runs, or whose route from text to
# a program this check does not follow, is refused where that target could be a script under
# .github/scripts/ (the reach under item 1), so what is left of the first limit is what that
# reach lets pass, each of these. A file one step or script writes that another runs by a
# literal path holding a `/`, `./run` or `bash out/run.sh` in a later step, since a file another
# text writes is no file this text writes; a file this text writes that holds a tool,
# downloaded, built or copied from a file named literally, and runs as a command, whose bytes
# this check does not read; and a command named literally that `xargs` or `find -exec` runs,
# `xargs go test`. And, AN ACCEPTED LIMIT, the declaration model for calls through a variable:
# a call through a variable in a text that can see a function that fetches is read as a call of
# a function, refused unless a DECLARED_CALLS entry writes what it adds or a DECLARED_LOOPS entry
# writes each fetch it can reach, and that figure stands for the call whatever the variable
# holds, a script's path included; and a library function running a variable the library sets
# nowhere runs what the sourcing text sets, which the reading of holders follows there only
# where that text can see a function that fetches. The architect accepted this model for the
# forms this tree writes, because a refusal alone would refuse the tree's own scripts:
# ci-tools.sh's `$fetch` and `$seam`, whose fetches DECLARED_LOOPS prices, and release-push.sh's
# `"$leak_engine"` and its `true` or `false` flags, DECLARED_CALLS entries, each run a function,
# a tool or a builtin and never a script, and a declaration names its figure and why where a
# reviewer reads it (dispatch://INFRA-1545, round 37). Until round 37 every call that did not
# write the script's path was this limit. A path that is written, in a variable or in any other
# place the first list refuses, is refused where its script fetches. And a script outside
# .github/scripts/ is not read at all, since that one directory is the whole set: a `run:` that
# calls the repository's own scripts/<name>.sh or a packages/<package>/scripts/<name>.sh —
# ci.yaml and the release workflows each hold such calls — is read no further than its own
# words, so a download written inside one of those files is held to no floor here and sits in
# no budget; which of those files this check should reach is dispatch://INFRA-2899's question.
# Moving one under .github/scripts/ is what brings it back under both rules, and a new file there
# is read without anyone remembering to list it.
#
# A heredoc's body is blanked before any rule reads the step (without_heredocs), so a path
# written in it is a path something prints rather than runs, a later run of what it printed
# being refused where it runs (the reach under item 1), and the Python a `python3` heredoc of a
# script runs is read by rule 6 alone.
# The harnesses left out are named at TEST_HARNESS below, with the two network calls they hold
# today and why a retry at either would destroy what the test measures.
#
# AND A BUILD CAN FETCH, which is the limit of the verb list rather than of the reading set.
# `go build`, `go test`, `go vet` and `go run` download a module this job's cache restore did not
# bring, and that fetch is in none of THE VERBS below, so no site of it is ever counted: a cache
# restore is not a download step, and the fetch sits inside a command whose purpose is something
# else. Rule 11 below is what reads that shape now, and it reads less than the rules above it, so
# each line of its reach is written here beside the line of the rule that decides it. What it
# reads: a job's `run:` body, in a workflow file, matched for `go build|test|vet|run` against a
# `go mod download` in a `run:` body of the same job. What it does not read, each a hole a reader
# would otherwise fill in wrongly:
#   - a composite action's steps, twice over: its loop walks `workflows` alone, and `job is
#     None` drops an action's steps even where one is read;
#   - a Go command inside a script the job calls, since it reads `step.get("run")` and follows
#     no call;
#   - the order of the two: a build before the download, or a build earlier in the same step,
#     passes, because the rule asks whether the job holds a download and never where;
#   - which module comes down: a download of `packages/envoy` answers for a
#     `go -C packages/envoy/infra test` whose module `go.work` does not bind;
#   - commands outside its pattern that fetch, among them `go list -deps`, `go -C=dir build`,
#     `go mod tidy`, `go get` and `go list -m all`. `go generate` is not one: driven at this
#     head in packages/envoy with an empty module cache and GOPROXY=off, `go generate -n ./...`
#     exits 0, while `go list -deps ./cmd/dispatch` fails `module lookup disabled by
#     GOPROXY=off`. Only a command a `//go:generate` directive runs can fetch.
#   - `go install` and `go run` of a `module@version`, which build from THAT module's own go.mod
#     and read no go.work at all, so a job's workspace `go mod download` covers none of what
#     they fetch: each such call is a download site of its own. They are retried where they are
#     written - scripts/go-third-party-notices.sh (go-licenses), scripts/e2e/lib/
#     live-daemon-proof.sh and scripts/e2e/dispatch-session-settings.sh (nats-server),
#     packages/envoy/loadtest/cluster/render-nats.sh (helm), and ci.yaml's own step for the four
#     tools packages/envoy/infra's live tests run.
# THIS PARAGRAPH CLAIMS NO SET OF JOBS, because three rounds of dispatch://INFRA-1545 falsified
# that sentence in the same direction each time: it named a set its reader cannot enumerate.
# What it states instead is what was audited and how. The audit, at each head where it is
# rewritten, reads every workflow job that compiles Go or installs a tool - directly, through a
# script it names by a literal path, through a script that one names, or through a composite
# action - and holds each to its module's retried download. What that method does not reach, and
# a reader must read for themselves: a script whose path a step builds at run time (e2e.yaml's
# proof names its own by a matrix value), a build inside another tool's own script
# (dispatch-browser.yaml's visual, through Playwright's server script), a third-party action's
# own build (codeql.yaml's Go row, inside github/codeql-action/autobuild, which no rule here can
# reach and which the exceptions name), and a script this check does not read at all, which is
# every one outside .github/scripts/. Emptying GO_BUILD_EXEMPT is no evidence for any of it: it
# shows what rule 11 reads and never what its holes hide. dispatch://INFRA-2648 owns
# the reach and the measurement that would widen it. Four times the shape was a real defect that
# every other rule passed: dispatch-browser.yaml's embedder (round 12),
# dispatch-browser-nightly.yaml's re-record (round 14), and e2e.yaml's proof and
# dispatch-browser.yaml's visual (round 17). Each was caught by a person reading the job rather
# than by any check, and the last two stood inside a hole this paragraph names.
#
# THE VERBS. A fetch is one of these commands, in command position, after the wrappers below are
# stripped: curl, wget, apt-get/apt (update, install, download, upgrade, dist-upgrade, source,
# build-dep), pip install, uv (sync, add, pip, tool, python, lock, export), bun install, bun add,
# npm/pnpm/yarn (install, ci, add), playwright install, go mod download, go install,
# mise (install, use), crane (copy, digest, config, pull, export, manifest, blob), docker pull,
# pulumi plugin install, and gh — every subcommand of it but the local ones named next.
#
# gh is read the other way round from the rest, by what it is not. `gh api` and `gh release
# download` were the whole of its table once, and an unretried `gh run list`, `gh pr view`, `gh
# run view`, `gh cache delete`, `gh workflow run` or `gh search code` was therefore invisible
# here: every one of them is the same HTTPS call to the same host as a `gh api`, carrying the
# same token, and the difference between the two sets was a subcommand name and nothing else. So
# the set is gh's own command list minus what reaches no network, read off `gh --help` and off
# each command's own help: GH_LOCAL below — `alias`, `completion`, `config`, `extension`, `help`
# and `version`, which answer out of the local config, the local extension directory or the
# binary — and the four local actions of `gh auth`, `git-credential`, `setup-git`, `switch` and
# `token`, which read the stored token or write the local git config, where `gh auth login`,
# `logout`, `refresh` and `status` talk to the host. Anything else gh runs is a fetch, so a
# subcommand nobody has listed is held to the floor rather than passing unseen. A verb is named
# `gh <subcommand> <action>` for the subcommands that take an action of their own (GH_GROUPS:
# `gh run list`, `gh release view`), and `gh <subcommand>` for the three that take an argument
# instead (`gh api repos/owner/name`, `gh browse`, `gh status`). A gh call that writes is a site
# like a gh call that reads: `gh api --method POST` has always been one here, and the retry is
# for the transport, which does not know the method.
#
# `mise x`, `mise exec`, `mise up` and
# `mise upgrade` are deliberately not among the verbs; rule 7 below says why, and what it does
# with one instead. `bunx`, `npx`, `pnpx`, `sh -c`, `bash -c` and `eval` are transparent: the
# verb is what they run, so a `bunx playwright install` is the Playwright download and a `bunx
# biome check` is not a fetch at all (the binary it runs came from the workspace install, which
# is itself a site).
#
# The wrappers stripped before the command is read are an assignment, a shell keyword, `sudo`,
# `env`, `exec`, `command`, `nice`, `nohup`, `time`, `timeout <seconds>`, `retry_fetch <what>
# <bound> --` and a Dockerfile's `retry <bound> <what>`. The last three are also what says a
# fetch is retried and bounded. A command that answers about itself (`--version`, `--help`) reads
# nothing from the network and is no fetch, whatever its verb.
#
# Two verbs are deliberately outside the subject, because R1's subject is a tool, a toolchain or a
# dependency: `git clone` and `git fetch`, which move this repository's own refs rather than
# anything a job installs, and which no site in the tree retries. `playwright install-deps` is
# named under the exceptions below.
#
# THE RULES.
#
#   1. In a workflow `run:`, an action.yml `run:` or a *.sh under .github/scripts/, a fetch must
#      be a `retry_fetch` call or, for curl, carry curl's own options at the floor.
#   2. A loop (`while`, `until`, `for`) whose body holds a fetch that is not a `retry_fetch` call
#      is a copy of the helper and is refused as one, whether or not that fetch would pass rule 1
#      on its own: R2's rule is that one place holds the attempts. A loop reaches a fetch two
#      ways, and both count: the fetch is written in the body, or the body is a function the same
#      block calls with a fetch among the call's words — `attempts 2 "apt-get update" sudo timeout
#      90 apt-get update`, the shape the shared helper was made from.
#   3. A Dockerfile `RUN` that fetches opens with the canonical `retry()` text (CANONICAL below),
#      compared with its backslash continuations joined and each run of blank space collapsed to
#      one space, which is the one liberty the tree's own two shapes force: a `RUN --mount=...`
#      indents the definition under the flag. Each fetch of that `RUN` is then wrapped in
#      `retry <bound> <what>`, or is a curl carrying curl's own options. A `RUN` that defines a
#      retry loop of its own text is refused. A Dockerfile is judged against the floor alone:
#      rule 2 does not read one, because the canonical definition is itself a loop and because a
#      `RUN` iterating a list is how a Dockerfile writes what a script would write as a function.
#      A `RUN` is read as Docker reads it: a comment line or a blank line inside its continuation
#      is dropped and the `RUN` reads on, so a fetch written below a comment is a fetch of that
#      `RUN` (dispatch://INFRA-1545, round 37). What holds the key of the step building a
#      Dockerfile is rule 10: above the longest single ladder of its RUN lines, since a build
#      meets a fault window through one fetch and pays one ladder for it.
#   4. An attempt with no time bound is refused: a curl with no `--max-time`, or a retried fetch
#      of any other verb that no `timeout` and no helper bounds. A bound of 0 is none, as
#      timeout(1) and curl read it: `timeout 0` and `--max-time 0` set no limit
#      (dispatch://INFRA-1545, round 37).
#   5. An `apt-get update` carries `-o APT::Update::Error-Mode=any`. This is apt's `--fail`, and
#      the reason is curl's: a fetch that reports success having fetched nothing. `apt-get update`
#      exits 0 when every index fails to download — it prints `W: Failed to fetch … 504 Gateway
#      Timeout` for each one and then `W: Some index files failed to download. They have been
#      ignored, or old ones used instead.` — so a retry around it succeeds on its first attempt
#      having read no list, and the `apt-get install` after it fails with `E: Package '…' has no
#      installation candidate` against a list that will never name the package. That is a failure
#      no further attempt can cure, which is what cost run 42375256920's `download-retry-images`
#      job. The option turns each of those warnings into an error, so the command exits non-zero
#      and the retry retries. It has existed since apt 2.1.10; the images here run apt 2.6
#      (bookworm) and 2.9 (trixie), and the runner's own on Ubuntu 24.04.
#
#      Every `apt-get update` this check holds to the floor carries it, retried or not. The two
#      sets are the same once the tree is green, since rule 1 and rule 3 retry every one of them;
#      scoping the rule to the retried ones would differ only in handing an author who writes a
#      bare `apt-get update` one refusal now and a second one after they add the retry. The sites
#      an exception below removes are outside this rule as they are outside the rest.
#   6. In Python, inside the heredoc of a *.sh under .github/scripts/, a read through
#      `urllib.request` goes through check-ci-images.sh's `read_retrying`, which carries the floor
#      in that check's own idiom — five attempts, waits of 10, 20, 40 and 80 seconds, each bounded
#      by the read's own `timeout=30`. One definition, no copies, as the shell has. A read is
#      `read_retrying`'s where it is written in the same heredoc, each a program of its own,
#      indented deeper than the `def` with no line between them at the `def`'s indentation or
#      shallower; until round 37 one stack of `def`s ran across every heredoc of a script
#      (dispatch://INFRA-1545, round 37).
#   7. A `mise x`, `mise exec`, `mise up` or `mise upgrade` carries no retry, anywhere: not a
#      `retry_fetch` call and not a Dockerfile `retry` call. None of the four fails when it
#      cannot resolve the tool. `mise x` warns (`Failed to resolve tool version list for … HTTP
#      host https://api.github.com:443 is unavailable`) and then runs whatever binary of that
#      name is already on PATH — a different build, at a version nobody asked for — and exits 0;
#      `mise up` prints `All tools are up to date` having read nothing, and exits 0. Measured on
#      mise 2026.8.12, the build packages/daemon/docker/worker.Dockerfile pins, against a proxy
#      faulting every host. So a retry around one of them waits for a failure that never comes,
#      and the cure is structural: retry `mise install <tool>`, which exits 1 on the failed fetch
#      on that same build, and read the path afterwards from what that install put on disk.
#
#      That is also why the four are not fetch verbs above, and why a bare one is on no list. A
#      `mise x` that follows the install of the tool it names resolves off disk: with every host
#      faulting and a decoy binary earlier on PATH it still printed the installed path, silently
#      and exit 0. A `mise x` that follows no install fetches only as a side effect of the
#      resolution that install should have done. Holding either to the floor would be demanding
#      the one shape this rule refuses, so the check reads these four only to refuse a retry.
#
#   8. A step that holds a retried fetch carries its own `timeout-minutes`, and that budget
#      gives strictly more time than the attempts the step allows: 60 x <minutes> is strictly
#      greater than
#      that step's arithmetic, which is 5 x <bound> + 150 s for each `retry_fetch` call and,
#      for each curl making its own attempts, one attempt per `--retry` and one more at
#      `--max-time` with curl's own waits between them — the same ladder rule 1 holds that curl
#      to, computed once in curl_ladder and read by both rules, so `--retry 7` is
#      8 x <bound> + 127 s and `--retry 10` is 11 x <bound> + 1023 s. A curl outside rule 1's
#      floor is read at the floor, which is the ladder it makes once that refusal is answered.
#      Those figures are summed over the step's own
#      calls, over those of every script under .github/scripts/ the step runs and every script
#      that one runs in turn, and, for a `uses:` step of a local composite action, over that
#      action's calls and the scripts and actions it reaches the same way. Equal is
#      not enough: the last attempt's own error and the runner's kill then land at the same
#      instant, and the retry can lose that race. Measured in run 42386541065's job
#      128445139635, where a `gh api` read bounded at 30 s sat under `timeout-minutes: 5`, its
#      own arithmetic of 5 x 30 + 150 = 300 s against a budget of 300 s: the helper's `::error::`
#      landed at 17:22:16.7444966Z and the runner's kill at 17:22:16.7483230Z, four milliseconds
#      apart. Lose that race and the step reports GitHub's bare `The action '…' has timed out
#      after 5 minutes.` instead of naming the site and its attempts, which is the loud failure
#      the retry exists to give. GitHub's granularity is the minute, so a whole minute is the
#      smallest margin that can be written.
#
#      A step holding an arithmetic above zero and writing no `timeout-minutes` at all is
#      refused with the figure it needs. A budget that does not exist is judged by nothing, and
#      nothing was what judged ci.yaml's `airlock-lean` at 61e220f36: the step running
#      .github/actions/install-elan carried that action's curl of the elan tarball,
#      8 x 60 + 127 = 607 s, wrote no budget of its own, and the job's twenty minutes carried
#      no term for it either, so an exhausted ladder there would have ended the job on GitHub's
#      bare timeout line. This check read that step's cost and compared it with nothing,
#      because only a step that wrote a budget made an entry to compare. Every step of a
#      workflow makes one now. A composite action's step makes none: GitHub takes no
#      `timeout-minutes` on one, so the budget that holds an action's calls is the `uses:` step
#      that runs it, which is where the figure is asked for.
#
#      The job's own `timeout-minutes` is held above that same arithmetic, because a job
#      ceiling at or under one of its steps' attempts cuts the step before the step's own budget
#      can name the site, and the step's margin then buys nothing. A job writing no
#      `timeout-minutes` is held at GitHub's own default of 360 minutes (JOB_DEFAULT_MINUTES).
#
#      A CEILING WRITTEN AS AN EXPRESSION IS A CEILING THIS CHECK DOES NOT READ, and it is
#      worth stating here rather than leaving for whoever writes the next one.
#      `written_minutes` takes a whole number and nothing else, so `timeout-minutes: ${{ matrix.timeout_minutes }}`
#      reads as a job that wrote none and is judged at the 360-minute default, whatever the
#      matrix row really allows. The instance in this tree is .github/workflows/e2e.yaml's
#      `proof` job, whose limits are the rows of .github/e2e-scripts.json: those rows are
#      derived by hand, each against the slowest retried step its own flags select, and that
#      file says so where they are written. Anyone adding such a row gets no gate, so the
#      arithmetic has to be written beside it. A step that writes its budget as an expression
#      is read the same way, which for a step means it is refused as writing none, so no step
#      of this tree writes one.
#
#      PYTHON IS READ IN ONE PLACE ONLY, which is the other gap worth stating here. Rule 6
#      reads a `urlopen` written in a heredoc of a `.sh` under .github/scripts, so those
#      reads meet R1's floor. Python anywhere else — a `.py` file, or a heredoc in a
#      workflow's or an action's `run:`, which the shell rules blank as data — is read by no
#      rule of this check at all: not rule 8, which puts a figure on none of it, and not rule
#      1, so a network read written there gets no floor rather than merely no price. ci.yaml's
#      `Check the package holds
#      every row of .github/ci-images.yaml` is this tree's instance: its reads are urllib's
#      inside .github/scripts/check-ci-images.sh, held to the floor by rule 6 and budgeted by
#      hand.
#      This is the rule ci-budgets.md's Budgets note already writes for a reader — a job carries its
#      measured duration plus the arithmetic of its slowest retried step — read here as the
#      half a check can make, which is the arithmetic and not the measurement.
#      That half is read twice, because one reading of it left a whole class green. A job is
#      refused when its ceiling sits at or under a step's RETRY ARITHMETIC, and refused again
#      when its ceiling is not above that step's OWN `timeout-minutes`: the second is what
#      catches a job whose steps each fit their own budgets while the job holds all of them
#      together, and
#      twenty-two job keys of this repository stood below their own documented derivation,
#      under a green check, until it was added (dispatch://INFRA-1545, round 27). The measured
#      term stays out of both readings, so a job's comment still carries arithmetic no program
#      checks, and the step keys this check does demand are the floor under it rather than the
#      budget itself.
#
#      How many times a step runs one call, and what this check can see of it. Rule 8
#      multiplies a call by the loops and the function calls it resolves, which are the three
#      kinds listed next; where a loop it did read writes no count it refuses the site rather
#      than guessing; every shape it reads as neither a loop nor a call leaves the call counted
#      once; and a few shapes it reads as a loop with the wrong count, so the call is multiplied
#      by a number that is not the step's. It is no shell parser, so the list of what it counts
#      once or miscounts, below the three, is as much this rule as the list of what it
#      multiplies.
#
#        * each loop around the call, and each loop around that loop. A call in a three-pass
#          loop inside a two-pass loop runs six times, and reading the inner loop alone would
#          count two of those six.
#        * each call of the function the call sits in, and the loops around those calls. A
#          page read written once in a function that a seven-pass loop calls is seven ladders
#          and not one. check-pr-text.sh reads like that instance and is not it: its page read
#          sits in read_list's `while :`, a loop this check cannot count, so the fold stops
#          there and never reaches the `for ((…))` over `lists` that runs read_list once per
#          list, whose passes this check reads no more than it reads the `while :`'s. The three
#          lists are carried by hand instead, in that script's DeclaredArithmetic entry below,
#          which writes the 3 x in its own sum.
#        * the loop a step wraps around the script it runs. The script's whole arithmetic is
#          the step's once per pass, as a call written in the `run:` would be.
#
#      Two written calls of one function are two, branches or not, as two fetches written in
#      one `run:` are two: this check reads no branch, and the figure it sums is the one a step
#      could wait for. The pass count is read where the loop writes it, and only in the shape
#      this tree writes, a `for x in 1 2 3 4 5 6 7`, whose list is literal words (THE FORMS RULE
#      8 READS, 5). dequeued.yaml's timeline read is one `retry_fetch` call of 300 s written
#      inside a seven-pass poll, so the step waits 2100 s for it and not 300 s. The
#      loop's own waits between the passes are added where they are written as a `sleep <n>` of
#      that loop's body — the poll's six 10 s waits above — and a loop whose wait is computed
#      rather than written adds none, so the figure there is the attempts alone and the passes
#      between them cost nothing in it.
#
#      A loop whose pass count is not written — a `while read` over a list the run makes, a
#      `while :` over the pages of a route — is a count this check does not make, and no pass
#      number stands in for one it did not make. What answers such a site is a
#      DeclaredArithmetic entry of DECLARED_LOOPS below: it writes the seconds that site
#      contributes to the budget of the step holding it, and its `why` writes that figure out
#      as a sum, so a reader checks the arithmetic rather than taking the number. The figure
#      may not fall below one call of the site, which is what one_call computes: a site costs
#      at least one call of itself, whatever the run finds for it to do, and a figure under
#      that floor is refused naming the site and the floor.
#
#      A per-attempt bound this check cannot read is the other reading that stops rule 8, and
#      a DeclaredArithmetic entry answers that one too. A curl writing `--max-time "$T"` is
#      bounded — the option is there, and the attempt really is bounded when the step runs —
#      while the seconds are the caller's, so one_call answers None for it and there is no
#      per-call figure for any pass count to multiply. Such a site needs no loop around it to
#      be unpriceable: with the passes read and the bound unread, what the step waits for is
#      as unknown as with neither read. So an entry writes the whole of what that site
#      contributes rather than one call of it, the floor above is not asked of it — there is
#      no one call to compare it with — and a site no entry covers is refused in the bound's
#      place, with the two ways out: write the bound as a number, or declare the site. A site
#      that writes no bound at all is rule 4's refusal and stays rule 8's silence, since the
#      figure this rule would ask its author for is the one rule 4 is already asking them to
#      write. The call is a wait all the same and not an absence of one, so a script holding it
#      is a script whatever runs it waits for, and a loop this check cannot count around a call
#      of that script is refused rather than dropped. .github/scripts/ci-tools.sh's
#      ci_tools_curl is the one such site in this tree — the helper every tool install of
#      .github/ci-tools.yaml fetches through — and rule 8 priced it, and so those installs, at
#      nothing until the declaration below priced it (review round 9 of dispatch://INFRA-1545).
#
#      WHAT IT COUNTS ONCE OR MISCOUNTS, which is the other half of this rule and the half a
#      reader is owed plainly. Round 37 took off this list every shape it named that priced a
#      step under its real cost and that this tree does not write, each now refused by name as
#      THE FORMS RULE 8 READS and the list after it say: two loops on one line, a loop opened
#      after a reserved word, a function that calls itself, a loop over an array or over
#      `$(seq …)` and every `for ((…))`, which are loops whose passes this check cannot read, and
#      a call of a function after any word but `if`, `if !`, `!` and `elif !`, which is no call
#      it reads (dispatch://INFRA-1545, round 37). This list therefore names no shape that
#      prices a step under its real cost: each is read, refused by name, or refused by name
#      unless a declaration writes its figure by hand. A script that sources a library and that
#      one step runs in more than one place was such a shape until round 37's class search,
#      charged that library once and refused nothing; it is the second kind below now
#      (dispatch://INFRA-1545, round 37). Three shapes this tree writes are the last kind, each
#      refused where no declaration covers it, and each declaration's figure is the one the
#      budget holding it already writes, with the sum it comes from:
#
#        * a call of a function through a variable, such as `"$fn"` or `$fetch`, which is no
#          call this check reads (variable_calls). Where the text can see a function that
#          fetches, the call is refused unless each fetch such a function holds is a
#          DECLARED_LOOPS entry, whose figure is that fetch's whole contribution for every
#          caller, or a DECLARED_CALLS entry writes what the calls through that variable add.
#          .github/scripts/ci-tools.sh's `$fetch` and `$seam` calls are covered by its `curl`
#          entry, which every fetch they reach goes through; release-push.sh's calls of its leak
#          engine and of its `true`/`false` flags are DECLARED_CALLS entries at 0 s.
#          runner-shutdown.sh writes no call through a variable.
#        * a library a step reaches along more than one path — it sources it and runs a script
#          that sources it too, or runs two scripts that each do — or along one path more than
#          once, as a script sourcing it that the step runs in more than one place, each run a
#          shell of its own; either is charged once, at its most expensive path, though each
#          path and each run can call its functions (shell_arithmetic, SHARED_LIBRARIES). The
#          step is refused unless a DECLARED_LIBRARIES entry writes what the library
#          contributes to it, which then replaces that charge. The tool-fallback steps that
#          source .github/scripts/ci-tools.sh and run ci-tools-proof.sh's `version-answers`,
#          which fetches nothing, are declared at 1281 s, the library's one `ci_tools_fetch`,
#          and so is the step 'Seed mise's install of jj with the staged binary, one byte
#          changed', which runs ci-tools-proof.sh three times, none of the runs calling a
#          function that fetches. The ci-ok-report action, which reaches runner-shutdown.sh
#          through ci-ok-report.sh and through the ci-ok.sh that runs, each of which makes one
#          call of runner_shutdown_kills in a shell of its own, is declared at 2 x (300 + 480) =
#          1560 s, which .github/ci-budgets.md's Budgets note writes into the report's 2160 s.
#        * a library's fetching functions called more than once after sourcing it, or inside a
#          loop whose passes this check cannot read, which are charged as the library's one
#          charge (library_functions). The body is refused unless a DECLARED_LIBRARIES entry,
#          for the step or for the file whose calls they are, writes what the library
#          contributes. ci-tools-proof.sh's `stage`, which calls ci_tools_fetch once for each
#          of its five staged rows, is declared for the staging step at the 5124 s its budget
#          writes and for every other step running that script at 1281 s; the blob check's
#          loop of ci_tools_curl_status calls at its own 2015 s; mirror-ci-tools.sh's fill at a
#          cold fill, 427 + 4 x 3633 = 14959 s, which .github/ci-budgets.md derives; and
#          ci-ok-report.sh's two calls of shutdown_killed, of which a run makes one, at 780 s.
#
#      What is read correctly, though a reader may expect otherwise: a retried call written
#      directly, not through a function, as the condition of an `if` inside a loop or behind an
#      assignment prefix such as `GH_HOST=x`, and a loop written on its own line inside an
#      `if`, are each multiplied by their loop. A `for` over anything but literal words, an
#      array's `"${name[@]}"` and a `$(seq 1 7)` included, and every `for ((…))`, are loops whose
#      passes this check cannot read, and are refused, as the paragraph below that begins "A
#      step whose budget reaches a loop" says.
#
#      The rest goes the other way, and is accepted: each prices a step at or above its real
#      cost, or refuses a step that is right. A script call and a loop written on one line are
#      read as the loop holding the call, and so is a command written on the line a one-line
#      function's body sits on, which is multiplied by that function's calls; a body defined
#      again charges every call of its name; and each refusal by
#      name above stands wherever a fetch is written in its reach, whatever the shape hides
#      there, a loop's reach being the whole text holding it.
#
#      None of this asks for a wider reading. A parser that learns some of those shapes and
#      stays silent on the rest is this same defect one layer in and harder to see, where a
#      list a reader can check is worth what it says and no more.
#
#      A step whose budget reaches a loop this check read but whose passes are not written,
#      and for which no DeclaredArithmetic entry writes the seconds, is refused in place of
#      being judged, and the refusal names the file, the line and the verb, the loop line it
#      could not read, and what answers it. That refusal is the default for the loops this
#      check reads, and for those alone. This rule has been wrong here four times: three fixes
#      taught the reading one more loop shape each while leaving the default on pass, and the
#      fourth was a figure of 0 written for a bound nobody read. A loop read with no count is
#      refused now, a bound read with no number is refused with it, and a declaration carries
#      its own arithmetic or it is not one.
#
#      An entry matching no such site fails this check, as a stale exception does, so a
#      declaration cannot outlive the loop or the unread bound it was written for; and an
#      entry whose file, step and verb match sites
#      in two loops fails as well, because it writes one figure and the second loop would take
#      it without anybody writing it one. A loop in a script no step runs is nobody's
#      arithmetic and is left alone, and so is a Dockerfile's, whose RUN lines sit in no step's
#      budget; rule 2 is what reads a loop wherever it sits.
#
#   9. In a step whose shell leaves `pipefail` off, a fetch that is not the last command of its
#      pipeline is refused. A workflow `run:` that names no shell runs under GitHub's own
#      default, `/usr/bin/bash -e {0}`: errexit on, pipefail off, and a pipeline's status is
#      then its last command's alone. So `curl -fsSL <url> | tar -xz` exits 0 when curl got a
#      504 and tar unpacked nothing, and a `retry_fetch` around that pipeline reads that 0 and
#      never retries: the site reads retried here and is unprotected in fact. It is the class of
#      curl without `--fail` (rule 1), of `apt-get update` without its error mode (rule 5) and
#      of `mise x` running a decoy (rule 7) — a command that reports success having fetched
#      nothing. The cure is to read the fetch's own status: write the pipeline as two commands,
#      with the fetch's output in a file or a variable, or give the step a shell that sets
#      `-o pipefail`.
#
#      Which steps the rule reaches is read rather than assumed, because the answer is each
#      step's own: its `shell:`, else its job's `defaults.run.shell`, else the workflow's, and a
#      value of `bash` — which GitHub expands to `bash --noprofile --norc -eo pipefail {0}` — or
#      a custom command line writing `pipefail` itself takes that step out of the rule. So it
#      reaches every workflow `run:` of this tree but main-bypass-check.yaml's one step, which
#      names `shell: bash` on purpose and whose own test holds it to that shell, and it reaches
#      no composite action's step: GitHub requires one to name a shell, and all eleven here name
#      `bash`. A *.sh under .github/scripts/ is outside it as well, each setting its own `-o
#      pipefail`, and so is a Dockerfile RUN, which names no step's shell and whose pipelines
#      are rule 3's business. Measured in run 42386541065's job logs: 63 steps at
#      `/usr/bin/bash -e {0}`.
#
#      The scripts are outside it for a reason that is a limit as much as a scoping, and the
#      limit is written here rather than left for a reader to find: this rule asks its question
#      of a step's shell, and a script sets its own flags, so a piped fetch inside a *.sh under
#      .github/scripts/ is a hole this rule has no step to read. The hole is unreachable today,
#      and that is a measurement rather than a habit: all 70 scripts under .github/scripts/ set
#      `pipefail`, so `grep -L pipefail .github/scripts/*.sh` prints nothing. A script that
#      stops setting it would carry the fault this rule refuses in a step, and nothing here
#      would say so. What closes the hole is reading each script's own `set` line, and the day a
#      script leaves `pipefail` off is the day to write that reading.
#
#      That reading is the rule rather than a grep over the workflows, and the difference is not
#      academic: a rule refusing every workflow `run:` would be one line and would refuse
#      main-bypass-check.yaml's step, the one site in the tree that named a pipefail shell on
#      purpose. A scope taken from "no step sets a shell here" punishes the careful site and
#      passes the careless one the day either changes, so the question is asked of each step.
#
#      No site in the tree is refused by this rule today, and three lines look as though they
#      would be. Both pipeline spellings count: `|`, and `|&`, which is bash's pipe of standard
#      error beside standard output and loses the fetch's status the same way. `||` is neither
#      of them but a logical OR, and it sets nothing — which is what ci.yaml's `cancel-started`
#      step and dequeued.yaml's `cancel-dequeued` write, each ending a line with `gh api
#      --silent --method POST … ||`, and what the scanner tells from a `|` by the character
#      after it. release-envoy-listener.yaml's `if ! legacy_source=$(gh api …)` is the third
#      line, and it reads that fetch's own status, which is what this rule asks for.
#
#      A site this rule refuses that another rule refuses too is one `::error` and not two. The
#      sentence is appended to that site's own refusal, so a reader counts one refused site
#      rather than two annotations on one line of one file.
#
#  10. A step that builds a Dockerfile names one of DOCKERFILES. Two shapes are read, in a
#      workflow step and in a composite action's step alike: a `docker/build-push-action` step's
#      `file:`, and a `docker build` or `docker buildx build` in a step's `run:`, whose file is
#      the word after `--file` or `-f` or the value of a `--file=`. A file DOCKERFILES does not
#      name is refused, naming it and saying to add it there: this check reads no Dockerfile it
#      does not name, so that file's RUN lines are held to nothing, and that is how
#      .github/docker/browser.Dockerfile came to be built by CI with a bare curl and a bare
#      `apt-get update` while DOCKERFILES named three files (dispatch://INFRA-1545, round 37). A
#      build whose file this check cannot read as a path is refused by name too, since it could
#      be building any file at all: a `file:` or a `--file` written as an expression or a
#      variable, and a build naming no file, which builds its context's own `Dockerfile`. A path
#      is read from the repository's root, as every build of this tree writes it. Not read: a
#      build written inside a script a step runs (download-retry-proof.sh's `docker buildx build
#      --file "$file"`), a `docker` command with a global option before its subcommand or in any
#      other spelling, and a `working-directory:` the step runs in.
#
#      And the key that holds such a build strictly exceeds the longest single ladder of the
#      Dockerfile it builds: the step's own `timeout-minutes` where it writes one, and its job's,
#      which is GitHub's 360 minutes where the job writes none. A ladder is one `retry` call's
#      5 x <bound> + 150 s, or one curl's own ladder as rule 1 computes it, each read once
#      whatever loop or function runs it; a key at or under it is refused naming both figures.
#      The reasoning, which is why one ladder and not the sum of them (dispatch://INFRA-1545,
#      round 37): a Dockerfile build runs its fetches one after another, so a fault window on
#      one host is paid once, through the first fetch that meets it, and every later fetch
#      meets a healthy host. Two hosts down together are still one window. A fetch that
#      exhausts its ladder fails the build whatever the key is, so a key above one ladder is
#      what lets that fetch's last attempt name itself rather than GitHub's bare timeout line
#      cutting it. What it does not cover:
#      two separate fault windows in one build, on two hosts at different times, each needing a ladder of its own.
#      The comparison is the one half a check can make: the build's measured duration is no
#      figure this check can read, so the price comment above the key carries both terms, the
#      measured build and the ladder, and the key is priced at their sum. This is no change to
#      rule 8, which still sums every ladder of a workflow step's own calls and charges a
#      Dockerfile's to none. A composite action's step is read for its file and not for a key,
#      since GitHub takes none there.
#
# THE EXCEPTIONS, which are R2's four, one of them gone with the site it named, and no others.
# Each is a site this repository refuses to
# retry on purpose. Each one written out as an entry of EXEMPTIONS below must match something: an
# entry that matches nothing fails this check, so an exception cannot outlive the site it was
# written for. (b) and (d) are read from the tree rather than listed, for the reasons they give.
#
#   a. A `ghcr.io` pull of a package this repository publishes. A failed pull there is a fault of
#      this repository's own package, and it must be seen rather than retried over. R2 named two
#      sites, .github/actions/pull-test-images and the `envoy-client` job's pull; T7's
#      `download-retry-images` job added a third, the pull of the base images of the four
#      Dockerfiles, which is the same package and the same reasoning. The browser lane's visual
#      job holds two more, its pulls of the pinned Playwright copy and of its stack's Postgres
#      copy, each made before its timed browser step (dispatch://INFRA-1545, round 37).
#   b. A `uses:` reference, and anything inside a third-party action. This needs no entry: only a
#      step's `run:` is read for its fetches, and no third-party action's source is in this tree.
#      A composite action cannot wrap a `uses:` step in a loop, which is why R4 answers the Bun
#      runtime with a restored cache rather than with a retry.
#   c. Gone with its site, and the letter is kept so that R2's own lettering still reads. It was
#      the apt phase of .github/scripts/playwright-install.sh, which dispatch://INFRA-1443
#      owned: that phase had an `attempts()` loop of its own, which switched origin after two
#      attempts where the shared helper cannot, and the four calls that handed it `apt-get`
#      were exempt from rules 1, 2 and 5 together. cybermonklabs/legion#2938 moved the Dispatch
#      browser suite into an image this repository builds, which carries every host package
#      that phase installed, and deleted the script with the steps that ran it. No apt call of
#      that shape is left in the tree, so the entry that named them is gone as well: an
#      exception matching nothing is one this check refuses, which is the rule above read
#      against its own author. `playwright install-deps` stays outside the verb list, for the
#      reason that phase gave; the browser download, `playwright install`, is a site like any
#      other, and the one left in this tree is the multiprocess job's Chromium.
#   d. The credentialed publish jobs of release.yaml, release-envoy-listener.yaml and
#      release-envoy-infra.yaml, and every *.sh under .github/scripts/ such a job runs.
#      .github/scripts/check-release-credentials.sh pins the command set such a job may run, and
#      a retry wrapper there is a change to that pinned shape. A job is
#      credentialed by that check's own definition, WRITE_SCOPES: it holds `contents`, `id-token`,
#      `attestations` or `actions` at `write`, or names the deploy key. Their build jobs, which
#      hold no credential, are in scope like any other job.
#
#      The scripts are the second half, and they are read from the tree rather than listed: the
#      roots are the scripts a credentialed job's steps call, and the closure adds the scripts
#      those call in turn, which is the reading rule 8 already makes of a step. One script is in
#      that set today, .github/scripts/release-push.sh, with .github/scripts/release-bump.sh,
#      which it calls and which fetches nothing. release-push.sh's own fetches are the gh calls
#      of its `restart`, `release` and `release-section` commands — `gh run list`, `gh workflow
#      run`, `gh release view`, `gh release create`, `gh release upload` and `gh release edit` —
#      and nothing in the tree but those publish jobs runs one of them.
#
#      Retrying them means sourcing .github/scripts/ci-retry.sh into the publish job, and that is
#      what check-release-credentials.sh refuses: its rule 1(a) lets a credentialed job run no
#      dependency code, and its dependency_command() names as dependency code any command whose
#      path holds a `scripts/` component other than release-push.sh itself (`if
#      DEPENDENCY_PATH.search(path) and path != RELEASE_PUSH`). A `.` of ci-retry.sh is read
#      through that check's INTERPRETERS to exactly that path. Measured: sourcing the helper into
#      release.yaml's `publish` step 'pi-envoy: Create GitHub release' makes that check print
#      `job 'publish' holds a credential, but step '…' runs '.github/scripts/ci-retry.sh';
#      dependency code runs only in a job that holds none` and exit 1. Writing the `.` inside
#      release-push.sh instead would hide the same code from that check rather than keep it out
#      of the job, so these sites are left unretried.
#
# TWO SCOPINGS OF THE SUBJECT, which are not exceptions to it: neither site is a download of a
# tool, a toolchain or a dependency, so R1 never reached them.
#
#   * A fetch of 127.0.0.1, localhost or ::1 is a readiness poll of a process the step itself
#     started, not a download. Its loop is a wait for that process, and rule 2 leaves it alone for
#     the same reason.
#   * A call whose own failure is consumed as the answer rather than as a fault — `x=$(cmd …
#     2>/dev/null || true)`, whose result is then tested — is a probe. The one site in the tree is
#     mirror-ci-images.sh's `crane digest` of the package's own tag: that refusal is the signal to
#     copy, and five attempts there would sleep 150 s for every row the package lacks, which are
#     exactly the rows the copy after it then writes. The copy itself, and the digest read that
#     confirms it, do retry.
#
# One more scoping, which the tree states where it uses it: e2e.yaml's package step and
# ci.yaml's `Install tmux` step. An `apt-get install` with no `-d` that follows an
# `apt-get install -d`
# in the same `run:` unpacks an archive already on disk and takes no network, so the download
# before it is the half that retries. An `apt-get` carrying a simulate flag (`-s`, `--simulate`,
# `--dry-run`, `--just-print`, `--no-act`, `--recon`) reads the lists on disk and fetches nothing
# either.
#
# `--list` prints the sites this check holds to the floor, one per line, as four tab-separated
# columns: the file, the line, the verb, and the step's name where the site is a step (`-` where
# it is not), sorted by file and then by line. Nothing else goes to standard output in that mode,
# so `wc -l` is the count and `cut -f1,2` is the file and line. dispatch://INFRA-1545's production
# measurement (R5, T10) reads this list at the merged head: it is the machine-readable subject of
# "a step R1 names". A site an exception or a scoping above removes is not on the list, by the
# same reasoning that leaves it unretried.
#
# `--jobs` prints the jobs rule 8 holds to a priced key, one per line: each job that writes its own
# `timeout-minutes` and holds a retried step costing more than 0 s, as eight tab-separated
# columns — the file, the job, the key's line, its minutes, the seconds of the job's slowest
# retried step, the first and the last line (counted from 1) of the comment block that prices the
# key, `-` and `-` where no block prices it, and that step's name — sorted by file and then by the
# key's line, with the count on standard error. Over a tree with a problem it prints the problems
# and exits 1, as `--list` does, and a key no block prices is one such problem, so a run that
# prints rows prints no `-`. The harness's real-tree case reads it.
#
# Run from anywhere: .github/scripts/check-download-retry.sh
#                    .github/scripts/check-download-retry.sh --list
#                    .github/scripts/check-download-retry.sh --jobs
# CI runs it in ci.yaml's lint job, and its tests in ci.yaml's test job.
set -euo pipefail

cd "$(dirname "$0")/../.."

case "$#:${1:-}" in
  0: | 1:--list | 1:--jobs) ;;
  *)
    echo "::error file=.github/scripts/check-download-retry.sh::this check takes no argument, or \`--list\` to print the sites it holds to the floor, or \`--jobs\` to print the jobs rule 8 prices. Run \`.github/scripts/check-download-retry.sh\`, \`.github/scripts/check-download-retry.sh --list\` or \`.github/scripts/check-download-retry.sh --jobs\`." >&2
    exit 2
    ;;
esac
export DOWNLOAD_RETRY_MODE="${1:-}"

# The walk is workflow_paths.py's, beside this script in the repository it checks, which
# PYTHONPATH names: the files this check reads are the files the other gates read.
# PYTHONDONTWRITEBYTECODE keeps the import from leaving a __pycache__ in the tree.
PYTHONDONTWRITEBYTECODE=1 PYTHONPATH=.github/scripts python3 <<'PY'
import math
import os
import re
import signal
import sys
from pathlib import Path

import yaml

from workflow_paths import walk

# The mode the argument chose: "" for the check, "--list" for its sites, "--jobs" for its jobs.
MODE = os.environ.get("DOWNLOAD_RETRY_MODE")


class LineLoader(yaml.SafeLoader):
    """SafeLoader that records each mapping's line, so a problem names where to edit."""


class Mapping(dict):
    """A YAML mapping that remembers where each key was written."""

    lines: dict[str, int]


# Every key written twice in a mapping of a workflow or an action, with the line of the second
# one. GitHub refuses such a file outright and runs nothing of it - no job, and no annotation a
# reader can find - while PyYAML takes the last of the two and says nothing, so a gate reading
# the file is more permissive than the runtime that runs it. That is how a fold of
# dispatch://INFRA-1545 wrote two `timeout-minutes` into one job, passed every check this
# repository owns, and produced a run of zero jobs (42947086450).
DUPLICATE_KEYS: list[tuple[str, int]] = []
# The line of every `timeout-minutes` key of the file being loaded, in any mapping: the lines the
# priced-comment clause may read a key off. A line of text that reads like one, a line of a
# `run:` body's heredoc, is no key, and that clause refuses it by name rather than pairing a
# comment with it (dispatch://INFRA-1545, round 37).
TIMEOUT_KEY_LINES: set[int] = set()


def _mapping_with_line(loader, node):
    seen: set[str] = set()
    for key, _ in node.value:
        name = getattr(key, "value", None)
        if name is None:
            continue
        if name in seen:
            DUPLICATE_KEYS.append((name, key.start_mark.line + 1))
        if name == "timeout-minutes":
            TIMEOUT_KEY_LINES.add(key.start_mark.line + 1)
        seen.add(name)
    mapping = Mapping(loader.construct_mapping(node, deep=False))
    mapping.lines = {
        key.value: key.start_mark.line + 1 for key, _ in node.value if hasattr(key, "value")
    }
    return mapping


LineLoader.add_constructor(yaml.resolver.BaseResolver.DEFAULT_MAPPING_TAG, _mapping_with_line)

# The four Dockerfiles a CI job builds. Named rather than discovered: a Dockerfile elsewhere in
# the tree is not built by CI, and a rename of one of these must fail here rather than pass by
# matching nothing. A step's build of a Dockerfile no entry here names is refused (rule 10, which
# says the shapes it reads): the fourth, the browser image's, came with main's #2938 while this
# list named three, and nothing said so (dispatch://INFRA-1545, round 37).
DOCKERFILES = (
    ".github/docker/browser.Dockerfile",
    "packages/daemon/docker/worker.Dockerfile",
    "packages/envoy/docker/Dockerfile",
    "packages/envoy/infra/host/heldportwrapper/Dockerfile",
)
WORKFLOWS = Path(".github/workflows")
ACTIONS = Path(".github/actions")
SCRIPTS = Path(".github/scripts")
HELPER = ".github/scripts/ci-retry.sh"
# This file, named by the problems it raises about its own lists.
SELF = ".github/scripts/check-download-retry.sh"
# The `.test.sh` harnesses beside the checks are not read. A harness is not a job's download: the
# two network calls in the tree today are ci-fault-proxy.test.sh's `run_curl`, which drives one
# curl at the fault proxy precisely to watch a single unretried attempt, and
# release-push.test.sh's `gh api meta`, which compares the pinned host keys with what GitHub
# publishes and prints SKIP when the read fails. A retry at either would destroy what the test
# measures, and neither brings a tool, a toolchain or a dependency into a job.
#
# The widened gh table above does not add a third. The other gh words a harness writes reach no
# host: release-push.test.sh and check-private-names.test.sh each write a stub `gh` into the
# test's own bin directory and put that directory first on PATH, so the words are the stub's own
# `case` arms and the calls the script under test makes land on it; check-ci-filters.test.sh's
# `gh run cancel` is a line of a workflow fixture it writes for the checker to read, which
# nothing runs. A retry around any of them would wrap a shell function or a string.
TEST_HARNESS = ".test.sh"
# What names a script: a word holding `/<name>.sh`, wherever on the line it sits —
# `.github/scripts/x.sh`, `"$script_dir/x.sh"`, `"$GITHUB_ACTION_PATH/../../scripts/x.sh"`,
# `tag=$(.github/scripts/x.sh)`, `. .github/scripts/ci-retry.sh`. Only the name is read, and only
# a name SCRIPTS holds is followed, SCRIPTS being the names under .github/scripts/ alone. The
# path a call writes in front of the name is one of half a dozen ways of reaching the same file,
# so reading the name rather than the path is what makes those spellings one script. A match is
# an occurrence and not yet a call: call_of below reads where on its line it is written, and
# only a path in a recognised call's place is charged (dispatch://INFRA-1545, round 37). WHAT
# THIS DOES NOT CLAIM IS COVERAGE OF EVERY SCRIPT CI RUNS: a CI job also runs scripts outside that
# directory, and four of them carry retried fetches this change wrote -
# scripts/go-third-party-notices.sh, scripts/e2e/dispatch-session-settings.sh,
# scripts/e2e/lib/live-daemon-proof.sh and packages/envoy/loadtest/cluster/render-nats.sh. A
# call of one of those is a name SCRIPTS does not hold, so this reader does not follow it and
# the step that runs it is priced by the step's own text. That reach is the one this file's
# header describes, and widening it is dispatch://INFRA-2899's question rather than a claim to
# be made here (review of dispatch://INFRA-1545 caught this sentence claiming the whole). A `.test.sh` harness matches nothing here, since the name this reads holds no dot.
SCRIPT_CALL = re.compile(r"/([a-z0-9-]+\.sh)")
# The three things an occurrence of a script's path can be, and the two kinds of the first
# (dispatch://INFRA-1545, round 37). A RUN is charged once for every time the body runs it; a
# library a body SOURCES is charged once for the step reaching it; a recognised non-call is
# charged nothing and refused nothing; and an occurrence in any other place is refused by name
# where the script it names fetches. The header's rule 8 reading writes out which places are
# which.
CALL_RUN = "run"
CALL_SOURCED = "sourced"
CALL_NONE = "non-call"
CALL_UNREAD = "unread"
# The commands whose arguments are text a step prints or searches and never runs, the two this
# tree writes a script's path after: a path written as an argument of one of them is a
# recognised non-call, an `::error` message included, unless that command's output is piped into
# a shell, which runs the text it prints and is read as any other pipe into a shell is
# (dispatch://INFRA-1545, round 37). What it prints into a file or a variable is data there, and
# a later run of that file or that variable is refused where it runs (unnamed_runs).
NONCALL_COMMANDS = {"echo", "grep"}
# The canonical inline retry of a Dockerfile RUN, already in the shape the comparison uses: its
# continuations joined and each run of blank space one space.
CANONICAL = (
    'retry() { b=$1; what=$2; shift 2; i=1; while :; do timeout "$b" "$@" && return 0; s=$?; '
    '[ "$i" -ge 5 ] && { echo "$what failed after $i attempts (last exit $s)" >&2; return "$s"; }; '
    'sleep "$(( 10 << (i - 1) ))"; i=$((i + 1)); done; }'
)
RETRY_FLOOR = 7
ATTEMPT_FLOOR = 5
# R1's floor on the ladder itself: the last attempt starts at least this many seconds after the
# first. The count alone does not say it, because `--retry-delay` and `--retry-max-time` each
# rewrite the waits between curl's own attempts (curl_ladder below).
LADDER_FLOOR = 120
# The wait curl takes before its first retry, and the ceiling its doubling reaches, read in
# curl's documentation at curl 8.14.1 (docs/cmdline-opts/retry.md): "it first waits one second
# and then for all forthcoming retries it doubles the waiting time until it reaches 10 minutes".
CURL_FIRST_WAIT = 1
CURL_WAIT_CEILING = 600
# Rule 8's arithmetic, the worst case of one retried call of the shared helper: it sleeps 10,
# 20, 40 and 80 seconds between its five attempts. The total curl waits between its own attempts
# is no constant, because `--retry-delay` and `--retry-max-time` each rewrite those waits:
# curl_waits reads those options off the call, and curl_ladder computes the ladder from them.
HELPER_WAIT = 150
# What a job allows a step when the job writes no `timeout-minutes` of its own, read off
# GitHub's own documentation ("Workflow syntax for GitHub Actions", `jobs.<job_id>.timeout-
# minutes`): six hours. Rule 8 holds a step's arithmetic under its job's budget as well as
# under its own, and this is the budget a job that writes none is holding it under.
JOB_DEFAULT_MINUTES = 360
# What a `timeout` argument's suffix multiplies its number by, as timeout(1) reads it.
SUFFIX = {"": 1, "s": 1, "m": 60, "h": 3600, "d": 86400}
# A curl `--max-time` value that is no limit at all: `0`, in any spelling curl reads as zero
# seconds (dispatch://INFRA-1545, round 37).
ZERO_SECONDS = re.compile(r"0*\.?0*")
# The one Python function a `urllib.request` read of a script under .github/scripts/ goes through,
# and the one file that defines it.
PYTHON_READER = "read_retrying"
PYTHON_READER_FILE = ".github/scripts/check-ci-images.sh"
PYTHON_READ = re.compile(r"urllib\.request\.urlopen\s*\(|(?<![\w.])urlopen\s*\(")
PYTHON_DEF = re.compile(r"^(\s*)def\s+(\w+)\s*\(")

LOOPBACK = ("127.0.0.1", "localhost", "::1")
# What a word sheds to become the name of the program it runs.
PROGRAM = re.compile(r"^(\$\{?GITHUB_WORKSPACE\}?/)?(\.\.?/)*")
ASSIGNMENT = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*\+?=")
EXPRESSION = re.compile(r"\$\{\{.*?\}\}")
FUNCTION_WORD = re.compile(r"^[A-Za-z_][\w:.-]*\(\)$")
# An empty `()` with blanks before or inside it, `name ()` or `name( )`: bash takes it as the `()`
# of a function's name, a spelling this check refuses by name rather than reads (Scanner's
# blank_parens; dispatch://INFRA-1545, round 37).
FUNCTION_PARENS = re.compile(r"\([ \t]*\)")
# A heredoc operator and its terminator word, read where the Scanner finds a `<<` outside quotes
# and comments (heredocs_of): the word is everything up to a blank or an operator, quotes and
# backslashes included, which heredocs_of removes as bash does (dispatch://INFRA-1545, round 37).
HEREDOC = re.compile(r"<<-?[ \t]*((?:[^\s;&|<>()'\"\\]|\\.|'[^']*'|\"[^\"]*\")+)")
# The consumers a heredoc of this tree is fed to, each of which takes the body as data: Python's
# program, a `cat` writing it out and a `read` taking its words. A heredoc fed to anything else
# is refused by name where its body fetches (unread_heredocs; dispatch://INFRA-1545, round 37).
HEREDOC_DATA = {"python3", "cat", "read"}
# A command word that is one parameter's value: `$fn`, `${fn}`, `$1`, `"$@"`, `"${@}"` and an
# array's `"${cmd[@]}"`, quoted or not. A call of a function through one is refused by name where
# it can hide a fetch (variable_calls; dispatch://INFRA-1545, round 37). Until round 37 it read a
# name or a digit alone, so `"$@"` and `"${cmd[@]}"` ran a fetching function no reading charged.
VARIABLE_CALL = re.compile(r"\$(?:\{[^{}]*\}|[A-Za-z_]\w*|\d|[@*#?$!-])")
# A word of a `for` list that is itself and nothing else: no expansion, no glob, no quoting. A
# list of these is the one pass count rule 8 reads off a loop (passes_of below).
LITERAL_WORD = re.compile(r"[A-Za-z0-9_@%+:,./=-]+")
# Transparent in command position, taking no argument of their own.
PASSTHROUGH = {"sudo", "exec", "command", "builtin", "nice", "ionice", "nohup", "time", "env",
               "stdbuf", "setsid", "doas", "xvfb-run"}
# Transparent, but the command they run is inside the script string behind `-c`, which rule 8
# reads in one form, `bash -c '<text>'` (sites_of_command).
INTERPRETERS = {"bash", "sh", "dash", "zsh"}
# Transparent, and the verb is what they run: a package runner resolves a binary the workspace's
# own install already provides.
RUNNERS = {"bunx", "npx", "pnpx", "pnpm-dlx"}
KEYWORDS = {"if", "then", "else", "elif", "fi", "do", "done", "while", "until", "for", "in",
            "case", "esac", "select", "function", "!", "[[", "]]", "{", "}"}
LOOP_WORDS = {"while", "until", "for", "select"}
# The reserved words after which bash still reads a `{` or a `}` as a group's brace: a brace is a
# reserved word only where it opens a command or follows these on it, as in `fi }` and `then {`.
# One written anywhere else is a word of its command, which Scanner's odd_braces names
# (dispatch://INFRA-1545, round 37).
BRACE_OPENERS = {"if", "then", "else", "elif", "fi", "do", "done", "while", "until", "esac", "!",
                 "{", "}", "time"}
APT_SUBCOMMANDS = {"update", "install", "download", "upgrade", "dist-upgrade", "full-upgrade",
                   "source", "build-dep", "reinstall"}
APT_SIMULATE = {"-s", "--simulate", "--just-print", "--dry-run", "--no-act", "--recon"}
APT_DOWNLOAD_ONLY = {"-d", "--download-only"}
# What an `apt-get update` option has to say for rule 5 to be satisfied, the value of an `-o`.
APT_ERROR_MODE = re.compile(r"apt::update::error-mode\s*=\s*any", re.IGNORECASE)
# A command that answers about itself reads nothing from the network, whatever its verb.
INFORMATIONAL = {"--version", "--help", "--usage"}
# The mise subcommands that fetch and say so: both exit 1 when the fetch fails.
MISE_SUBCOMMANDS = {"install", "use"}
# Rule 7's four: each exits 0 when it cannot resolve the tool, so none of them is a fetch verb
# and a retry around one protects nothing. The check reads them only to refuse that retry.
MISE_SILENT = {"x", "exec", "up", "upgrade"}
CRANE_SUBCOMMANDS = {"copy", "cp", "digest", "config", "pull", "export", "manifest", "blob"}
UV_SUBCOMMANDS = {"sync", "add", "pip", "tool", "python", "lock", "export"}
NPM_SUBCOMMANDS = {"install", "ci", "i", "install-test", "add"}
BUN_SUBCOMMANDS = {"install", "add", "i"}
# gh. Every subcommand of it but these reaches api.github.com over HTTPS, so the table is written
# the other way round from the rest: what is named here is what fetches nothing, and anything
# else gh runs is a fetch. GH_LOCAL answers out of the local config, the local extension
# directory or the binary itself: `gh config get`, `gh alias list`, `gh completion`, `gh
# extension list`, `gh help` and `gh version`. `gh auth` is half of each — `git-credential`,
# `setup-git`, `switch` and `token` read the stored token or write the local git config, while
# `login`, `logout`, `refresh` and `status` talk to the host — so its local actions are named
# apart. GH_GROUPS is the set of subcommands that take an action word of their own, which is
# gh's own grouping (`gh run list`, `gh release view`, `gh cache delete`): a verb of one of them
# is named with that action, and `gh api`, `gh browse` and `gh status`, which take an argument
# instead, are named by the subcommand alone.
GH_LOCAL = {"alias", "completion", "config", "extension", "help", "version"}
GH_AUTH_LOCAL = {"git-credential", "setup-git", "switch", "token"}
GH_GROUPS = {"accessibility", "attestation", "auth", "cache", "codespace", "gist", "gpg-key",
             "issue", "label", "org", "preview", "project", "release", "repo", "ruleset", "run",
             "search", "secret", "ssh-key", "variable", "workflow"}
# The write scopes check-release-credentials.sh counts as a credential (its WRITE_SCOPES), and the
# deploy key it reads beside them.
WRITE_SCOPES = ("contents", "id-token", "attestations", "actions")
DEPLOY_KEY = re.compile(r"secrets\s*(\.\s*DEPLOY_KEY\b|\[\s*['\"]DEPLOY_KEY['\"]\s*\])",
                        re.IGNORECASE)
RELEASE_WORKFLOWS = (
    ".github/workflows/release.yaml",
    ".github/workflows/release-envoy-listener.yaml",
    ".github/workflows/release-envoy-infra.yaml",
)

problems: list[str] = []


def problem(path: str, line: int, message: str) -> None:
    problems.append(f"::error file={path},line={line}::{message}")


# ---------------------------------------------------------------- reading the shell


def heredocs_of(text: str) -> list[tuple[int, re.Match, list[str], bool]]:
    """Each heredoc of a shell body, as the line of its operator, counted from 0, the operator
    with its terminator word, matched on that line, the lines of its body, and whether any part
    of that word is quoted, which keeps the body from expansion. An operator is a `<<` the
    Scanner reads outside quotes and comments, fed the text a line at a time so that a string
    spanning lines is read as one, and a body's own lines are not read for one. Until round 37
    every `<<` on a line was an operator, so a `<<` inside a quoted word, such as the `echo
    "files<<FILES"` a step writes to $GITHUB_OUTPUT, opened a body that no line closes, and every
    line after it was read as data by every rule (dispatch://INFRA-1545, round 37).

    The terminator is the whole word after the operator with its quotes and backslashes removed,
    as bash reads it: a word written `\\EOF`, `"E"OF` or `E'OF'` ends the body at a line `EOF`,
    and keeps it from expansion. Until round 37 the word was read as a name in one pair of quotes,
    so a word written `\\EOF` opened no heredoc and its body was read as shell, and one written
    `"E"OF` ended at a line `E`, which blanked every line after it (dispatch://INFRA-1545, round
    37)."""
    found = []
    lines = text.split("\n")
    scanner = Scanner(1)
    index = 0
    while index < len(lines):
        number = index
        index += 1
        scanner.operators = []
        scanner.scan(lines[number] + "\n")
        for start in scanner.operators:
            match = HEREDOC.match(lines[number], start)
            if match is None:
                continue
            terminator = re.sub(r"\\(.)|'([^']*)'|\"([^\"]*)\"",
                                lambda part: "".join(filter(None, part.groups())), match.group(1))
            body = []
            while index < len(lines) and lines[index].strip() != terminator:
                body.append(lines[index])
                index += 1
            index += 1
            found.append((number, match, body, re.search(r"['\"\\]", match.group(1)) is not None))
    return found


def without_heredocs(text: str) -> tuple[str, list[tuple[int, str]]]:
    """The text with every heredoc body blanked, and those bodies as (line, text) pairs, the
    line counted from 1. A body is data to every rule reading the shell, which it is for the
    consumers this tree feeds one to, HEREDOC_DATA: the Python a `python3 <<'PY'` runs is read by
    rule 6 instead, off the bodies heredocs_of reads, and a heredoc fed to anything else is
    refused by name where its body fetches (unread_heredocs). Until round 37 a body fed to a
    shell was read as the step's own commands, with a model of the shell's options in front of
    the operator, and no step of this tree feeds one to a shell (dispatch://INFRA-1545, round
    37)."""
    lines = text.split("\n")
    bodies: list[tuple[int, str]] = []
    for number, _match, body, _quoted in heredocs_of(text):
        for offset, body_line in enumerate(body):
            lines[number + 1 + offset] = ""
            bodies.append((number + 2 + offset, body_line))
    return "\n".join(lines), bodies


def unread_heredocs(text: str) -> list[tuple[int, str, str]]:
    """Each heredoc of this body that something may run while this check reads its body as
    data, as the line of its operator, counted from 0, its body, and what its refusal says. Such
    a heredoc is refused by name where its body holds a fetch or names a script that fetches
    (dispatch://INFRA-1545, round 37). Three shapes: a consumer that is not one of HEREDOC_DATA,
    read off the operator's line as the last command in front of it once the wrappers
    reduce_words strips are gone, a shell, `"$SHELL"` or `ssh host bash` among them; a command
    whose output a pipe on the operator's line hands to a shell, a `cat` piped into `bash`; and
    an unquoted body holding a command substitution, which the step's own shell runs before any
    consumer reads the body."""
    found = []
    for number, match, body, quoted in heredocs_of(text):
        commands = Scanner(1).scan(match.string[:match.start()])
        words = reduce_words(commands[-1].words)[0] if commands else []
        if not words or PROGRAM.sub("", words[0]).rsplit("/", 1)[-1] not in HEREDOC_DATA:
            sentence = HEREDOC_UNREAD.format(
                consumer=f"`{words[0]}`" if words else "no command",
                cure="Write the body's commands in the step itself")
        elif re.search(r"\|&?\s*(?:\S*/)?(?:bash|sh|dash|zsh)\b", match.string[match.end():]):
            sentence = HEREDOC_UNREAD.format(
                consumer=f"`{words[0]}`, whose output a pipe on the operator's line hands to a "
                         "shell",
                cure="Write the body's commands in the step itself, rather than a printed copy "
                     "of them")
        elif not quoted and re.search(r"\$\(|`", "\n".join(body)):
            sentence = HEREDOC_UNREAD.format(
                consumer=f"`{words[0]}` unquoted, and the body holds a command substitution, "
                         "which the step's own shell runs before that command reads the body",
                cure="Quote the terminator word, and run each command the substitution ran on "
                     "its own before the heredoc")
        else:
            continue
        found.append((number, "\n".join(body), sentence))
    return found


def probe_spans(text: str) -> list[tuple[int, int]]:
    """The character spans of each `x=$(… )` whose failure is consumed as the answer: its own
    error silenced and `|| true` inside the substitution or right after it."""
    spans = []
    for match in re.finditer(r"[A-Za-z_][A-Za-z0-9_]*=\$\(", text):
        depth = 1
        index = match.end()
        while index < len(text) and depth:
            if text[index] == "(":
                depth += 1
            elif text[index] == ")":
                depth -= 1
            index += 1
        inner = text[match.end():index - 1]
        consumed = re.search(r"\|\|\s*true\s*$", inner.strip()) or re.match(
            r"\s*\|\|\s*true\b", text[index:index + 40]
        )
        if consumed and ("2>/dev/null" in inner or "2>&1" in inner):
            spans.append((match.start(), index))
    return spans


class Command:
    """One simple command of a shell body: its words, the line it starts on, the character offset
    it starts at, the loops it sits in — all of them, outermost first, with the innermost one
    beside them — and whether a `|` is what ended it, which says its status is not the
    pipeline's and so is rule 9's question. `quoted` says, for each word the Scanner read, whether
    a quote or a backslash was written in it, which bash reads as no reserved word
    (dispatch://INFRA-1545, round 37)."""

    def __init__(self, words: list[str], line: int, offset: int) -> None:
        self.words = words
        self.line = line
        self.offset = offset
        self.loops: list[int] = []
        self.loop: int | None = None
        self.piped = False
        self.quoted: list[bool] = []


class Scanner:
    """One quote-aware pass over a shell body, collecting each simple command with its line. The
    shape is .github/scripts/check-release-credentials.sh's Scanner, which reads a `run:` the same
    way; this one keeps the keywords, so a loop and a function definition are visible, and carries
    each command's line and offset, so a problem names where to edit. `blank_parens` holds the
    line of each empty `()` with blanks before or inside it, `name ()` or `name( )`, which bash
    reads as a function's name and this check refuses by name rather than reads
    (dispatch://INFRA-1545, round 37). `operators` holds the offset of each `<<` it reads outside
    quotes and comments, which is a heredoc operator, and `quote` is the quote still open when a
    scan ends, so a body fed to it one line at a time is read as one text (heredocs_of;
    dispatch://INFRA-1545, round 37). `odd_braces` holds each `{` or `}` word it reads as a
    group's brace where bash reads a word of the command instead, as (line, the brace): one
    written in quotes or after a backslash, or after a word of its command that is no reserved
    word, such as `echo '}'` (dispatch://INFRA-1545, round 37). `braces` is how many `${` are
    open around what it reads: a parameter expansion is one word to its closing `}`, as bash
    reads `${name##* }` and `${name:-$(cmd)}`, and `expansion` marks a word whose `}` closed one,
    which is no group's brace (dispatch://INFRA-1545, round 37)."""

    def __init__(self, first_line: int) -> None:
        self.commands: list[Command] = []
        self.words: list[str] = []
        self.word = ""
        self.started = False
        self.at_word_start = True
        self.redirect_target = False
        self.line = first_line
        self.word_line = first_line
        self.word_offset = 0
        self.command_line = first_line
        self.command_offset = 0
        self.open_command = False
        # The substitutions and subshells open here, innermost last.
        self.open_subs: list[tuple] = []
        self.blank_parens: list[int] = []
        self.operators: list[int] = []
        self.quote: str | None = None
        # Whether a quote or a backslash was written in the word being read, and the same for
        # each word of the command being read.
        self.quoted = False
        self.word_quoted: list[bool] = []
        self.odd_braces: list[tuple[int, str]] = []
        self.braces = 0
        self.expansion = False

    def add(self, char: str, offset: int) -> None:
        if not self.started:
            self.word_line = self.line
            self.word_offset = offset
        if not self.open_command:
            self.command_line = self.line
            self.command_offset = offset
            self.open_command = True
        self.word += char
        self.started = True
        self.at_word_start = False

    def end_word(self) -> None:
        brace = False
        if self.started and not self.redirect_target:
            self.words.append(self.word)
            self.word_quoted.append(self.quoted)
            # `{` and `}` are reserved words, so `fetch() { curl …; }` holds a command of its own
            # after the brace: without this the curl would read as an argument of `fetch()`. A
            # `}` that closes a parameter expansion is the end of that word, and no brace.
            brace = self.word in ("{", "}") and not self.expansion
            # Bash reads one as a brace only unquoted and where every word before it in its
            # command is a reserved word, so `fi }` closes a group and `echo '}'` prints a `}`.
            # This reading takes every one for a brace, and the others are refused by name where
            # a fetch is written in the text; a `function name {` is refused as that shape
            # already (dispatch://INFRA-1545, round 37).
            before = self.words[:-1]
            if brace and "function" not in before and (
                    self.quoted or any(word not in BRACE_OPENERS for word in before)):
                self.odd_braces.append((self.word_line, self.word))
        if self.started:
            self.redirect_target = False
        self.word = ""
        self.started = False
        self.quoted = False
        self.expansion = False
        self.at_word_start = True
        if brace:
            self.end_command()

    def end_command(self, piped: bool = False) -> None:
        self.end_word()
        if self.words:
            command = Command(self.words, self.command_line, self.command_offset)
            command.piped = piped
            command.quoted = self.word_quoted
            self.commands.append(command)
        self.words = []
        self.word_quoted = []
        self.redirect_target = False
        self.open_command = False

    def name_function(self, i: int) -> None:
        """`name()`: its parentheses are part of the name's word, not a subshell, and they end its
        command, so the `{` after them is a command of its own however it is spaced."""
        self.add("(", i)
        self.add(")", i)
        self.end_command()

    def scan(self, text: str) -> list[Command]:
        quote = self.quote
        open_subs = self.open_subs
        i = 0
        while i < len(text):
            char = text[i]
            after = text[i + 1:i + 2]
            if char == "\n":
                self.line += 1
            if quote == "'":
                if char == "'":
                    quote = None
                else:
                    self.add(char, i)
            elif char == "`":
                self.end_command()
                if open_subs and open_subs[-1][0] == "tick":
                    _kind, quote, self.braces = open_subs.pop()
                else:
                    open_subs.append(("tick", quote, self.braces))
                    quote = None
                    self.braces = 0
            elif char == "$" and after == "(":
                self.add(char, i)
                self.end_command()
                open_subs.append(("paren", quote, self.braces))
                quote = None
                self.braces = 0
                i += 1
            elif quote == '"':
                if char == '"':
                    quote = None
                elif char == "\\" and after in ("$", "`", '"', "\\"):
                    self.add(after, i)
                    i += 1
                elif char == "\\" and after == "\n":
                    self.line += 1
                    i += 1
                else:
                    self.add(char, i)
            elif char == "$" and after == "{":
                # A parameter expansion is one word to its closing `}`, blanks and operators
                # included, as bash reads `${name##* }`: until round 37 this reading split it at
                # its blank and took its `}` for a group's brace (dispatch://INFRA-1545, round
                # 37). A `$( … )` or a quote inside it is read as anywhere else.
                self.add(char, i)
                self.add(after, i + 1)
                self.braces += 1
                i += 1
            elif self.braces and char in "{}":
                self.add(char, i)
                self.braces += 1 if char == "{" else -1
                self.expansion = True
            elif self.braces and char in " \t\n;&|<>()":
                self.add(char, i)
            elif char == "#" and self.at_word_start:
                end = text.find("\n", i)
                i = len(text) if end < 0 else end
                continue
            elif char == "\\" and after == "\n":
                self.line += 1
                i += 1
            elif char == "\\" and after:
                self.add(after, i)
                self.quoted = True
                i += 1
            elif char in "'\"":
                quote = char
                if not self.started:
                    self.word_line = self.line
                self.started = True
                self.quoted = True
                self.at_word_start = False
                if not self.open_command:
                    self.command_line = self.line
                    self.command_offset = i
                    self.open_command = True
            elif char == "(" and after == ")" and self.started:
                self.name_function(i)
                i += 1
            elif char == "(" or (char in "<>" and after == "("):
                if char != "(":
                    i += 1
                elif FUNCTION_PARENS.match(text, i) and not ASSIGNMENT.fullmatch(self.word):
                    self.blank_parens.append(self.line)
                self.end_command()
                open_subs.append(("paren", None, 0))
            elif char == ")":
                self.end_command()
                if open_subs and open_subs[-1][0] == "paren":
                    _kind, quote, self.braces = open_subs.pop()
            elif char in "<>":
                if text[i:i + 2] == "<<" and text[i + 2:i + 3] != "<":
                    self.operators.append(i)
                self.end_word()
                self.redirect_target = True
                while text[i + 1:i + 2] in ("<", ">", "&", "|"):
                    i += 1
            elif char in ";&|\n":
                # A `|` that ends this command pipes its output into the next one, and the
                # pipeline's status is that next one's where pipefail is off (rule 9). `|&`
                # pipes standard error with it and is as much a pipeline; the `&` of it then
                # ends a command with no words, which adds none. `||` is a logical OR and no
                # pipeline at all, and its second `|` ends a wordless command the same way.
                self.end_command(char == "|" and after != "|")
            elif char in " \t":
                self.end_word()
            else:
                self.add(char, i)
            i += 1
        self.end_command()
        self.quote = quote
        return self.commands


class CallScanner(Scanner):
    """The Scanner above, read for the one question of where a script's path is written
    (dispatch://INFRA-1545, round 37). It keeps the character span of every word, so an
    occurrence of a path is found in the word holding it; the substitutions open around each
    command, so a call inside a `$(…)` or a process substitution is told apart from one at top
    level; and the targets of redirects. It reads two shapes the Scanner reads otherwise, and
    which the rules reading sites do not need: a `$(…)` or a backtick substitution keeps the
    command it is written in, which the shell does — the Scanner ends that command at the `$(`,
    so `x=$(.github/scripts/x.sh)` and `. "$(dirname "$0")/x.sh"` lose the word around them —
    and `name=( … )` is an array, whose elements the Scanner reads as the commands of a
    subshell. A command's `context` is the substitutions open around it, innermost last: "dollar"
    for `$(…)`, "tick" for backticks, "paren" for a subshell, "proc" for `<(…)` or `>(…)` and
    "array" for an array's elements.

    It reads two more things as bash reads them, which the rules reading sites do not need
    (dispatch://INFRA-1545, round 37). Where each comment runs, in `comments`: from a `#` that
    starts a word outside quotes to the end of its physical line, which a backslash there does
    not continue, so `true;# …` and `(true)# …` each end in one, and a line starting with `#`
    inside a string that spans lines is part of that string. And which newlines a backslash
    continues, in `continued`. A `[[ … ]]`, an extglob pattern and a `case` pattern are not
    modelled: their `(`, `)`, `|`, `<` and `>` read as the shell's own operators, which the
    header's rule 8 reading lists with what each makes of a path written there. Each command
    keeps which of its words a quote or a backslash was written in, as the Scanner's do, which
    is how a quoted `[[` is told from the test's own (variable_calls; dispatch://INFRA-1545,
    round 37); and a parameter expansion is one word, as the Scanner reads it. And each
    command's `writes` holds the word each of its redirects of output names, a file the text
    writes, `written` those of a redirect written where no command is, `} > file` after a group,
    and each command's `input` is what its last redirect of standard input reads: `<<<` a
    here-string, `<<` a heredoc, `<` a file and `<(` a process substitution, or ""
    (written_files, unnamed_runs; dispatch://INFRA-1545, round 37)."""

    # What a substitution saves of the command it opens inside, and restores when it closes.
    SAVED = ("words", "spans", "word", "chars", "started", "at_word_start", "redirect_target",
             "open_command", "command_line", "command_offset", "word_quoted", "quoted", "braces",
             "expansion", "redirect_out", "command_input", "command_writes")

    def __init__(self, first_line: int) -> None:
        super().__init__(first_line)
        self.chars: list[int] = []
        self.spans: list[tuple[int, int] | None] = []
        self.redirects: list[tuple[int, int]] = []
        self.restored_quote: str | None = None
        self.comments: list[tuple[int, int]] = []
        self.continued: set[int] = set()
        self.written: list[str] = []
        # The files the redirects of the command being read write.
        self.command_writes: list[str] = []
        # Whether the redirect read last writes its target, a `>`, rather than reads it.
        self.redirect_out = False
        # What the command being read takes its standard input from, as `input` says.
        self.command_input = ""

    def add(self, char: str, offset: int) -> None:
        super().add(char, offset)
        self.chars.append(offset)

    def end_word(self) -> None:
        brace = False
        span = (min(self.chars), max(self.chars)) if self.chars else None
        if self.started and not self.redirect_target:
            self.words.append(self.word)
            self.spans.append(span)
            self.word_quoted.append(self.quoted)
            brace = self.word in ("{", "}") and not self.expansion
        elif self.started and self.redirect_target and span:
            self.redirects.append(span)
            if self.redirect_out:
                self.command_writes.append(self.word)
        if self.started:
            self.redirect_target = False
        self.word = ""
        self.started = False
        self.quoted = False
        self.expansion = False
        self.at_word_start = True
        self.chars = []
        if brace:
            self.end_command()

    def end_command(self, piped: bool = False) -> None:
        self.end_word()
        if self.words:
            command = Command(self.words, self.command_line, self.command_offset)
            command.piped = piped
            command.spans = self.spans
            command.quoted = self.word_quoted
            command.context = tuple(entry[0] for entry in self.open_subs)
            command.input = self.command_input
            command.writes = self.command_writes
            self.commands.append(command)
        else:
            self.written += self.command_writes
        self.words = []
        self.spans = []
        self.word_quoted = []
        self.redirect_target = False
        self.open_command = False
        self.command_input = ""
        self.command_writes = []

    def open_sub(self) -> dict:
        """The outer command, saved mid-word: the text of a `$(…)` or a backtick substitution is
        part of the word it is written in, and the commands inside it are commands of their own."""
        saved = {name: list(getattr(self, name)) if isinstance(getattr(self, name), list)
                 else getattr(self, name) for name in self.SAVED}
        if not self.open_command:
            saved.update(open_command=True, command_line=self.line, command_offset=None)
        self.words, self.spans, self.word, self.chars = [], [], "", []
        self.word_quoted, self.quoted = [], False
        self.braces, self.expansion = 0, False
        self.started, self.at_word_start, self.redirect_target = False, True, False
        self.open_command, self.command_input, self.command_writes = False, "", []
        return saved

    def close_sub(self, text: str, i: int) -> None:
        self.end_command()
        _kind, quote, saved, start = self.open_subs.pop()
        for name, value in saved.items():
            setattr(self, name, value)
        if self.command_offset is None:
            self.command_offset = start
        self.word += text[start:i + 1]
        self.chars += [start, i]
        self.started = True
        self.at_word_start = False
        self.restored_quote = quote

    def scan(self, text: str) -> list[Command]:
        quote = None
        open_subs = self.open_subs
        i = 0
        while i < len(text):
            char = text[i]
            after = text[i + 1:i + 2]
            if char == "\n":
                self.line += 1
            if quote == "'":
                if char == "'":
                    quote = None
                else:
                    self.add(char, i)
            elif char == "`":
                if open_subs and open_subs[-1][0] == "tick":
                    self.close_sub(text, i)
                    quote = self.restored_quote
                else:
                    open_subs.append(("tick", quote, self.open_sub(), i))
                    quote = None
            elif char == "$" and after == "(":
                open_subs.append(("dollar", quote, self.open_sub(), i))
                quote = None
                i += 1
            elif quote == '"':
                if char == '"':
                    quote = None
                elif char == "\\" and after in ("$", "`", '"', "\\"):
                    self.add(after, i)
                    i += 1
                elif char == "\\" and after == "\n":
                    self.line += 1
                    self.continued.add(i + 1)
                    i += 1
                else:
                    self.add(char, i)
            elif char == "$" and after == "{":
                self.add(char, i)
                self.add(after, i + 1)
                self.braces += 1
                i += 1
            elif self.braces and char in "{}":
                self.add(char, i)
                self.braces += 1 if char == "{" else -1
                self.expansion = True
            elif self.braces and char in " \t\n;&|<>()":
                self.add(char, i)
            elif char == "#" and self.at_word_start:
                end = text.find("\n", i)
                end = len(text) if end < 0 else end
                self.comments.append((i, end))
                i = end
                continue
            elif char == "\\" and after == "\n":
                self.line += 1
                self.continued.add(i + 1)
                i += 1
            elif char == "\\" and after:
                self.add(after, i)
                self.quoted = True
                i += 1
            elif char in "'\"":
                quote = char
                self.started = True
                self.quoted = True
                self.at_word_start = False
                if not self.open_command:
                    self.command_line = self.line
                    self.command_offset = i
                    self.open_command = True
            elif char == "(" and after == ")" and self.started:
                self.name_function(i)
                i += 1
            elif char == "(" or (char in "<>" and after == "("):
                kind = "paren" if char == "(" else "proc"
                if char == "(" and self.started and ASSIGNMENT.fullmatch(self.word):
                    kind = "array"
                if char != "(":
                    if char == "<" and (self.words or self.started):
                        self.command_input = "<("
                    i += 1
                self.end_command()
                open_subs.append((kind, None))
            elif char == ")":
                if open_subs and open_subs[-1][0] == "dollar":
                    self.close_sub(text, i)
                    quote = self.restored_quote
                else:
                    self.end_command()
                    if open_subs and open_subs[-1][0] in ("paren", "proc", "array"):
                        quote = open_subs.pop()[1]
            elif char in "<>":
                self.end_word()
                self.redirect_out = char == ">"
                if char == "<" and text[i + 1:i + 2] not in ("&", ">"):
                    self.command_input = ("<<<" if text[i:i + 3] == "<<<"
                                          else "<<" if text[i:i + 2] == "<<" else "<")
                self.redirect_target = True
                while text[i + 1:i + 2] in ("<", ">", "&", "|"):
                    i += 1
            elif char in ";&|\n":
                self.end_command(char == "|" and after != "|")
            elif char in " \t":
                self.end_word()
            else:
                self.add(char, i)
            i += 1
        self.end_command()
        return self.commands


class Loops:
    """What rule 8 reads of one shell body: each loop by its own line, with the line its `done`
    sits on, the passes it makes and the seconds it waits between two of them; the line of the
    loop in each function that holds one — the loop a call of that function reaches; and where
    each function this body defines is written and where it is called, which is how many times
    a call inside it runs. A pass count is None where the loop does not write one."""

    def __init__(self) -> None:
        self.function_loop: dict[str, int] = {}
        self.passes: dict[int, int | None] = {}
        self.sleep: dict[int, int] = {}
        self.extent: dict[int, int] = {}
        self.function_span: dict[str, tuple[int, int]] = {}
        self.function_calls: dict[str, list[int]] = {}
        # The function definitions this check refuses by name rather than reads, as (line, the
        # last line its body can reach or None, shape): function_bodies's and Scanner's
        # blank_parens (dispatch://INFRA-1545, round 37).
        self.odd_functions: list[tuple[int, int | None, str]] = []
        # The loops this check refuses by name rather than reads, as (line, shape), each shape
        # one of LOOP_SHARED and LOOP_AFTER_WORD (dispatch://INFRA-1545, round 37).
        self.odd_loops: list[tuple[int, tuple[str, str, str]]] = []
        # Each `bash -c`, `sh -c` or `eval` text this check refuses by name where it fetches, as
        # (line, the words up to the text, why): one written in another form than `bash -c
        # '<text>'`, or holding a loop or a function (dispatch://INFRA-1545, round 37).
        self.shell_texts: list[tuple[int, str, str]] = []
        # Each call of a function this body defines written after a word CALL_PREFIXES does not
        # hold, as (line, name, the words before the name), refused by name where the function
        # fetches (dispatch://INFRA-1545, round 37).
        self.odd_calls: list[tuple[int, str, list[str]]] = []
        # The forms this check refuses by name where a fetch, or a call of a script that
        # fetches, is written anywhere in the text, as (line, what the refusal says after naming
        # the step or the script): an `alias`, a brace bash reads as a word, and a loop's own word
        # bash reads as no reserved word (dispatch://INFRA-1545, round 37). `aliases` is the line
        # of each alias, which a text sourcing this one is refused for too.
        self.whole_text: list[tuple[int, str]] = []
        self.aliases: list[int] = []
        # Each command written after the `}` that closes a function's body, on a line that body
        # holds a command of, as (line, the function): read as the function's, refused by name
        # where its reach fetches (dispatch://INFRA-1545, round 37).
        self.after_close: list[tuple[int, str]] = []
        # Each `retry_fetch` or `timeout` written in a form this check does not read, as (line,
        # what the refusal says, whether the command holds a fetch, whether it is a call of the
        # helper): a `retry_fetch` outside its signature or behind a wrapper, which fetches
        # nothing, and a `timeout` whose short options take a word the reading does not
        # (odd_command; dispatch://INFRA-1545, round 37). `defines_helper` is the line of each
        # definition of a function named `retry_fetch`.
        self.odd_commands: list[tuple[int, str, bool, bool]] = []
        self.defines_helper: list[int] = []
        # The commands of the body and the text they were read off, which the readings of a
        # call through a variable and of a library's functions need (commands_of;
        # dispatch://INFRA-1545, round 37).
        self.commands: list[Command] = []
        self.text = ""
        self.first_line = 1

    def enclosing(self, line: int) -> list[int]:
        """The loops around one line of this body, outermost first. A command carries its own
        stack; this answers for a line read off the text, which is how a script call is found."""
        return sorted(start for start, end in self.extent.items() if start <= line <= end)

    def function_of(self, line: int) -> str | None:
        """The function whose body holds this line, the innermost where more than one does."""
        found: str | None = None
        for name, (start, end) in self.function_span.items():
            if start <= line <= end and (found is None or start > self.function_span[found][0]):
                found = name
        return found


def passes_of(text: str, command: Command) -> int | None:
    """The passes a loop makes, where the loop writes them as this tree does: a `for x in 1 2 3
    4 5 6 7`, whose list is literal words. Everything else — a `while`, an `until`, a `for` over
    a variable, over the lines a command printed or over `$(seq 1 7)`, and a `for ((…))` —
    writes no count this check reads, and reads as None rather than as one pass, which is a loop
    refused unless a declaration writes its seconds. Until round 37 a `$(seq …)` list and a
    `for ((i = 0; i < <bound>; i += <step>))` over a number or an array's `${#name[@]}` were
    counted too, and the count of an array's words read `read -ra name` and `("${@}")` as one
    word each; no such loop of this tree holds a call whose figure its passes enter
    (dispatch://INFRA-1545, round 37)."""
    match = re.match(r"for\s+[A-Za-z_]\w*\s+in\s+([^\n;]*)", text[command.offset:])
    words = match.group(1).split() if match else []
    if words and all(LITERAL_WORD.fullmatch(word) for word in words):
        return len(words)
    return None


# The function definitions bash takes and this check does not read, each refused by name where a
# fetch, or a call of a script that fetches, follows it (dispatch://INFRA-1545, round 37).
FUNCTION_KEYWORD = "with the `function` keyword"
FUNCTION_BLANK = "with a blank before or inside its `()`"
FUNCTION_NOT_GROUP = ("with a body that is not a `{ … }` group, such as a `( … )` subshell, an "
                      "`if`, a loop or a `case`")
FUNCTION_AFTER_WORD = "after a reserved word on its line, such as `then name() {`"
FUNCTION_NESTED = "inside another function's body"
# Three more readings round 37 refuses by name rather than models, none written in this tree
# (dispatch://INFRA-1545, round 37): a name defined twice, whose first body this check charged
# once whatever the calls made before the second; a definition on the line another function's
# body ends on, whose line, and so its body's fetch, was read as the first function's; and a
# function that calls itself, directly or through another, whose calls of itself are no calls
# here.
FUNCTION_AGAIN = "that the same text defines again under the same name"
FUNCTION_SHARED = ("on the line another function's body ends on, such as `quiet() { :; }; "
                   "name() {`")
FUNCTION_CALLS_ITSELF = "that calls itself, directly or through another function"
# What the refusal of one of them says after naming the step or the script holding it, and what
# it asks the author to write, which is the form below unless FUNCTION_CURES names another. Its
# reach starts on the definition's own line, so a fetch written before the name on that line is
# in it: until round 37 the sentence said "written after it" of such a fetch
# (dispatch://INFRA-1545, round 37).
FUNCTION_REFUSAL = (
    "defines a function {shape}, a form this check does not read as a function, and a fetch, or "
    "a call of a script that fetches, is in its reach: written on its line or after it, or in "
    "the body of a function that a call written there calls. A call in a function's body runs "
    "once for every call of the function, and this check counts those calls only for a function "
    "written `name() {{ …; }}`: its name with `()` against it, then a `{{ … }}` group, on the "
    "name's line or the next, as a command of its own outside any other function. Read as "
    "commands of the shell, the calls in this one are charged once whatever the function's calls. "
    "{cure}")
FUNCTION_CURE = "Write the function in that form, or write its body where it is called"
FUNCTION_CURES = {
    FUNCTION_AGAIN: "Give each definition a name of its own, or write its body where it is called",
    FUNCTION_SHARED: "Start each definition on a line of its own",
    FUNCTION_CALLS_ITSELF: ("Write the repetition as a loop whose passes are written, such as "
                            "`for pass in 1 2 3; do`, around a call of a function that calls "
                            "no function holding it"),
}
# The words a call of a function may follow on its command, which are the ones this tree writes
# (dispatch://INFRA-1545, round 37): none, `if`, `if !`, `!` and `elif !`. A call after any other
# word — `then f` and `do f` on one line, an assignment prefix, `timeout`, `command` — is read as
# no call and refused by name where the function fetches, since its passes or its count were what
# a reading of it could miss.
CALL_PREFIXES = {(), ("if",), ("if", "!"), ("!",), ("elif", "!")}
CALL_REFUSAL = (
    "calls `{name}` after `{prefix}`, a form this check does not read as a call of a function, "
    "and `{name}` fetches. This check counts a call where the function's name opens its command "
    "or follows `if`, `if !`, `!` or `elif !`, the forms this tree writes, so the fetches this "
    "call runs are charged as though it were not made. Write the call as a command of its own, "
    "on a line of its own or after one of those words")
# The loops this check refuses by name rather than reads, none written in this tree: a loop is
# read by the line it opens on and only where its word opens a command, so each of these hides
# passes a call makes (dispatch://INFRA-1545, round 37). Each is (what the refusal says it
# writes, why that hides passes, what to write instead).
LOOP_SHARED = ("two loops on one line",
               "The passes of one of them stand for the other's, so a call in either can be "
               "charged fewer times than it runs",
               "Write each loop on lines of its own")
LOOP_AFTER_WORD = ("a loop after a reserved word on its line, such as `then for x in 1 2; do`",
                   "This one is read as no loop at all, so a call in it is charged once whatever "
                   "its passes",
                   "Open the loop on a line of its own, `then` on one line and `for x in 1 2; "
                   "do` on the next")
LOOP_REFUSAL = ("writes {shape}, and a fetch, or a call of a script that fetches, is written in "
                "the same text. This check reads a loop by the line it opens on, and finds one "
                "only where its word opens a command. {why}. {cure}")
# Three more forms refused by name where a fetch, or a call of a script that fetches, is written
# anywhere in the text, none written in this tree, each hiding a count of lines as a loop does
# (dispatch://INFRA-1545, round 37): an alias, a brace bash reads as a word, and a loop's own
# word bash reads as no reserved word. Each is what the refusal says after naming the step or
# the script.
ALIAS_REFUSAL = (
    "defines an alias, a form this check does not read, and a fetch, or a call of a script that "
    "fetches, is written in the same text. Bash expands an alias once `shopt -s expand_aliases` "
    "is set, and sh always does, so an alias can open a loop or hide words this check never "
    "reads: `alias nice='for pass in 1 2 3; do'` makes `nice retry_fetch …; done` three calls, "
    "which this check charges as one. Write the words the alias stands for at each place it is "
    "used")
BRACE_REFUSAL = (
    "writes `{brace}` where bash reads it as a word of its command and not as a group's brace: "
    "in quotes, after a backslash, or after a word that is no reserved word, such as `echo '}}'`. "
    "A fetch, or a call of a script that fetches, is written in the same text, and this check "
    "reads every `{{` and `}}` word as a brace, so a function's body is read as ending or going "
    "on there, and a call after it is charged as though it were outside that function or inside "
    "it. Write the brace another way, such as `printf '%s\\n' \"$close\"` with `close='}}'` set "
    "before")
LOOP_WORD_REFUSAL = (
    "writes `{word}` {how}, where bash does not read it as the reserved word that opens or "
    "closes a loop and this check does, and a fetch, or a call of a script that fetches, is "
    "written in the same text. This check reads a loop from a command whose first word is "
    "`for`, `while`, `until` or `select` to the next whose first word is `done`, with quotes and "
    "backslashes removed and the `(`, `)` and `|` of a `case` pattern or an extglob read as the "
    "shell's own operators, so the calls after this word are read as inside or outside a loop "
    "they are not, and charged for passes they do not make or once for passes they do. Write a "
    "loop's own words bare, and test such a value another way, such as `[ \"$state\" = done ]`")
# What the refusal of a heredoc unread_heredocs finds says after naming the step or the script:
# the consumer, and the form to write instead (dispatch://INFRA-1545, round 37).
HEREDOC_UNREAD = (
    "feeds a heredoc to {consumer}, and the body holds a fetch or names a script that fetches. "
    "This check reads every heredoc's body as data, which it is for `python3`, `cat` and `read` "
    "with nothing piped into a shell, the consumers this tree writes, so what runs of this one is "
    "charged nothing. {cure}, or move them into a script under .github/scripts/ and run that")
# What the refusal of shell text written in a form other than `bash -c '<text>'`, or holding a
# loop, a function or an alias, says after naming the step or the script (dispatch://INFRA-1545,
# round 37).
SHELL_TEXT_REFUSAL = (
    "runs `{form}` text, {why}, and a fetch is written in it. This check reads shell text in one "
    "form, `bash -c '<text>'`, with `-c` the shell's one option and no loop, no function and no "
    "alias in the text, which is the form this tree writes: it charges a fetch there once for "
    "each time the command runs, and counts no option's effect, no pass of a loop and no call of "
    "a function written there. Write `bash -c '<text>'`, setting any option inside the text "
    "(`set -o pipefail`, `shopt -s <name>`), and write a loop or a function in the step itself "
    "or in a script under .github/scripts/ that the step runs, where this check reads it")
# What the refusal of a command `odd_command` names says after naming the step or the script
# (dispatch://INFRA-1545, round 37): a `retry_fetch` behind a wrapper, which runs an executable
# and cannot run a shell function, and one outside its signature, which the helper refuses with
# no attempt made, both fetching nothing and charged nothing; a `timeout` whose short options end
# in one that takes the next word, which hides the command behind it; a definition of the
# helper's own name; and a command after the `}` that closes a function, on a line its body
# holds a command of. None is written in this tree.
RETRY_BEHIND = (
    "runs `retry_fetch` behind `{wrapper}`, which runs an executable and cannot run the helper, "
    "a shell function: the call fails before any attempt and fetches nothing, so this check "
    "charges it nothing. Call `retry_fetch` as a command of its own, with nothing before it but "
    "variable assignments, and write any wrapper inside it, after its `--`")
RETRY_UNSIGNED = (
    "calls `retry_fetch` in a form other than its signature, `retry_fetch <what> <seconds> -- "
    "<command…>` with the seconds a whole number above 0: the helper refuses such a call with "
    "no attempt made, so nothing is retried or bounded, and this check charges it nothing. "
    "Write the call in that form")
TIMEOUT_CLUSTER = (
    "runs `timeout` with `{option}`, a cluster of short options whose last takes the next word as "
    "its value, a form this check does not read: it takes that word for the duration and the "
    "duration for the command, so what `timeout` runs is read as no fetch. Write each option as "
    "a word of its own, such as `-v -k 5`")
HELPER_DEFINED = (
    "defines a function named `retry_fetch`, the helper's own name, so every call this check "
    "reads as the helper's runs this definition instead, and is read as retried at R1's floor "
    "and bounded whatever this one does. Source .github/scripts/ci-retry.sh and give this "
    "function another name")
AFTER_CLOSE_REFUSAL = (
    "writes a command after the `}}` that closes `{name}`, on a line that function's body holds "
    "a command of, a form this check does not read, and a fetch, or a call of a script that "
    "fetches, is in its reach. This check reads a line as a function's from its definition to "
    "its body's last command, so that command is charged once for each call of `{name}`, and a "
    "fetch there is taken for that function's. Start the command on a line of its own")


def function_bodies(commands: list[Command]) -> tuple[list[str | None], dict[str, int],
                                                       list[tuple[int, int | None, str]],
                                                       list[tuple[int, str]]]:
    """For each command, in the order a scanner lists them, the function whose body holds it, or
    None; for each function, the index of the command that names it; and each definition this
    reading does not follow, as (line, last, shape), `last` being the last line its body can
    reach, None for the end of the text. A function is read in one spelling: `name()`, its
    parentheses against its name, which the scanners make a command of its own, and then a `{ … }`
    group, on that line or the next, written as a command of its own outside any other function.
    This is the one reading of a function both scanners' readers share: structure multiplies what
    a body holds by the function's calls, and functions_around refuses a script run inside one.

    Every other definition bash takes is a shape this reading names rather than reads
    (dispatch://INFRA-1545, round 37): the `function` keyword, a body that is not a group, a
    definition after a reserved word on its line, and one inside another function's body, whose
    body ends where that function's does, with the blank before or inside `()` that Scanner
    names. Read as commands of the body, a call in one of them would be charged once for any
    number of calls of the function, so the step or the script writing one is refused where a
    fetch is written in its reach (unread_shapes_of). So is a name defined twice, refused at its
    first definition, since a call before the second runs the first body and the reading holds
    one body for each name; and a definition on the line another function's body ends on, since
    a line is held by the function whose definition it reads first (dispatch://INFRA-1545, round
    37). This tree writes one of these shapes, twice: download-retry-proof.sh defines
    `probe_wrong` and `fail` inside the function that calls them, and no fetch is written there.
    Every other function it defines is `name() {`.

    The fourth answer is each command written after the `}` that closes a body, on a line that
    body holds a command of, `quiet() { :; }; curl …`, as (line, the function): structure reads a
    line as a function's from its definition to its body's last command, so such a command is
    read as the function's, and it is refused by name where it fetches (dispatch://INFRA-1545,
    round 37)."""
    marks: list[str | None] = []
    defined: dict[str, int] = {}
    odd: list[tuple[int, int | None, str]] = []
    after_close: list[tuple[int, str]] = []
    # The lines of the definitions written inside the body being read.
    nested: list[int] = []
    name = ""
    # `{` less `}` inside the body being read.
    depth = 0
    pending: Command | None = None
    # The line the last body read ended on, with its closing `}`, and whether a command of that
    # body other than a closing brace is written on it.
    closed: int | None = None
    shared = False
    # The lines of the body being read that hold a command other than a closing brace.
    body_lines: set[int] = set()
    for index, command in enumerate(commands):
        words = command.words
        # The first word that is no reserved word, or that is `function`, which is one.
        lead = next((at for at, word in enumerate(words)
                     if word not in KEYWORDS or word == "function"), len(words))
        first = words[lead] if lead < len(words) else ""
        if first == "function" and lead + 1 < len(words):
            # `function` with no name after it defines nothing: a `case` pattern of that word.
            odd.append((command.line, None, FUNCTION_KEYWORD))
        elif FUNCTION_WORD.fullmatch(first) and depth > 0:
            nested.append(command.line)
        elif FUNCTION_WORD.fullmatch(first) and lead > 0:
            odd.append((command.line, None, FUNCTION_AFTER_WORD))
        if depth > 0:
            marks.append(name)
            depth += words.count("{") - words.count("}")
            if any(word != "}" for word in words):
                body_lines.add(command.line)
            if depth <= 0:
                odd += [(line, command.line, FUNCTION_NESTED) for line in nested]
                nested = []
                closed = command.line
                shared = closed in body_lines
                body_lines = set()
            continue
        defining, pending = pending, None
        if defining is not None:
            if words[0] == "{":
                marks.append(name)
                depth = words.count("{") - words.count("}")
                continue
            odd.append((defining.line, None, FUNCTION_NOT_GROUP))
        marks.append(None)
        if FUNCTION_WORD.fullmatch(words[0]):
            name = words[0][:-2]
            if name in defined:
                odd.append((commands[defined[name]].line, None, FUNCTION_AGAIN))
            if command.line == closed:
                odd.append((command.line, None, FUNCTION_SHARED))
            defined[name] = index
            pending = command
        elif command.line == closed and shared:
            after_close.append((command.line, name))
    if pending is not None:
        odd.append((pending.line, None, FUNCTION_NOT_GROUP))
    odd += [(line, None, FUNCTION_NESTED) for line in nested]
    return marks, defined, odd, after_close


def structure(commands: list[Command], text: str) -> Loops:
    """Marks each command with the loops it sits in, and returns what rule 8 reads of them: each
    loop's extent, passes and own wait between two passes, the loop each function holds, and
    where each function this body defines is written and called — function_bodies's reading of
    which commands a function's body holds, with the definitions it does not read. A call is a
    command whose first word, once the words CALL_PREFIXES lists are gone, is a function's name,
    outside that function's own body: `fetch`, `if fetch`, `if ! fetch`, `! fetch` and
    `elif ! fetch` each call `fetch`, the five forms this tree writes. A call after any other
    word, a `then` or a `do` on its line, an assignment prefix or a wrapper, is no call here
    and is refused by name where the function fetches (odd_calls; dispatch://INFRA-1545, round
    37). The loops and the functions it refuses by name rather than reads are LOOP_SHARED,
    LOOP_AFTER_WORD and FUNCTION_CALLS_ITSELF; and three forms it refuses by name wherever the
    text fetches, an alias, a loop's own word bash reads as no reserved word, and the braces
    Scanner names (whole_text; dispatch://INFRA-1545, round 37).

    A function's lines run from its definition to its body's last command other than a closing
    brace, so a command after a `}` that starts its line, `}; next`, is the text's own: until
    round 37 they ran to the `}`'s line, so that command was charged for each call of the
    function and a fetch there taken for the function's (dispatch://INFRA-1545, round 37)."""
    loops: list[int] = []
    last = 0
    read = Loops()
    holders, defined, read.odd_functions, read.after_close = function_bodies(commands)
    for command, holder in zip(commands, holders):
        first = command.words[0]
        if first in LOOP_WORDS or first == "done":
            # Bash reads the word as a loop's only bare, and only where it opens a command: one
            # in quotes or after a backslash is a command of that name, and one a `case`
            # pattern or an extglob writes where it opens a command, `done)` on a line of its
            # own or after `;;`, `(done)` or `@(done|failed)`, is a pattern, which the scanners
            # read as a command ended by its `)` or `|`. One written after `case … in` on that
            # line is a word of the `case` command, opens no command, and is read as no loop's
            # word here either (dispatch://INFRA-1545, round 37).
            how = None
            if command.quoted and command.quoted[0]:
                how = "in quotes or after a backslash"
            elif (len(command.words) == 1
                  and re.match(r"\w+\s*(?:\)|\|(?!\|))", text[command.offset:])
                  and re.search(r"(?:\bin|;;|;&|\(|(?<!\|)\|)$", text[:command.offset].rstrip())):
                how = "as a `case` pattern or an extglob alternative, such as `done)`"
            if how:
                read.whole_text.append((command.line,
                                        LOOP_WORD_REFUSAL.format(word=first, how=how)))
        if first == "done" and loops:
            read.extent[loops.pop()] = command.line
        command.loops = list(loops)
        command.loop = loops[-1] if loops else None
        last = max(last, command.line)
        if (command.loop is not None and first == "sleep" and len(command.words) == 2
                and re.fullmatch(r"\d+", command.words[1])):
            # The loop's own wait between two passes, where it is written as a number. A
            # `sleep "$(( 10 << (i - 1) ))"` is computed rather than written and adds nothing,
            # and where a loop writes more than one the longest is what a pass can wait.
            seconds = int(command.words[1])
            read.sleep[command.loop] = max(read.sleep.get(command.loop, 0), seconds)
        if holder is not None and command.loop is not None and holder not in read.function_loop:
            read.function_loop[holder] = command.loop
        # The first word that is no reserved word but a loop's: a loop opened after one, as in
        # `then for x in 1 2; do`, is refused by name rather than read (dispatch://INFRA-1545,
        # round 37).
        lead = next((word for word in command.words
                     if word not in KEYWORDS - LOOP_WORDS and word != "time"), "")
        if first not in LOOP_WORDS and lead in LOOP_WORDS:
            read.odd_loops.append((command.line, LOOP_AFTER_WORD))
        if first in LOOP_WORDS:
            if command.line in read.passes:
                read.odd_loops.append((command.line, LOOP_SHARED))
            loops.append(command.line)
            read.passes[command.line] = passes_of(text, command)
    # A loop or a function whose closing word this body never writes runs to the end of it.
    for line in loops:
        read.extent[line] = last
    for name, index in defined.items():
        start = commands[index].line
        read.function_span[name] = (start, max(
            (command.line for command, holder in zip(commands, holders)
             if holder == name and any(word != "}" for word in command.words)), default=start))
    # Which functions each function calls, which is how one that reaches itself is found.
    calls_from: dict[str, set[str]] = {}
    for index, (command, holder) in enumerate(zip(commands, holders)):
        reduced = reduce_words(command.words)[0]
        name = reduced[0] if reduced else ""
        if name == "alias" and any("=" in word for word in reduced[1:]):
            read.aliases.append(command.line)
            read.whole_text.append((command.line, ALIAS_REFUSAL))
        if name not in defined or index == defined[name]:
            continue
        if tuple(command.words[:len(command.words) - len(reduced)]) not in CALL_PREFIXES:
            read.odd_calls.append((command.line, name,
                                   command.words[:len(command.words) - len(reduced)]))
            continue
        if holder is not None:
            calls_from.setdefault(holder, set()).add(name)
        if holder != name:
            read.function_calls.setdefault(name, []).append(command.line)
    for name, index in defined.items():
        reached: set[str] = set()
        waiting = list(calls_from.get(name, ()))
        while waiting:
            called = waiting.pop()
            if called not in reached:
                reached.add(called)
                waiting += calls_from.get(called, ())
        if name in reached:
            read.odd_functions.append((commands[index].line, None, FUNCTION_CALLS_ITSELF))
    return read


def reduce_words(words: list[str]) -> tuple[list[str], str | None, bool, int | None]:
    """The words of the command that actually runs, the retry wrapping it (`retry_fetch`, the one
    helper, or `retry`, a Dockerfile's inline definition), whether something bounds it, and the
    seconds of that bound where it is written as a number — rule 8's `<bound>`."""
    retry = None
    bounded = False
    bound: int | None = None
    words = list(words)
    while words:
        first = words[0]
        if ASSIGNMENT.match(first) or first in PASSTHROUGH or first in KEYWORDS:
            words.pop(0)
            continue
        if first == "timeout":
            # timeout(1)'s options, `-k <duration>` and `-s <signal>` taking the word after them,
            # then its duration, which is the word after the options whatever it is. A duration
            # this check cannot read as a whole number of seconds above 0 — a `0`, which is no
            # limit at all, a decimal, a variable — bounds nothing here, and the command behind
            # it stays the command the rules read: until round 37 an option's value or such a
            # duration was read as the command, so the fetch behind it was no fetch at all, and
            # `timeout 0` read as a bound of 0 s (dispatch://INFRA-1545, round 37).
            words.pop(0)
            while words and words[0].startswith("-") and words[0] != "--":
                if words.pop(0) in ("-k", "-s", "--kill-after", "--signal") and words:
                    words.pop(0)
            if words and words[0] == "--":
                words.pop(0)
            duration = words.pop(0) if words else ""
            match = re.fullmatch(r"(\d+)([smhd]?)", duration)
            if match and int(match.group(1)) > 0:
                bounded = True
                if bound is None:
                    bound = int(match.group(1)) * SUFFIX[match.group(2)]
            continue
        if first == "retry_fetch":
            # `retry_fetch <what> <bound> --`, the helper's own signature and the one form this
            # tree writes: the bound is the second word, and a call in any other form stops in
            # the helper with no attempt made, so it bounds none here. Until round 37 the bound
            # was the first count of seconds among the three words, so `retry_fetch 5 300 -- …`
            # read 5 s an attempt where the helper allows 300 s (dispatch://INFRA-1545, round 37).
            retry = "retry_fetch"
            if len(words) > 3 and words[3] == "--" and re.fullmatch(r"[1-9]\d*", words[2]):
                bounded = True
                if bound is None:
                    bound = int(words[2])
            del words[:words.index("--", 0, 4) + 1 if "--" in words[:4] else 1]
            continue
        if first == "retry" and len(words) > 2 and re.fullmatch(r"\d+", words[1]):
            retry = "retry"
            bounded = True
            if bound is None:
                bound = int(words[1])
            del words[:3]
            continue
        break
    return words, retry, bounded, bound


def odd_command(words: list[str]) -> tuple[str, bool] | None:
    """What the refusal of this command says, where it writes `retry_fetch` or `timeout` in a
    form this check does not read, and whether that form is a call of the helper, which then
    fetches nothing; None where it writes neither (dispatch://INFRA-1545, round 37). The wrappers
    are read as reduce_words reads them. A `retry_fetch` behind `timeout`, another retry, or any
    wrapper but `time` is run as an executable, which the helper, a shell function, is not; one
    whose `--` is not its third word, or whose seconds are written as no whole number above 0
    and as no variable, is refused by the helper with no attempt made; and a `timeout` whose
    short options are a cluster ending in `-k` or `-s`, such as `-vk 5`, takes the next word as
    that option's value, where reduce_words takes it for the duration."""
    rest = list(words)
    wrapper: str | None = None
    while rest:
        first = rest[0]
        if ASSIGNMENT.match(first) or first in KEYWORDS or first == "time":
            rest.pop(0)
            continue
        if first == "retry_fetch":
            if wrapper is not None:
                return RETRY_BEHIND.format(wrapper=wrapper), True
            bound = rest[2] if len(rest) > 2 else ""
            if len(rest) < 4 or rest[3] != "--" or not (
                    "$" in bound or "EXPR" in bound
                    or (re.fullmatch(r"\d+", bound) and int(bound) > 0)):
                return RETRY_UNSIGNED, True
            wrapper = "retry_fetch"
            del rest[:4]
            continue
        if first == "timeout":
            rest.pop(0)
            while rest and rest[0].startswith("-") and rest[0] != "--":
                option = rest.pop(0)
                if re.fullmatch(r"-[vfp]+[ks]", option):
                    return TIMEOUT_CLUSTER.format(option=option), False
                if option in ("-k", "-s", "--kill-after", "--signal") and rest:
                    rest.pop(0)
            if rest and rest[0] == "--":
                rest.pop(0)
            del rest[:1]
            wrapper = "timeout"
            continue
        if first == "retry" and len(rest) > 2 and re.fullmatch(r"\d+", rest[1]):
            wrapper = "retry"
            del rest[:3]
            continue
        if first in PASSTHROUGH:
            wrapper = first
            rest.pop(0)
            continue
        return None
    return None


def helper_bound_word(words: list[str]) -> str | None:
    """The seconds word of this command's `retry_fetch`, as written, where it opens the command
    after assignments and reserved words: what a refusal of a bound this check cannot read as a
    number quotes (dispatch://INFRA-1545, round 37)."""
    rest = list(words)
    while rest and (ASSIGNMENT.match(rest[0]) or rest[0] in KEYWORDS or rest[0] == "time"):
        rest.pop(0)
    return rest[2] if len(rest) > 2 and rest[0] == "retry_fetch" else None


def subcommand(words: list[str]) -> str | None:
    """The first word that is not an option."""
    return next((word for word in words if not word.startswith("-")), None)


def gh_verb_of(rest: list[str]) -> str | None:
    """The verb a `gh` call runs, named `gh <subcommand>` or `gh <subcommand> <action>`, or None
    where that subcommand reads and writes nothing over the network. Every gh subcommand but
    GH_LOCAL and the four local `gh auth` actions reaches api.github.com over HTTPS, which is the
    read `gh api` already makes, so the default here is that a gh call is a fetch: a subcommand
    nobody listed is held to the floor rather than passing unseen."""
    words = [word for word in rest if not word.startswith("-")]
    sub = words[0] if words else None
    if sub is None or sub in GH_LOCAL:
        return None
    action = words[1] if len(words) > 1 else None
    if sub == "auth" and action in GH_AUTH_LOCAL:
        return None
    if sub not in GH_GROUPS:
        # `api`, `browse` and `status` take no action word: the word after them is an argument
        # (`gh api repos/owner/name`), and naming it in the verb would name every route.
        return f"gh {sub}"
    return f"gh {sub} {action}" if action else f"gh {sub}"


def verb_of(words: list[str]) -> str | None:
    """The fetch verb these words run, named as the list and the messages name it, or None."""
    if not words:
        return None
    # Its own options, which stop at a `--`: past that separator the words belong to a command
    # this one runs (`mise x "$pin" -- omp --version` resolves the tool and then asks it, and
    # that trailing `--version` says nothing about this command).
    own = words[1:words.index("--")] if "--" in words[1:] else words[1:]
    if any(word in INFORMATIONAL for word in own):
        return None
    name = PROGRAM.sub("", words[0]).rsplit("/", 1)[-1]
    rest = words[1:]
    if name in RUNNERS:
        return verb_of(rest)
    if name in ("curl", "wget") and any(word in ("-V", "-h") for word in rest):
        return None
    if name == "curl":
        return "curl"
    if name == "wget":
        return "wget"
    if name in ("apt-get", "apt", "aptitude"):
        sub = subcommand(rest)
        if sub not in APT_SUBCOMMANDS or any(word in APT_SIMULATE for word in rest):
            return None
        return f"apt-get {sub}"
    if name in ("pip", "pip3") and subcommand(rest) == "install":
        return "pip install"
    if name == "uv":
        sub = subcommand(rest)
        return f"uv {sub}" if sub in UV_SUBCOMMANDS else None
    if name == "bun":
        sub = subcommand(rest)
        return f"bun {sub}" if sub in BUN_SUBCOMMANDS else None
    if name in ("npm", "pnpm", "yarn"):
        sub = subcommand(rest)
        return f"{name} {sub}" if sub in NPM_SUBCOMMANDS else None
    if name == "playwright":
        # `install-deps` is the apt phase, which dispatch://INFRA-1443 owns: it is not this one.
        return "playwright install" if rest and rest[0] == "install" else None
    if name == "go":
        while len(rest) > 1 and rest[0] == "-C":
            del rest[:2]
        if rest[:2] == ["mod", "download"]:
            return "go mod download"
        if rest[:1] == ["install"]:
            return "go install"
        return None
    if name == "mise":
        sub = subcommand(rest)
        return f"mise {sub}" if sub in MISE_SUBCOMMANDS else None
    if name == "crane":
        sub = subcommand(rest)
        return f"crane {sub}" if sub in CRANE_SUBCOMMANDS else None
    if name == "docker":
        return "docker pull" if subcommand(rest) == "pull" else None
    if name == "gh":
        return gh_verb_of(rest)
    if name == "pulumi" and rest[:2] == ["plugin", "install"]:
        return "pulumi plugin install"
    return None


def silent_verb_of(words: list[str]) -> str | None:
    """The rule 7 mise subcommand these words run, or None. These are no fetch verbs: each exits
    0 when it cannot resolve the tool, so what the check does with one is refuse a retry."""
    if not words:
        return None
    own = words[1:words.index("--")] if "--" in words[1:] else words[1:]
    if any(word in INFORMATIONAL for word in own):
        return None
    if PROGRAM.sub("", words[0]).rsplit("/", 1)[-1] != "mise":
        return None
    sub = subcommand(words[1:])
    return f"mise {sub}" if sub in MISE_SILENT else None


def verb_in(words: list[str]) -> tuple[str, list[str]] | None:
    """The first fetch verb among these words, wherever it sits: what a call hands a loop that
    runs `"$@"`."""
    for index in range(len(words)):
        verb = verb_of(words[index:])
        if verb:
            return verb, words[index:]
    return None


def curl_bounds(words: list[str]) -> list[str | None]:
    """Each value a curl writes for its per-attempt bound, in the order written, as `--max-time
    <s>`, `-m <s>` or `--max-time=<s>`, None for one with no word after it. curl takes the last of
    the first two spellings and rejects the third as an unknown option; this tree writes one, as
    `--max-time <s>`, and every other way of writing a bound is refused by name
    (curl_bound_refusal), since rule 8 read the first until round 37 (dispatch://INFRA-1545,
    round 37)."""
    found: list[str | None] = []
    for index, word in enumerate(words):
        if word.startswith("--max-time="):
            found.append(word.partition("=")[2])
        elif word in ("--max-time", "-m"):
            found.append(words[index + 1] if index + 1 < len(words) else None)
    return found


# The options curl reads the next word after as their value, each written with an `<argument>` in
# `curl --help all` at curl 8.14.1, short and long: a `--max-time` or a `-m` written right after
# one of them is that option's value and bounds nothing (curl_bound_refusal;
# dispatch://INFRA-1545, round 37).
CURL_VALUE_SHORT = set("ACDEFHKPQTUXYbcdemortuwxyz")
CURL_VALUE_LONG = set("""
abstract-unix-socket alt-svc aws-sigv4 cacert capath cert cert-type ciphers config connect-timeout
connect-to continue-at cookie cookie-jar create-file-mode crlfile curves data data-ascii
data-binary data-raw data-urlencode delegation dns-interface dns-ipv4-addr dns-ipv6-addr
dns-servers doh-url dump-header ech egd-file engine etag-compare etag-save expect100-timeout form
form-string ftp-account ftp-alternative-to-user ftp-method ftp-port ftp-ssl-ccc-mode
happy-eyeballs-timeout-ms haproxy-clientip header hostpubmd5 hostpubsha256 hsts interface ip-tos
ipfs-gateway json keepalive-cnt keepalive-time key key-type krb libcurl limit-rate local-port
login-options mail-auth mail-from mail-rcpt max-filesize max-redirs max-time netrc-file noproxy
oauth2-bearer output output-dir parallel-max pass pinnedpubkey preproxy proto proto-default
proto-redir proxy proxy-cacert proxy-capath proxy-cert proxy-cert-type proxy-ciphers
proxy-crlfile proxy-header proxy-key proxy-key-type proxy-pass proxy-pinnedpubkey
proxy-service-name proxy-tls13-ciphers proxy-tlsauthtype proxy-tlspassword proxy-tlsuser
proxy-user proxy1 pubkey quote random-file range rate referer request request-target resolve
retry retry-delay retry-max-time sasl-authzid service-name sigalgs socks4 socks4a socks5
socks5-gssapi-service socks5-hostname speed-limit speed-time ssl-sessions stderr telnet-option
tftp-blksize time-cond tls-max tls13-ciphers tlsauthtype tlspassword tlsuser trace trace-ascii
trace-config unix-socket upload-file upload-flags url url-query user user-agent variable
vlan-priority write-out
""".split())


def takes_value(word: str) -> bool:
    """Whether curl reads the word after this one as this option's value: a long option of
    CURL_VALUE_LONG with no `=`, or a short cluster whose first letter taking a value is its
    last, since curl takes the rest of a cluster as the value of its first such letter."""
    if word.startswith("--"):
        return "=" not in word and word[2:] in CURL_VALUE_LONG
    if not re.fullmatch(r"-[A-Za-z]+", word):
        return False
    first = next((at for at, letter in enumerate(word[1:]) if letter in CURL_VALUE_SHORT), None)
    return first == len(word) - 2


def curl_bound_refusal(words: list[str]) -> str | None:
    """What the refusal of a curl making its own attempts says after naming the site, where it
    writes its per-attempt bound in a form this check does not read, or None
    (dispatch://INFRA-1545, round 37). This tree writes `--max-time <seconds>` once, as two words
    of their own after a word that is no option taking a value, and rule 8 prices that form
    alone. Every other spelling is refused by name, each with what curl does with it: a
    `--max-time=<s>`, which curl rejects; a `-m` against its seconds or ending a short cluster,
    which this check does not read and curl does; a bound right after an option taking a value,
    which is that value; a `--max-time` with no value, on which curl stops; more than one bound,
    of which curl takes the last; and a value that is no whole number of seconds and no variable,
    which this check cannot price. A bound written as a variable is the unread bound rule 8
    refuses unless declared, and none of these."""
    for index, word in enumerate(words[1:], 1):
        before = words[index - 1]
        cluster = re.fullmatch(r"-([A-Za-z]*?)m.*", word)
        clustered = bool(cluster and word != "-m" and not word.startswith("--")
                         and not CURL_VALUE_SHORT & set(cluster.group(1)))
        if ((word in ("--max-time", "-m") or word.startswith("--max-time=") or clustered)
                and takes_value(before)):
            return (f"writes `{word}` right after `{before}`, an option that takes the next word "
                    f"as its value, so curl reads `{word}` as that value and bounds nothing, "
                    "while this check would read a bound there. Write `--max-time <seconds>` "
                    "once, after the value of every option before it")
        if word.startswith("--max-time="):
            return (f"writes `{word}`, a spelling curl rejects as an unknown option before any "
                    "attempt, which this check would read as its bound. Write `--max-time "
                    "<seconds>` once, as two words")
        if clustered:
            return (f"writes its per-attempt bound as `{word}`, `-m` against its seconds or as "
                    "the last letter of a cluster of short options, a spelling this check does "
                    "not read and curl does, taking the last bound it is given. Write "
                    "`--max-time <seconds>` once, as two words of their own")
    bounds = curl_bounds(words)
    if None in bounds:
        return ("writes `--max-time` with no value after it, on which curl stops with `option "
                "--max-time: requires parameter` before any attempt. Write `--max-time <seconds>` "
                "once")
    if len(bounds) > 1:
        return (f"writes its per-attempt bound {len(bounds)} times, "
                + " and ".join(f"`{value}`" for value in bounds) + ", and curl takes the "
                "last one while rule 8 reads one, the form this tree writes: until round 37 "
                "it read the first, so `--max-time 30 --max-time 300` was priced at 30 s an "
                "attempt for a curl that waits 300 s. Write `--max-time <seconds>` once")
    value = bounds[0] if bounds else None
    if value is None or "$" in value or "EXPR" in value or re.fullmatch(r"\d+", value):
        return None
    if re.fullmatch(r"\d*\.\d+|\d+\.", value):
        return (f"writes `--max-time {value}`, which curl takes as {value} s and this check "
                "reads only as whole seconds, so it can put no figure on the attempts. Write "
                f"`--max-time {math.ceil(float(value))}`, the whole seconds above it")
    return (f"writes `--max-time {value}`, which is no number of seconds, and curl stops on it "
            "before any attempt. Write `--max-time <seconds>` once")


def curl_options(words: list[str]) -> tuple[int | None, bool, bool, bool]:
    """What a curl carries: its `--retry` count, whether it passes `--retry-all-errors`, whether
    it bounds each attempt with `--max-time`, and whether a persistent error fails it (`--fail`,
    the `f` of a short cluster, or a read of the status it wrote out). The bound is the last one
    curl_bounds reads, as curl takes it, and a `--max-time 0` sets no limit, as `timeout 0`
    does, nor does a `--max-time` with no value, on which curl stops with an error before any
    attempt (dispatch://INFRA-1545, round 37)."""
    retries = None
    all_errors = False
    bounds = curl_bounds(words)
    bounded = bool(bounds) and bounds[-1] is not None and not ZERO_SECONDS.fullmatch(bounds[-1])
    fails = "%{http_code}" in " ".join(words)
    index = 1
    while index < len(words):
        word = words[index]
        value = None
        if "=" in word and word.startswith("--"):
            word, _, value = word.partition("=")
        if word == "--retry":
            if value is None and index + 1 < len(words):
                value = words[index + 1]
            if value and value.isdigit():
                retries = int(value)
        elif word == "--retry-all-errors":
            all_errors = True
        elif word in ("--fail", "--fail-with-body", "--fail-early"):
            fails = True
        elif re.fullmatch(r"-[A-Za-z]*f[A-Za-z]*", word):
            fails = True
        index += 1
    return retries, all_errors, bounded, fails


def curl_bound(words: list[str]) -> int | None:
    """The seconds a curl gives each of its own attempts, its `--max-time`, where the first
    value it writes for it is a number above 0: rule 8's `<bound>` for a curl that makes the
    attempts itself. A curl writing more than one is refused by name (curl_bounds)."""
    bounds = curl_bounds(words)
    first = bounds[0] if bounds else None
    return int(first) if first and re.fullmatch(r"\d+", first) and int(first) > 0 else None


def curl_waits(words: list[str]) -> tuple[int | None, int | None, bool]:
    """What this curl writes for `--retry-delay` and `--retry-max-time`, in seconds, and whether
    every value it writes for them is a number this check can read. A value that is not — a
    variable, an expression — leaves the ladder unreadable rather than default, since the one
    thing this check must not do with these two options is assume."""
    delay: int | None = None
    max_time: int | None = None
    readable = True
    for index, word in enumerate(words):
        value = None
        if "=" in word and word.startswith("--"):
            word, _, value = word.partition("=")
        if word not in ("--retry-delay", "--retry-max-time"):
            continue
        if value is None and index + 1 < len(words):
            value = words[index + 1]
        if not value or not re.fullmatch(r"\d+", value):
            readable = False
            continue
        if word == "--retry-delay":
            delay = int(value)
        else:
            max_time = int(value)
    return delay, max_time, readable


def curl_ladder(retries: int, delay: int | None, max_time: int | None) -> int:
    """The seconds between the first of curl's own attempts and the last, as curl makes them —
    R1's floor is that this is at least LADDER_FLOOR. The rule is curl's own, at curl 8.14.1:
    the wait before the first retry is one second and doubles for each of the rest up to ten
    minutes; `--retry-delay <n>` replaces that with a fixed n, and `--retry-delay 0` asks for
    the default back-off again; `--retry-max-time <n>` lets a retry start only while the timer
    reset at the first attempt has not reached n, and `--retry-max-time 0` sets no limit. The
    attempts themselves are taken as instant, so this is the longest ladder the WAITS can
    make. What a real ladder comes to depends on the host rather than being always shorter:
    against one that refuses at once the attempts cost nothing and this figure is the ladder,
    and against one that accepts and never answers each attempt spends its whole `--max-time`,
    which both fills the timer sooner — fewer attempts — and makes the ladder far longer in
    seconds than this figure. At `--retry-max-time 300` with a 60 s bound that shape is five
    attempts over 256 s, where these waits alone would give eight over 127 s
    (dispatch://INFRA-1545)."""
    if delay:
        waits = [delay] * retries
    else:
        waits = [min(CURL_FIRST_WAIT << index, CURL_WAIT_CEILING) for index in range(retries)]
    elapsed = 0
    for wait in waits:
        if max_time and elapsed >= max_time:
            break
        elapsed += wait
    return elapsed


def apt_error_mode(words: list[str]) -> bool:
    """Whether this `apt-get update` fails when an index read fails, rather than warning and
    exiting 0: `-o APT::Update::Error-Mode=any`, in any spelling apt takes for an option. Option
    names are matched without regard to case, as apt reads them."""
    for index, word in enumerate(words):
        lowered = word.lower()
        value = None
        if lowered in ("-o", "--option"):
            value = words[index + 1] if index + 1 < len(words) else None
        elif lowered.startswith("-o"):
            value = word[2:].lstrip("=")
        elif lowered.startswith("--option="):
            value = word.partition("=")[2]
        if value and APT_ERROR_MODE.fullmatch(value.strip()):
            return True
    return False


class Site:
    """One network fetch: where it is, what verb it uses, what retries it, what bounds each of
    its attempts, and what it is in. `piped` is rule 9's: the fetch sits left of a `|` in a
    shell that leaves pipefail off, so the pipeline's status is its last command's and this
    fetch's failure is not read. `silent` marks rule 7's shape instead: a `mise x`, `mise
    exec`, `mise up` or `mise upgrade` with a retry around it, which is no fetch to hold to the
    floor but a retry to refuse. `loops` is rule 8's: every loop this call sits in, outermost
    first, and `loop` is the innermost of them, which is rule 2's. `factor` is how many times
    the body runs this call once those loops, the function holding it and the loops around that
    function's own calls are all read; `wait` is what those loops sleep between their passes;
    and `blocked` names the one loop whose passes this check could neither read nor find
    declared, which is the case it refuses rather than counts. `helper_bound` is the seconds
    word of the `retry_fetch` its command opens with, as written, which a refusal of a bound
    this check cannot read as a number quotes (dispatch://INFRA-1545, round 37)."""

    def __init__(self, path: str, line: int, verb: str, step: str, words: list[str],
                 retry: str | None, bounded: bool, loops: list[int], bound: int | None = None,
                 silent: bool = False, piped: bool = False) -> None:
        self.path = path
        self.line = line
        self.verb = verb
        self.step = step
        self.words = words
        self.retry = retry
        self.bounded = bounded
        self.loops = loops
        self.loop = loops[-1] if loops else None
        self.bound = bound
        self.silent = silent
        self.piped = piped
        self.factor: int | None = 1
        self.wait = 0
        self.blocked: int | None = None
        self.helper_bound: str | None = None


class Occurrence:
    """One path of a script under .github/scripts/ written in a shell body, and what this check
    reads it as (dispatch://INFRA-1545, round 37). `target` is the script; `kind` is CALL_RUN,
    CALL_SOURCED, CALL_NONE or CALL_UNREAD; `form` names, for an unread one, the place it is
    written in, which its refusal quotes; `line` is the line the command holding it starts on,
    which is the line the loops are read against, and `shown` the line the path itself sits
    on; `factor` is how many times the body runs that line — the loops around it and the calls
    of the function holding it — and None where a loop around it writes no count, `wait` what
    those loops sleep between their passes, and `loop` the loop whose passes could not be read.
    A `uses:` of a local composite action is a run of the action.yml it names, written as one of
    these with a factor of 1."""

    def __init__(self, target: str, kind: str, line: int, shown: int | None = None,
                 form: str = "", factor: int | None = 1, wait: int = 0,
                 loop: int | None = None) -> None:
        self.target = target
        self.kind = kind
        self.line = line
        self.shown = line if shown is None else shown
        self.form = form
        self.factor = factor
        self.wait = wait
        self.loop = loop


# What one Uncounted names: a fetch written in the body itself, a fetch of that body whose own
# per-attempt bound this check cannot read, or a call of a script that body runs. The three are
# read differently and take different advice, and the `kind` field below is the one place those
# differences are written down.
UNCOUNTED_SITE = "site"
UNCOUNTED_UNREAD_BOUND = "unread bound"
UNCOUNTED_SCRIPT_CALL = "script call"


class Uncounted:
    """One thing a step waits for that this check can put no figure on, and for which no
    DeclaredArithmetic entry writes the seconds: a retried call, or a script the body runs,
    inside a loop whose passes it cannot read; or a retried call whose attempts are bounded by
    a variable rather than by a number, wherever that call sits and whether or not a loop is
    around it.

    `what` is the verb, or the script's path; `one` is what one call of it costs, left None
    wherever no figure can be made of that — a script, whose arithmetic is read when the
    refusal is written because the scripts are read after the steps that run them, and a site
    whose bound is written as a variable rather than as a number, or not found at all.
    `unbounded` tells those last two apart, for an UNCOUNTED_SITE: True where this check finds
    no bound on one attempt of the site in any spelling (bound_spelled answers False), so the
    refusal names that rather than a variable nobody wrote (dispatch://INFRA-1545, round 37).
    `helper` is True where the variable is the seconds of a `retry_fetch`, which rule 4 refuses
    at the site whatever a declaration writes, so the refusal offers no declaration in place of
    a number there (dispatch://INFRA-1545, round 37).

    `kind` tells apart the three things this class holds: UNCOUNTED_SITE, a fetch written in
    the body the item was read from, inside a loop with no pass count; UNCOUNTED_UNREAD_BOUND,
    a fetch of that same body whose passes this check did read — there may be no loop around
    it at all — and whose per-attempt bound it could not, which is a wait no budget can be
    held above either; and UNCOUNTED_SCRIPT_CALL, a call of a script that body runs. Every
    reader asking which of the three it has asks this field. Reading `one is None` for that
    question was wrong, because None is an answer about a figure and not about a kind: a site
    whose bound is a variable has no figure either, so it read as a script call, and the
    filter that drops a script waiting for nothing dropped it instead of refusing it
    (review round 8 of dispatch://INFRA-1545)."""

    def __init__(self, path: str, line: int, what: str, loop: int | None,
                 one: int | None, kind: str, unbounded: bool = False,
                 helper: bool = False) -> None:
        self.path = path
        self.line = line
        self.what = what
        self.loop = loop
        self.one = one
        self.kind = kind
        self.unbounded = unbounded
        self.helper = helper


def one_call(site: Site) -> int | None:
    """The wall time one call of this site takes to exhaust its attempts. Five at the helper's
    own bound with its sleeps of 10, 20, 40 and 80 s between them, or, where curl makes the
    attempts itself, one per `--retry` and one more at curl's `--max-time` with the ladder
    curl_ladder computes between them — the same ladder rule 1 holds that curl to, so the two
    rules read one figure. A curl outside that floor is read at the floor, which is the ladder
    it will make once rule 1's refusal is answered.

    Three answers, not two. A number is the seconds one call takes; 0 is a call this check puts
    no wait behind at all — a `mise x`, whose retry rule 7 refuses, and a verb whose attempts
    neither the helper nor curl itself makes; and None is a call that does make attempts whose
    length is written where this check cannot read it, a bound given as `"$T"` rather than as
    a number — or none at all, which is the shape rule 4 refuses and which bound_written below
    tells from this one. No arithmetic can be made of an unread bound from the call itself — a
    DeclaredArithmetic entry of DECLARED_LOOPS below is what writes its seconds instead, and
    rule 4 is what refuses an attempt with no bound at all — but it is not the same answer as
    0: the body holding it waits for something of unread length rather than for nothing. Two
    readers need the pair apart.
    retries_nothing, through script_waits, asks whether a script holds any call at all, and
    reading both as 0 is the hole review round 6 of dispatch://INFRA-1545 found there: a `curl -fsSL
    --retry 7 --retry-all-errors --max-time "$T"` inside a script made that script read as one
    that retries nothing, and a loop whose passes this check cannot read around a call of it
    was dropped rather than refused. uncounted_of below asks the same of a site written in the
    body itself: it keeps the sites answering None, which are waits of unread length, and
    drops the ones answering 0, which are waits of no length at all (review round 8 of dispatch://INFRA-1545)."""
    if site.silent:
        return 0
    if site.retry == "retry_fetch":
        return None if site.bound is None else ATTEMPT_FLOOR * site.bound + HELPER_WAIT
    if site.verb == "curl" and site.retry is None:
        if site.bound is None:
            return None
        retries, _, _, _ = curl_options(site.words)
        delay, max_time, readable = curl_waits(site.words)
        if retries is None or retries < RETRY_FLOOR or not readable:
            retries, delay, max_time = RETRY_FLOOR, None, None
        return (retries + 1) * site.bound + curl_ladder(retries, delay, max_time)
    return 0


def bound_written(site: Site) -> bool:
    """Whether anything bounds one attempt of this site, which is rule 4's question: the
    `timeout` or `retry_fetch` seconds around it, or curl's own `--max-time`. The number may
    still be unreadable — `--max-time "$T"` writes the option and leaves the seconds to the
    caller — and that is the pair rule 8 needs told apart from a site that writes no bound at
    all. Where its passes are read, a site writing none is rule 4's refusal, and rule 8 adds
    nothing to it: one fault reads better as one annotation, and the figure rule 8 would ask
    for is the very thing rule 4 is already asking the author to write. Inside a loop whose
    passes this check cannot read, the loop is a fault of rule 8's own, so that site is
    refused for it all the same, and bound_spelled below says what that refusal names: a
    bound written as a variable, or none this check finds (dispatch://INFRA-1545, round 37)."""
    if site.bounded:
        return True
    return site.verb == "curl" and site.retry is None and curl_options(site.words)[2]


def bound_spelled(site: Site) -> bool:
    """Whether one attempt of this site carries a bound in any spelling, a variable included,
    which is what rule 8 says of a site one_call answers None for: bound_written's answer, and
    also the seconds of a `retry_fetch` this check cannot read as a number, which bound every
    attempt of whatever verb it runs and where rule 4 reads no bound. A `retry_fetch` writing
    those seconds in any other spelling than a number or a variable is no site (odd_command),
    so they are a variable here, the word the refusal of that site quotes
    (variable_helper_bound). False is a site with no bound this check finds, and a refusal
    saying its bound was written as a variable told the author of a curl with no `--max-time`
    to write as a number a bound they had not written (dispatch://INFRA-1545, round 37). Until
    round 37's class search the helper's seconds counted here only around a curl writing
    `--max-time`, so `retry_fetch <what> "$T" -- gh api …` in a loop this check cannot count
    was told this check finds no bound on it, while the refusal of that site said its seconds
    were written as `$T` (dispatch://INFRA-1545, round 37)."""
    return bound_written(site) or site.retry == "retry_fetch"


def declared_arithmetic(site: Site) -> "DeclaredArithmetic | None":
    """The declaration that carries this site's own arithmetic, where one is written."""
    return next((entry for entry in DECLARED_LOOPS if entry.covers(site)), None)


def around(loops: Loops, lines: list[int]) -> tuple[int | None, int, int | None]:
    """Folding a cost at one position outwards through the loops around it: how many times they
    run it, what they sleep between their passes, and the loop whose passes could not be read
    where there is one. Each loop multiplies what is inside it and adds its own waits —
    `passes * inside + (passes - 1) * sleep` — so a call in a loop inside a loop is multiplied
    by both. A loop whose count this check never made stops the fold rather than taking a
    figure from somewhere else: that site is refused below, or answered in seconds by a
    DeclaredArithmetic entry, because a pass count nobody read is a pass count nobody checks."""
    factor = 1
    wait = 0
    for line in reversed(lines):
        passes = loops.passes.get(line)
        if passes is None:
            return None, 0, line
        wait = passes * wait + max(passes - 1, 0) * loops.sleep.get(line, 0)
        factor *= passes
    return factor, wait, None


def times_run(loops: Loops, line: int, stack: list[int] | None = None,
              seen: frozenset[str] = frozenset()) -> tuple[int | None, int, int | None]:
    """How many times a body runs what is written at `line`, what the loops around it sleep, and
    the loop that stopped the reading where one did. The loops around the line are the first
    half; the second is the function holding it, which runs once for each of its own calls, each
    of those multiplied the same way again. `seen` stops a function that reaches itself, which
    the shell would run until something failed and which must not hang this check."""
    factor, wait, blocked = around(loops, loops.enclosing(line) if stack is None else stack)
    if factor is None:
        return None, 0, blocked
    holder = loops.function_of(line)
    calls = loops.function_calls.get(holder, ()) if holder is not None else ()
    # A function this body defines and never calls is counted once, as a command of the body
    # itself is: its call is written in whatever sources this file.
    if holder is None or holder in seen or not calls:
        return factor, wait, None
    outer = 0
    outer_wait = 0
    for call in calls:
        times, waited, blocked = times_run(loops, call, None, seen | {holder})
        if times is None:
            return None, 0, blocked
        outer += times
        outer_wait += waited
    return outer * factor, outer * wait + outer_wait, None


def arithmetic_of(site: Site) -> int | None:
    """Rule 8's arithmetic for one site: one call of it for each time the step runs it, with
    the waits of the loops that run it. Two readings stop that sum, and a DeclaredArithmetic
    entry's own seconds answer both: a loop around the call whose passes this check cannot
    read, and a call one_call itself can put no figure on because its bound is written as a
    variable. Where no entry covers such a site the answer is None, which is a site refused
    below rather than a budget undercounted here.

    The unread bound is the whole of that second reading: there is no per-call number to
    multiply by anything, so the entry's figure is the site's whole contribution whether the
    passes around it were read or not. That site answered 0 here until round 9 of
    dispatch://INFRA-1545, which is what priced every tool install fetching through
    .github/scripts/ci-tools.sh at nothing, loop or no loop."""
    one = one_call(site)
    if one is not None:
        if not one:
            return 0
        if site.factor is not None:
            return site.factor * one + site.wait
    entry = declared_arithmetic(site)
    if entry is None:
        return None
    entry.used += 1
    if site.blocked is not None:
        entry.loops.add(site.blocked)
    if one is not None and entry.seconds < one:
        # The floor is about one call of a site that has a figure. Where one_call answered
        # None there is nothing to compare the entry with: that answer is the absence of the
        # very number this comparison would read, so the entry stands on its own `why` alone.
        entry.below[site.line] = one
    return entry.seconds


def added(total: int | None, more: int | None) -> int | None:
    """Two arithmetics summed, where either may be the unreadable one: a step holding a site
    this check cannot count has no figure at all, and a figure short by one loop would be worse
    than none."""
    return None if total is None or more is None else total + more


def functions_around(commands: list[Command]) -> list[bool]:
    """For each command, in the order CallScanner lists them, whether it sits in the body of a
    function, as function_bodies reads one. A script a function runs is run as many times as the
    function is called, from wherever it is called, which this reading does not follow, so a call
    there is one it does not read (dispatch://INFRA-1545, round 37). A definition function_bodies
    does not read leaves its body read as commands of the shell, and is refused by name where a
    fetch follows it (unread_shapes_of)."""
    return [holder is not None for holder in function_bodies(commands)[0]]


def call_of(commands: list[Command], in_function: list[bool],
            redirects: list[tuple[int, int]], pos: int, name: str) -> tuple[str, str]:
    """What the occurrence of `/<name>` at character `pos` of the body is, as (kind, form): a run
    or a source, the two recognised calls; a recognised non-call; or an occurrence this check
    does not read, with `form` naming the place it is written in. The recognised places are the
    ones this tree writes, and the header's rule 8 reading lists them (dispatch://INFRA-1545,
    round 37): a RUN, the path as the command word with nothing before it but assignment
    prefixes, at top level or as the whole command of a `$( … )`; a SOURCE, `. <path>` or
    `source <path>` with nothing before it, at top level; and a non-call, an argument of `echo`
    or `grep` whose output no shell runs. A comment never reaches here: occurrences_of leaves
    out what CallScanner read as one."""
    found = next(((index, word) for index, command in enumerate(commands)
                  for word, span in enumerate(command.spans)
                  if span and span[0] <= pos <= span[1]), None)
    if found is None:
        if any(start <= pos <= end for start, end in redirects):
            return CALL_UNREAD, "a redirect"
        return CALL_UNREAD, "a place this check reads no command at"
    index, at = found
    command = commands[index]
    words = command.words
    reduced = reduce_words(words)[0]
    wrappers = len(words) - len(reduced)
    ends = words[at].endswith("/" + name)
    head = PROGRAM.sub("", reduced[0]).rsplit("/", 1)[-1] if reduced else ""
    if command.context and command.context[-1] == "array":
        return CALL_UNREAD, "an array"
    if words[0] in ("for", "select", "case"):
        return CALL_UNREAD, f"the words of a `{words[0]}`"
    if at < wrappers:
        if ASSIGNMENT.match(words[at]):
            return CALL_UNREAD, "a variable assignment"
        return CALL_UNREAD, f"an argument of `{words[0]}`"
    argument = at - wrappers
    following = commands[index + 1].words if index + 1 < len(commands) else []
    shell = reduce_words(following)[0][:1]
    into_shell = bool(command.piped and shell
                      and PROGRAM.sub("", shell[0]).rsplit("/", 1)[-1] in INTERPRETERS)
    if head in NONCALL_COMMANDS and argument > 0:
        # Text piped into a shell is shell that shell runs, whatever command prints it: an
        # `echo <path> | bash` runs the script as surely as `cat <path> | bash` does, so the
        # pipe is asked about before the printing command is (dispatch://INFRA-1545, round 37).
        if into_shell:
            return CALL_UNREAD, "a pipe into a shell"
        # What a `$( … )` or backticks print is a word of the command around them, which the
        # shell runs where that word is the command's own, `$(echo <path>)`, or where that
        # command runs text, `eval "$(echo <path>)"`: such a path is no non-call
        # (dispatch://INFRA-1545, round 37). One printed into an assignment or into an argument
        # of any other command is printed, as this tree's `reader_lines=$(grep … <path>)` is.
        if command.context and command.context[-1] in ("dollar", "tick"):
            outer = next((other for other in commands[index + 1:]
                          if len(other.context) < len(command.context)
                          and any(span and span[0] <= pos <= span[1] for span in other.spans)),
                         None)
            outer_reduced = reduce_words(outer.words)[0] if outer is not None else []
            outer_at = (next((at for at, span in enumerate(outer.spans)
                              if span and span[0] <= pos <= span[1]), 0)
                        if outer is not None else 0)
            outer_wrappers = len(outer.words) - len(outer_reduced) if outer is not None else 0
            runner = (PROGRAM.sub("", outer_reduced[0]).rsplit("/", 1)[-1]
                      if outer_reduced else "")
            if outer is None or outer_at == outer_wrappers or (
                    outer_at > outer_wrappers
                    and runner in INTERPRETERS | {"eval", ".", "source"}):
                return CALL_UNREAD, (f"an argument of `{head}` inside a `$( … )` or backticks "
                                     "whose output the shell runs as a command")
        return CALL_NONE, f"an argument of `{head}`"
    if argument == 0:
        if not ends:
            return CALL_UNREAD, "a command word that holds more than the script's path"
        kind, places = CALL_RUN, ((), ("dollar",))
    elif head in (".", "source") and argument == 1 and ends:
        kind, places = CALL_SOURCED, ((),)
    elif into_shell:
        return CALL_UNREAD, "a pipe into a shell"
    else:
        return CALL_UNREAD, f"an argument of `{head}`"
    behind = next((word for word in words[:wrappers]
                   if kind == CALL_SOURCED or not ASSIGNMENT.match(word)), None)
    if behind is not None:
        return CALL_UNREAD, f"a command behind `{behind}`"
    if command.context not in places:
        context = command.context
        return CALL_UNREAD, ("a process substitution" if "proc" in context
                             else "a `( … )` subshell" if "paren" in context
                             else "a backtick substitution" if "tick" in context
                             else "a `$( … )`" if context == ("dollar",)
                             else "a `$( … )` inside another")
    if in_function[index]:
        return CALL_UNREAD, "a function body"
    return kind, ""


def occurrences_of(text: str, first_line: int, loops: Loops) -> list[Occurrence]:
    """Every occurrence of a script's path in a shell body, each read as call_of reads it, with
    the lines it sits on and how many times the body runs that line (dispatch://INFRA-1545,
    round 37), `loops` being what sites_of read of the same body. A step that runs a script
    waits for every attempt that script makes, once for each time the step runs it, so this is
    where rule 8 learns which scripts a body waits for.

    The occurrences are read off the text by the path each writes, those inside a comment left
    out, and then placed in the command holding them: until round 37 the path anywhere on such
    a line was a call, so a path in a message was charged as one and a path in a variable was
    charged where it was written rather than where it ran. A name SCRIPTS does not hold is
    nothing this check reads.

    What is a comment, and which lines a backslash continues, is CallScanner's reading, which
    is bash's: a comment runs from a `#` that starts a word outside quotes to the end of its
    physical line. Until round 37 both were read off the line's text, with every line ending in
    a backslash joined to the next first, which was wrong three ways (dispatch://INFRA-1545,
    round 37): `true;# <path>` and `(true)# <path>` were refused as a place no command is read
    at; a comment line ending in a backslash took the next line into the comment, so a run
    written there was charged nothing, where bash ends the comment at the newline and runs it;
    and a line starting with `#` inside a string spanning lines was left out as a comment, so a
    path written there, an argument of the command the string belongs to, was not refused.

    A source the body runs other than once, inside a loop, is read as no call: the library is
    refused by name where it fetches, rather than charged (dispatch://INFRA-1545, round 37). No
    step and no script of this tree sources a library inside a loop, so shell_arithmetic charges
    no loop's waits for a source. Charging them took a model of every path to the library, and
    the first one round 37 wrote, by the names along a path, undercounted a library sourced in a
    loop and again after it: `for p in 1 2 3; do . <lib>; sleep 60; done` and then `. <lib>`, the
    lib at 1050 s, waits 3270 s and was charged 3150 s.

    A heredoc body is left out with the comments, which is the reading without_heredocs makes:
    the paths a `cat` or a `python3 <<'PY'` body spells are paths something prints rather than
    runs — check-ci-images.sh's `MIRROR_SCRIPT`, which names the script a reader is told to
    run, and this file's own EXEMPTIONS and DECLARED_LOOPS, which name the files their entries
    cover. Counting those as calls gave the lint step that runs this check 15000 s of other
    scripts' arithmetic, and the ci-images job's two check steps mirror-ci-images.sh's 3000 s,
    none of which those steps wait for. A body fed to anything that may run it names a script
    that fetches only to be refused by name (unread_heredocs)."""
    stripped = without_heredocs(text)[0]
    # The expressions masked to as many characters, so a word's span is an offset of `stripped`.
    scanner = CallScanner(first_line)
    commands = scanner.scan(EXPRESSION.sub(lambda match: "E" * len(match.group(0)), stripped))
    in_function = functions_around(commands)
    # The line each physical line's logical line starts on: a line whose newline a backslash
    # continues, as CallScanner read it, goes on into the next. An occurrence held by no command,
    # in a redirect or where no command is read, is keyed there.
    starts: list[int] = []
    start = 0
    newline = -1
    for number, physical in enumerate(stripped.split("\n")):
        starts.append(start)
        newline += len(physical) + 1
        if newline not in scanner.continued:
            start = number + 1
    found: list[Occurrence] = []
    for match in SCRIPT_CALL.finditer(stripped):
        path = str(SCRIPTS / match.group(1))
        physical = stripped.count("\n", 0, match.start())
        if path not in script_files or any(begin <= match.start() < end
                                           for begin, end in scanner.comments):
            continue
        kind, form = call_of(commands, in_function, scanner.redirects, match.start(),
                             match.group(1))
        # The physical line the command holding the path starts on, which is the line the loops
        # around it are read against, as sites_of reads each command's: keyed to its logical
        # line, a run on the line after `echo start \` was read against the line before a loop
        # opening there, and charged one pass of it (dispatch://INFRA-1545, round 37).
        line = next((command.line for command in commands
                     if any(span and span[0] <= match.start() <= span[1]
                            for span in command.spans)), first_line + starts[physical])
        factor, wait, blocked = times_run(loops, line)
        if kind == CALL_SOURCED and (factor != 1 or wait):
            kind, form = CALL_UNREAD, "a source inside a loop"
        found.append(Occurrence(path, kind, line, first_line + physical, form, factor, wait,
                                blocked))
    return found


def recognised(found: list[Occurrence]) -> list[Occurrence]:
    """The runs and the sources among these occurrences: the calls rule 8 charges."""
    return [occurrence for occurrence in found if occurrence.kind in (CALL_RUN, CALL_SOURCED)]


def blocked_calls(path: str, calls: list[Occurrence]) -> list[Uncounted]:
    """The scripts one body runs inside a loop whose passes this check cannot read: whoever runs
    that body waits for the whole of each script once per pass, and the passes are unread. One
    item for each script, at the first such call of it.

    Every such call is listed here, the ones that cost nothing included, because what a script
    costs cannot be asked at this point: the scripts are read after the steps that run them, so
    script_cost, script_reach and script_waits are still being filled while this runs. That is
    why `one` is None for every item here and why the kind is written down instead:
    waits_for_nothing below asks its question of these items alone, and asks it by the kind,
    dropping the calls that turn out to wait for no attempt at all."""
    first: dict[str, Occurrence] = {}
    for call in calls:
        if call.factor is None:
            first.setdefault(call.target, call)
    return [Uncounted(path, call.line, target, call.loop, None, UNCOUNTED_SCRIPT_CALL)
            for target, call in sorted(first.items())]


def unread_shapes_of(path: str, text: str, first_line: int, step: str, loops: Loops,
                     found: list[Site], called: list[Occurrence]
                     ) -> list[tuple[int, str, bool, set[str], set[str], set[str]]]:
    """The shapes of one shell body this check refuses by name rather than reads
    (dispatch://INFRA-1545, round 37): each function definition function_bodies, structure and
    Scanner name, each loop structure names, each call of a function structure names, each
    heredoc unread_heredocs names, each `bash -c`, `sh -c` or `eval` text sites_of_command
    names, each form whole_text holds, each command after a function's closing `}` on its line,
    each command odd_command names and each definition of the helper's own name. Each is (line,
    sentence, waits, targets, names, sources): the line it is written on, what the refusal says
    of it, whether a fetch it can hide is written in it, the scripts it can hide a call of, the
    names of the commands in its reach and the libraries this body sources. It is refused where
    it holds such a fetch, one of those scripts fetches, or one of those names is a function one
    of those libraries defines that fetches, which is known only once every script is read:
    until round 37 a call of a library's fetching function inside the reach of a function called
    after `then`, or of one defined with the `function` keyword, was neither, and the shape was
    passed with that call charged once (dispatch://INFRA-1545, round 37). A function's reach is
    every fetch and every recognised call from its line to the last its body can reach, since
    its body follows its name, and the body of every function a call in that reach calls, in
    turn, since a fetch there runs once for each of those calls; a call's is the reach of the
    function it calls, from its definition; a loop's, and that of each form whole_text holds, is
    the whole text, since what it hides is a count of lines; a command's after a closing `}` is
    its own line's; a heredoc's is its body, read as shell for this question alone, where any
    fetch counts, since no other rule reads that body; a text's is the fetches read in it, each
    a site on the command's line; and a `retry_fetch` that fetches nothing, and a definition of
    the helper's own name, are refused wherever they are written. A script path in such a text
    is refused as an argument of the shell already."""
    end = first_line + text.count("\n")
    sources = {call.target for call in called if call.kind == CALL_SOURCED}

    def reach(start: int, last: int) -> set[int]:
        lines = set(range(start, last + 1))
        grown = True
        while grown:
            grown = False
            for name, calls in loops.function_calls.items():
                span = set(range(loops.function_span[name][0], loops.function_span[name][1] + 1))
                if not span <= lines and any(call in lines for call in calls):
                    lines |= span
                    grown = True
        return lines

    def held(within: set[int]) -> tuple[bool, set[str], set[str], set[str]]:
        """What a reach holds: a fetch, the scripts it calls, the names its commands call, and
        the libraries whose functions those names may be."""
        names = {reduced[0] for command in loops.commands if command.line in within
                 for reduced in [reduce_words(command.words)[0]] if reduced}
        return (any(site.line in within and one_call(site) != 0 for site in found),
                {call.target for call in called if call.line in within}, names, sources)

    whole = (any(one_call(site) != 0 for site in found), {call.target for call in called},
             set(), set())
    shapes = []
    named: set[int] = set()
    for line, last, shape in loops.odd_functions:
        if line in named:
            # A definition two shapes describe, `function name () {`, is refused once.
            continue
        named.add(line)
        shapes.append((line, FUNCTION_REFUSAL.format(shape=shape,
                                                     cure=FUNCTION_CURES.get(shape, FUNCTION_CURE)),
                       *held(reach(line, end if last is None else last))))
    for line, name, prefix in loops.odd_calls:
        start, last = loops.function_span[name]
        shapes.append((line, CALL_REFUSAL.format(name=name, prefix=" ".join(prefix)),
                       *held(reach(start, last))))
    for line, (shape, why, cure) in dict.fromkeys(loops.odd_loops):
        shapes.append((line, LOOP_REFUSAL.format(shape=shape, why=why, cure=cure), *whole))
    for line, sentence in dict.fromkeys(loops.whole_text):
        shapes.append((line, sentence, *whole))
    for line, name in loops.after_close:
        shapes.append((line, AFTER_CLOSE_REFUSAL.format(name=name), *held(reach(line, line))))
    if path != HELPER:
        shapes += [(line, HELPER_DEFINED, True, set(), set(), set())
                   for line in loops.defines_helper]
    for line, sentence, waits, helper in loops.odd_commands:
        # A text defining its own `retry_fetch` runs that one at each call, which the refusal
        # of the definition says, and a sentence about the helper's signature would not be true.
        if not (helper and loops.defines_helper):
            shapes.append((line, sentence, waits, set(), set(), set()))
    for number, body, sentence in unread_heredocs(text):
        held_sites = sites_of(path, body, first_line + number + 1, step)[0]
        shapes.append((first_line + number, sentence, bool(held_sites),
                       {str(SCRIPTS / match.group(1)) for match in SCRIPT_CALL.finditer(body)}
                       & script_files, set(), set()))
    for line, form, why in loops.shell_texts:
        # A fetch in the text is a site on the command's own line (sites_of_command).
        shapes.append((line, SHELL_TEXT_REFUSAL.format(form=form, why=why),
                       any(site.line == line and one_call(site) != 0 for site in found), set(),
                       set(), set()))
    return shapes


def commands_of(body: str, first_line: int) -> tuple[list[Command], Loops,
                                                     list[tuple[int, int]]]:
    """The simple commands of a shell body, what rule 8 reads of its loops, and the probe
    spans. The braces Scanner reads where bash reads a word, and each definition of a function
    named `retry_fetch` in any spelling, are kept with the rest of what is refused by name
    (dispatch://INFRA-1545, round 37)."""
    text, _ = without_heredocs(EXPRESSION.sub("EXPR", body))
    scanner = Scanner(first_line)
    commands = scanner.scan(text)
    loops = structure(commands, text)
    loops.odd_functions += [(line, None, FUNCTION_BLANK) for line in scanner.blank_parens]
    loops.whole_text += [(line, BRACE_REFUSAL.format(brace=brace))
                         for line, brace in scanner.odd_braces]
    loops.defines_helper = [
        command.line for command in commands
        if "retry_fetch()" in command.words
        or any(word == "function" and following in ("retry_fetch", "retry_fetch()")
               for word, following in zip(command.words, command.words[1:]))
        or (command.words[-1] == "retry_fetch" and command.line in scanner.blank_parens)]
    loops.commands, loops.text, loops.first_line = commands, text, first_line
    return commands, loops, probe_spans(text)


def sites_of(path: str, body: str, first_line: int, step: str,
             pipefail: bool = True) -> tuple[list[Site], Loops]:
    """Every fetch of one shell body, and what rule 8 read of that body's loops. `pipefail` is
    whether the shell that runs it sets `-o pipefail`: where it does, a pipeline carries the
    failure of every command in it and rule 9 has nothing to refuse. That is the default,
    because a *.sh and a Dockerfile RUN are read with no step's `shell:` to ask about and each
    sets its own flags. A command odd_command names is kept for its refusal, and one that calls
    the helper in a form that fetches nothing holds no site (dispatch://INFRA-1545, round 37)."""
    commands, loops, probes = commands_of(body, first_line)
    found: list[Site] = []
    for command in commands:
        if any(start <= command.offset < end for start, end in probes):
            continue
        odd = odd_command(command.words)
        if odd is not None:
            sentence, helper = odd
            loops.odd_commands.append((command.line, sentence,
                                       helper or verb_in(command.words) is not None, helper))
            if helper:
                continue
        held = sites_of_command(path, command, loops, step)
        for site in held:
            site.helper_bound = helper_bound_word(command.words)
        found += held
    if pipefail:
        for site in found:
            site.piped = False
    found = unpacks_dropped(found)
    # A site whose loops write no count leaves whatever step runs it without a figure here:
    # arithmetic_of asks the DeclaredArithmetic entries for its seconds, and uncounted_of below
    # hands the step's budget the ones no entry answers.
    for site in found:
        site.factor, site.wait, site.blocked = times_run(loops, site.line, site.loops)
    return found, loops


def uncounted_of(found: list[Site]) -> list[Uncounted]:
    """The sites of one body this check can put no figure on and no DeclaredArithmetic entry
    writes the seconds of: a retried call inside a loop whose passes it cannot read, and a
    retried call whose own per-attempt bound is written as a variable rather than as a number.
    A step whose budget reaches one of these is refused in its place — in the loop's, or in
    the bound's — and a site no budget reaches costs nobody anything and is left alone, rule 2
    being what reads a loop wherever it sits.

    A site one_call answers 0 for is left out: a `mise x`, or a verb whose attempts neither the
    helper nor curl itself makes, is a wait of no length, and a loop around no wait is a loop
    rule 8 has nothing to compare a budget with. A site it answers None for is kept, because
    that is a wait of unread length and not an absent one. Inside a loop it has no pass count
    either, so both halves of the step's wait are unreadable; with the passes read it is still
    one wait nobody can price, and pricing it at 0 is what let every tool install of
    .github/scripts/ci-tools.sh sit under a budget of one minute (review round 8 of dispatch://INFRA-1545
    and review round 9 of dispatch://INFRA-1545). Dropping it inside a loop priced the step's whole wait at 0 and refused nobody
    for it (review round 8 of dispatch://INFRA-1545). A site rule 4 refuses for writing no
    bound at all is left to rule 4 where its passes are read, which is the bound_written
    question above: what rule 8 would ask its author for is the figure rule 4 is already asking
    them to write. Inside a loop whose passes this check cannot read it is kept, because that
    loop is rule 8's own fault and rule 4 says nothing of it, and it is marked `unbounded`
    where bound_spelled finds no bound in any spelling, so its refusal says that: it said the
    bound was written as a variable, and told the author to write as a number a bound they had
    not written (dispatch://INFRA-1545, round 37). The refusals below read the kind field, not
    `one`, to tell these items from blocked_calls's script calls, so a None written here blurs
    nothing. A curl writing its bound in a form curl_bound_refusal names is refused by name in
    its own place, and is no item here: its bound was said to be written as a variable, which
    `--max-time 1.5` is not (dispatch://INFRA-1545, round 37)."""
    kept = []
    for site in found:
        if site.path in DOCKERFILES:
            continue
        if (site.verb == "curl" and site.retry is None
                and curl_bound_refusal(site.words) is not None):
            continue
        one = one_call(site)
        if declared_arithmetic(site) is not None:
            continue
        if site.factor is None:
            if one != 0:
                kept.append(Uncounted(site.path, site.line, site.verb, site.blocked, one,
                                      UNCOUNTED_SITE,
                                      unbounded=one is None and not bound_spelled(site),
                                      helper=one is None and site.retry == "retry_fetch"))
        elif one is None and bound_written(site):
            # No loop in the item: `loop` names the one whose passes could not be read, and
            # this site's were read, whether it sits in a loop or in none. The refusal below
            # names the bound instead, which is the half nobody can read here.
            kept.append(Uncounted(site.path, site.line, site.verb, None, None,
                                  UNCOUNTED_UNREAD_BOUND))
    return kept


def unpacks_dropped(found: list[Site]) -> list[Site]:
    """The sites with the apt unpack phases taken out. An `apt-get install` with no `-d` that
    follows an `apt-get install -d` in the same body installs an archive already on disk and
    takes no network: the download before it is the half that retries, which is what
    e2e.yaml's package step and ci.yaml's `Install tmux` step each say where they do it."""
    downloaded = False
    kept = []
    for site in found:
        if site.verb == "apt-get install":
            only = any(word in APT_DOWNLOAD_ONLY for word in site.words)
            if downloaded and not only:
                continue
            downloaded = downloaded or only
        kept.append(site)
    return kept


def sites_of_command(path: str, command: Command, loops: Loops,
                     step: str, retry: str | None = None, bounded: bool = False,
                     depth: int = 0, bound: int | None = None) -> list[Site]:
    words, wrapped, wrapped_bound, wrapped_seconds = reduce_words(command.words)
    retry = retry or wrapped
    bounded = bounded or wrapped_bound
    if bound is None:
        bound = wrapped_seconds
    if not words or depth > 3:
        return []
    name = PROGRAM.sub("", words[0]).rsplit("/", 1)[-1]
    if name in INTERPRETERS or name == "eval":
        # The text a shell runs is the word after its first option holding a `c`, whatever the
        # options around it, so a fetch in it is a site to every rule. Rule 8 reads it in one
        # form, `bash -c '<text>'` holding no loop and no function, which is the form this tree
        # writes, and refuses any other by name where the text fetches (unread_shapes_of): until
        # round 37 the options before a `-c` were read with a model of `-o`'s value alone, so
        # the text after `bash -O extglob -c` was no site at all (dispatch://INFRA-1545, round
        # 37).
        flag = 0 if name == "eval" else next(
            (at for at, word in enumerate(words) if re.fullmatch(r"-[A-Za-z]*c[A-Za-z]*", word)),
            len(words))
        if flag + 1 >= len(words):
            return []
        script = " ".join(words[1:]) if name == "eval" else words[flag + 1]
        inner = Scanner(command.line).scan(script)
        held = structure(inner, script)
        form = " ".join(words[:flag + 1])
        if held.passes or held.function_span or held.odd_functions or held.odd_loops:
            loops.shell_texts.append((command.line, form, "whose text holds a loop or a function"))
        elif name != "bash" or words[1] != "-c":
            loops.shell_texts.append((command.line, form, "a form other than `bash -c`"))
        elif held.whole_text:
            # An alias, or a loop's own word bash reads as another word, each refused by name
            # in a step, written in the one form of text this check reads
            # (dispatch://INFRA-1545, round 37).
            loops.shell_texts.append((command.line, form,
                                      "whose text holds an alias, or a loop's own word that "
                                      "bash reads as another word"))
        found = []
        for sub in inner:
            sub.line = command.line
            sub.offset = command.offset
            # The text runs once for each time the command does, inside the loops around it,
            # which until round 37 a fetch in it left out (dispatch://INFRA-1545, round 37).
            sub.loops = command.loops
            # A `bash -c '…' | tar` pipes the interpreter, so a fetch inside it loses its own
            # status the same way a fetch written in the pipeline would.
            sub.piped = sub.piped or command.piped
            found += sites_of_command(path, sub, loops, step, retry, bounded, depth + 1,
                                      bound)
        return found
    if name in loops.function_loop:
        # A loop that runs what it is handed: the verb among the call's words is that loop's fetch.
        held = verb_in(words[1:])
        if not held:
            return []
        verb, rest = held
        _, _, inner_bound, inner_seconds = reduce_words(rest)
        return [Site(path, command.line, verb, step, rest, retry, bounded or inner_bound,
                     command.loops + [loops.function_loop[name]],
                     bound if bound is not None else inner_seconds, piped=command.piped)]
    silent = silent_verb_of(words)
    if silent:
        # Rule 7. A bare one is no fetch this check holds to anything; a retried one is the
        # shape the rule refuses, and it is recorded so the verdict can name it.
        if not retry:
            return []
        return [Site(path, command.line, silent, step, words, retry, bounded, command.loops,
                     bound, silent=True, piped=command.piped)]
    verb = verb_of(words)
    if not verb:
        return []
    if verb in ("curl", "wget") and loopback_only(words):
        return []
    if verb == "curl" and retry is None:
        # curl makes the attempts itself, and `--max-time` is what bounds each one of them.
        bound = curl_bound(words) or bound
    return [Site(path, command.line, verb, step, words, retry, bounded, command.loops, bound,
                 piped=command.piped)]


def loopback_only(words: list[str]) -> bool:
    """Whether every address this fetch names is the loopback: a readiness poll of a process the
    step itself started, which is no download."""
    urls = [word for word in words if "://" in word]
    if not urls:
        return False
    return all(any(host in url for host in LOOPBACK) for url in urls)


# ---------------------------------------------------------------- the exceptions


class Exemption:
    """One site R2 excepts, by the file, the job and the step it sits in and the verbs it covers.
    `why` is what a reader of a stale entry is told the entry was for."""

    def __init__(self, path: str, job: str | None, step: str | None,
                 verbs: tuple[str, ...] | None, why: str) -> None:
        self.path = path
        self.job = job
        self.step = step
        self.verbs = verbs
        self.why = why
        self.used = 0

    def covers(self, site: Site, job: str | None) -> bool:
        return (
            site.path == self.path
            and (self.job is None or self.job == job)
            and (self.step is None or self.step == site.step)
            and (self.verbs is None or site.verb in self.verbs)
        )


GHCR_WHY = ("a ghcr.io pull of a package this repository publishes: a failed pull is a fault of "
            "this repository's own package and must be seen, not retried over")
EXEMPTIONS = [
    Exemption(".github/actions/pull-test-images/action.yml", None, None,
              ("docker pull",), GHCR_WHY),
    Exemption(".github/workflows/ci-job-envoy-client.yaml", "envoy-client",
              "Pre-pull the NATS image the credential tests run", ("docker pull",), GHCR_WHY),
    Exemption(".github/workflows/ci-job-download-retry-images.yaml", "download-retry-images",
              "Pull every base image the four builds read", ("docker pull",), GHCR_WHY),
    Exemption(".github/workflows/dispatch-browser.yaml", "visual",
              "Pull the pinned Playwright copy", ("docker pull",), GHCR_WHY),
    Exemption(".github/workflows/dispatch-browser.yaml", "visual",
              "Pull the visual stack's Postgres copy", ("docker pull",), GHCR_WHY),
]


# ------------------------------------ what a reader declares where this check's reading stops


class DeclaredArithmetic:
    """One figure a reader writes down where rule 8's own reading stops, matched to a site by
    the file, the step and the verb it names. It answers for a site whose pass count this
    check cannot resolve — a `while read` over a list the run makes, a `while :` over the
    pages of a route — and for a site whose per-attempt bound is written as a variable rather
    than as a number, which is a call this check can put no seconds behind however plainly it
    can count the passes. It answers in seconds rather than in passes, because a loop this
    check could not read is a loop nobody has priced, and a pass number written over it is an
    excuse rather than a declaration; and because a site whose bound is unread has no per-call
    figure for a pass count to multiply at all. It writes the seconds that site contributes to
    the budget of the step holding it, and `why` writes that figure out as a sum, so a reader
    checks the arithmetic rather than taking the number on trust. This check uses it in place
    of the arithmetic it could not compute.

    An entry matching nothing fails this check, as a stale exception does, so a declaration
    cannot outlive what it was written for; an entry two loops of one file match fails as well,
    because it writes one figure and the second loop would take it without anybody writing it
    one; and a figure below one call of the site is refused with that floor, because a site
    costs at least one call of itself, whatever the run finds for it to do. That floor is
    asked of the sites that have a figure: where the bound is the unread half there is no one
    call to compare, and the entry's own `why` is the whole of what a reader checks."""

    def __init__(self, path: str, step: str | None, verb: str, seconds: int, why: str) -> None:
        self.path = path
        self.step = step
        self.verb = verb
        self.seconds = seconds
        self.why = why
        self.used = 0
        self.loops: set[int] = set()
        # The sites whose one call costs more than this entry's whole figure, each with that
        # floor. Each of them is this entry refused by name.
        self.below: dict[int, int] = {}

    def covers(self, site: Site) -> bool:
        # A step's site is matched by its step's name and a script's, read with no step, by
        # None, the two forms this tree writes; until round 37 None matched every step of a
        # workflow too (dispatch://INFRA-1545, round 37).
        return (site.path, self.step or "-", site.verb) == (self.path, site.step, self.verb)

    def names(self) -> str:
        """How a refusal names this entry."""
        return self.path + (f" step '{self.step}'" if self.step else "") + f" `{self.verb}`"

    def figure(self) -> str:
        return f"{self.seconds} s"


# Two readings of rule 8 stop at something this table answers, and the entries below are of
# both kinds.
#
# A loop in this tree that rule 8 cannot read is one whose passes are as many as the run
# finds work: a `while read` over a list the run wrote, a `while :` over the pages of a route.
# Each one's figure is the passes its own prose argues for at one call of that site, and the sum
# is written out beside it so that a reader checks it rather than takes it. What every entry of
# one pass says, each in its own words: the loop runs the call once for each thing the run found
# to do, the budget is written for the one the common run finds, and a run where every pass
# exhausts its five attempts is not the transient fault a retry exists to survive but an outage
# of the host, which ci-budgets.md's Budgets note leaves to the job's own ceiling rather than to the
# step's.
#
# A call whose per-attempt bound is written as a variable is the other kind, and it needs no
# loop around it to need an entry: what one call of it takes is the figure one_call cannot
# make, so the entry writes the whole of what that site contributes and the sum it comes from.
# One such entry's figure is one figure for every caller of the site, which is the limit of
# this mechanism and is said out loud in the `why` that has to live with it.
DECLARED_LOOPS: list[DeclaredArithmetic] = [
    DeclaredArithmetic(".github/workflows/ci.yaml",
                       "Force-cancel the runs of the groups the queue removed", "gh api", 300,
                       "one force-cancel per group the queue removed, and the budget is "
                       "written for the one a common run removes: 1 x (5 x 30 + 150) = 300 s. "
                       "That step's own comment derives its budget from that one ladder beside "
                       "about 3 s a call over the at most 21 calls the 20 groups the queue "
                       "builds at once can need"),
    DeclaredArithmetic(".github/workflows/dequeued.yaml",
                       "Cancel the removed group's ci.yaml run", "gh api", 300,
                       "one force-cancel per ci.yaml merge-group run still going at the "
                       "removed group's head, and the queue starts one run for a group: a "
                       "re-run is another attempt of that run and not a second one. "
                       "1 x (5 x 30 + 150) = 300 s"),
    DeclaredArithmetic(".github/workflows/main-bypass-check.yaml",
                       "Start ci.yaml when this push's head was never tested",
                       "gh workflow run", 300,
                       "one dispatch. .github/scripts/main-bypass-check.sh's header says it "
                       "prints one line on standard output, `dispatch <sha>`, naming the head "
                       "when it reached main untested, so the loop over its acts runs this "
                       "call once. 1 x (5 x 30 + 150) = 300 s"),
    # A script site is read with no job and no step, so these entries name no step: the call at
    # the `for path in scripts:` loop below passes step="-".
    DeclaredArithmetic(".github/scripts/check-pr-text.sh", None, "gh api", 900,
                       "the page read of one list route, over the three lists this script "
                       "reads. read_list's loop reads pages until a short one, which is no "
                       "count this check can make, and a pull request carrying more than 100 "
                       "reviews or more than 100 comments reads another page at the "
                       "per_page=100 the call asks for; the budgets reaching it are written "
                       "for the one page the common pull request answers. The "
                       "`for ((i = 0; i < ${#lists[@]}; i += 2))` under it runs read_list "
                       "once for each of three lists, so three lists at one page each is "
                       "3 x 1 x (5 x 30 + 150) = 900 s. The two reads this script writes "
                       "outside that loop are counted where they are written and are no part "
                       "of this figure"),
    DeclaredArithmetic(".github/scripts/ci-ok-reconcile.sh", None, "gh api", 900,
                       "one PATCH per other ci-ok on the head that needs one, and three is "
                       "what the ci-ok step's budget is written for: 3 x (5 x 30 + 150) = "
                       "900 s. A merge-group run posts one ci-ok per job whose own result "
                       "decides the group (ci-ok-report.sh), so a run that fails in three jobs "
                       "leaves three posts on the head; the two posted ci-oks that script's "
                       "header measures on open heads are one per head. This one is written "
                       "high on purpose: the script's refusals only warn, and a step the "
                       "runner cuts fails the ci-ok job, which turns a passing required check "
                       "red"),
    # Not a count of passes: one time bound around the whole loop. runner_shutdown_kills reads
    # the annotations of every check run of every failed job it is asked about, which is as many
    # as the run failed, and stops starting reads once its deadline, _runner_shutdown_read_seconds,
    # has passed (main's #2945 wrote the loop; the deadline is this merge's).
    DeclaredArithmetic(".github/scripts/runner-shutdown.sh", None, "gh api", 480,
                       "the annotations loop's own deadline, 180 s, which that file derives "
                       "from its measurements (one read 3.1 s to 4.3 s; at most 17 reads in "
                       "the 97 failed ci.yaml runs read, 73 s), plus one read's whole ladder, "
                       "since the last read may start just before the deadline and run every "
                       "attempt: 180 + (5 x 30 + 150) = 480 s. The check-runs read before the "
                       "loop is counted where it is written and is no part of this figure"),
    DeclaredArithmetic(".github/scripts/mirror-ci-images.sh", None, "crane copy", 1650,
                       "one copy per row of .github/ci-images.yaml the package does not hold, "
                       "and the ci-images step's budget is written for the rows a run copies, "
                       "at the under 75 s a row that file's own comment derives: "
                       "1 x (5 x 300 + 150) = 1650 s"),
    DeclaredArithmetic(".github/scripts/mirror-ci-images.sh", None, "crane digest", 450,
                       "the digest read that confirms the copy above it, once per row copied: "
                       "1 x (5 x 60 + 150) = 450 s"),
    DeclaredArithmetic(".github/scripts/mirror-ci-images.sh", None, "crane config", 900,
                       "the Go check of one row, once per tagged library/golang row, of which "
                       ".github/ci-images.yaml holds one. The read sits "
                       "in image_go(), which check_go() calls, and the loop runs check_go once "
                       "for the row the package already holds and once for the row it is about "
                       "to copy — two written calls of which a pass runs one, so the figure is "
                       "twice what a row costs: 2 x (5 x 60 + 150) = 900 s"),
    # Not a loop. ci_tools_curl writes `--max-time "$max_time"`, the row's own field, so one
    # call of this one curl has no figure and rule 8 priced every tool install of this tree at
    # nothing without this entry (review round 9 of dispatch://INFRA-1545).
    DeclaredArithmetic(".github/scripts/ci-tools.sh", None, "curl", 1281,
                       "the three fetches one failing `ci_tools_fetch` of a ghcr-mirror row "
                       "with a release asset of its own makes: the asset, then the anonymous "
                       "pull token and then the blob of the copy (ci_tools_fetch, "
                       "ci_tools_ghcr_token, ci_tools__fetch_ghcr). What one of those ladders "
                       "costs is decided by `--retry-max-time 300`, which that file writes "
                       "beside `--retry 7`, and its own block on that option is where the "
                       "per-ladder ceiling is derived: a mixture of quick failures and one "
                       "stalled attempt is the expensive shape, and 427 s is the ceiling it "
                       "reaches. This entry reads that figure rather than deriving it again, "
                       "and 3 x 427 = 1281 s. "
                       "One figure cannot be true of every caller of one helper, and this one "
                       "is written for the worst of the installs: .github/actions/install-crane "
                       "and .github/actions/install-changie each fetch such a row, and "
                       ".github/actions/install-jj's mise step fetches the mise row the same "
                       "way. A row whose `primary` is empty costs the second source alone - "
                       "2 x 427 = 854 s for the jj row's token and blob, and one npm tarball, "
                       "427 s, for a bun row - and a step that sources this file twice is "
                       "charged this figure twice, so an install of one row is over-priced. "
                       "Two callers loop over it, and each is priced by a DECLARED_LIBRARIES "
                       "entry instead. .github/scripts/mirror-ci-tools.sh makes nine of these "
                       "requests for each ghcr-mirror row it fills, and its entry is a cold "
                       "fill, 14959 s, which .github/ci-budgets.md's \"The ci-images job's "
                       "ceiling and the tool copy\" derives. "
                       ".github/scripts/ci-tools-proof.sh's `stage` calls ci_tools_fetch once "
                       "per row of its STAGED_ROWS, a call of a function this file defines, "
                       "which this check charges as the file's one charge: the twelve ladders "
                       "those five rows make are the staging step's entry, 5124 s, derived "
                       "from this entry's figures"),
]


class DeclaredLibrary:
    """What one fetching library contributes to a step, written down where rule 8 charges it
    once and the step can run its fetches more than once (dispatch://INFRA-1545, round 37): a
    library the step reaches along more than one path, sourced by the step and by the scripts
    it runs; one it reaches along one path more than once, as a script sourcing it that the
    step runs in more than one place; and a library whose fetching functions a body calls more
    than once, or inside a loop whose passes this check cannot read, after sourcing it. Each is
    refused by name unless an entry covers it. `path` and `step` name the step, its file and
    its name; or `path` names the script or the composite action whose calls are the reading,
    with `step` None, for every step reaching that file — a step's own entry wins over a
    file's. `seconds` replaces what that library is charged for the step, which is its own
    arithmetic at its most expensive path, and `why` writes that figure out as a sum. An entry
    naming a file this tree holds and matching nothing fails this check, as a stale declaration
    does."""

    def __init__(self, path: str, step: str | None, library: str, seconds: int,
                 why: str) -> None:
        self.path = path
        self.step = step
        self.library = library
        self.seconds = seconds
        self.why = why
        self.used = 0

    def names(self) -> str:
        """How a refusal names this entry."""
        return self.path + (f" step '{self.step}'" if self.step else "") + f" {self.library}"


# The tree's own readings of these three shapes, each at the worst case its `why` derives, which
# is the figure the budget holding it writes (dispatch://INFRA-1545, round 37).
TOOL_FALLBACK = ".github/workflows/ci-job-tool-fallback.yaml"
CI_TOOLS = ".github/scripts/ci-tools.sh"
RUNNER_SHUTDOWN = ".github/scripts/runner-shutdown.sh"
DECLARED_LIBRARIES: list[DeclaredLibrary] = [
    DeclaredLibrary(TOOL_FALLBACK,
                    "Stage the correct bytes of every row a case serves, before the fault",
                    CI_TOOLS, 5124,
                    "`stage` loops over ci-tools-proof.sh's STAGED_ROWS - crane, changie, mise, jj "
                    "and bun-linux-x64 - and calls ci_tools_fetch once for each, the figures of "
                    "the DECLARED_LOOPS entry for ci-tools.sh's curl: a row whose primary is a "
                    "release asset of its own costs 1281 s, which is crane, changie and mise; the "
                    "jj row's primary is empty, 854 s; and bun-linux-x64 costs its one npm "
                    "tarball, 427 s. 3 x 1281 + 854 + 427 = 5124 s, the sum the step's own "
                    "comment writes for its 86 minutes"),
    DeclaredLibrary(".github/scripts/ci-tools-proof.sh", None, CI_TOOLS, 1281,
                    "ci_tools_fetch and ci_tools_fetch_primary are called in `stage`'s loop and "
                    "nowhere else in this script, and only the staging step passes `stage`, "
                    "which its own entry prices; every other subcommand reaches the library's "
                    "fetches through nothing it calls, so a step running one is charged the "
                    "library once, as it sources it: 1 x 1281 s, the figure each such step's "
                    "comment writes for its 22 minutes"),
    DeclaredLibrary(TOOL_FALLBACK, "Check every tool is the version its row pins", CI_TOOLS,
                    1281,
                    "the step sources ci-tools.sh and runs ci-tools-proof.sh `version-answers` "
                    "for each of eight rows, and `version-answers` compares a printed version "
                    "with the row and fetches nothing, so the library's fetches are the step's "
                    "once: 1 x 1281 s, the figure the step's comment writes for its 22 minutes"),
    DeclaredLibrary(TOOL_FALLBACK, "Print what every tool answers now", CI_TOOLS, 1281,
                    "the step sources ci-tools.sh and runs ci-tools-proof.sh `version-answers` "
                    "for crane, changie and jj, which fetches nothing, and calls "
                    "ci_tools_verify, a digest read off disk: 1 x 1281 s, the figure the step's "
                    "comment writes for its 22 minutes"),
    # A script that sources the library, run three times by one step, each run a shell of its
    # own: the one tree instance of that shape, counted at b5aba1e5 with this check's own
    # survey instrumented (dispatch://INFRA-1545, round 37).
    DeclaredLibrary(TOOL_FALLBACK,
                    "Seed mise's install of jj with the staged binary, one byte changed",
                    CI_TOOLS, 1281,
                    "the step runs ci-tools-proof.sh three times, each run a shell of its own "
                    "that sources ci-tools.sh: `payload-path jj` reads the row's second source "
                    "and its digest off .github/ci-tools.yaml (ci_tools_field, ci_tools_digest), "
                    "`flip-last-byte` changes the copy's last byte with python3, and "
                    "`seed-mise-jj` installs that copy in mise's directory. None of the three "
                    "calls a function that fetches, so the runs fetch 0 + 0 + 0 = 0 s, and the "
                    "step is charged the library once, as it sources it, the charge every step "
                    "running a subcommand of the rig but `stage` carries: 1 x 1281 s, the figure "
                    "the step's comment writes for its 22 minutes"),
    DeclaredLibrary(TOOL_FALLBACK, "Check the package holds the blob of every mirrored row",
                    CI_TOOLS, 2015,
                    "the anonymous pull token at 60 s an attempt, 427 s, and one HEAD per "
                    "ghcr-mirror row of .github/ci-tools.yaml at 30 s an attempt, 397 s each over "
                    "the four such rows, 1588 s, both per-ladder figures ci-tools.sh's own "
                    "`--retry-max-time` block writes: 427 + 1588 = 2015 s, the sum the step's "
                    "comment writes for its 34 minutes"),
    DeclaredLibrary(".github/scripts/mirror-ci-tools.sh", None, CI_TOOLS, 14959,
                    "a cold fill, which .github/ci-budgets.md's \"The ci-images job's ceiling "
                    "and the tool copy\" derives: a ghcr-mirror row costs nine requests, the "
                    "two of them carrying the asset's bytes at 427 s and seven at 397 s, the "
                    "per-ladder figures ci-tools.sh's own `--retry-max-time` block writes, "
                    "2 x 427 + 7 x 397 = 3633 s; the four ghcr-mirror rows with the one token "
                    "request are 427 + 4 x 3633 = 14959 s"),
    DeclaredLibrary(".github/actions/ci-ok-report/action.yml", None, RUNNER_SHUTDOWN, 1560,
                    "two calls of runner_shutdown_kills, each a check-run read, 5 x 30 + 150 = "
                    "300 s, and the annotations loop runner-shutdown.sh's DECLARED_LOOPS entry "
                    "prices, 480 s: ci-ok-report.sh's own, when this job failed, and ci-ok.sh's, "
                    "which that script then runs in a process of its own for the run's failed "
                    "jobs, so the check-run read is not shared. 2 x (300 + 480) = 1560 s, the "
                    "term .github/ci-budgets.md's Budgets note writes for the ci-ok report"),
    DeclaredLibrary(".github/scripts/ci-ok-report.sh", None, RUNNER_SHUTDOWN, 780,
                    "its two calls of shutdown_killed, which calls runner_shutdown_kills, "
                    "exclude each other: the second runs only when the first did not, since "
                    "either answer of the first sets shutdown_job or decided_by. 1 x (300 + 480) "
                    "= 780 s, runner-shutdown.sh's check-run read and annotations loop"),
]


class DeclaredCall:
    """What the calls through one variable add to the script writing them, where that script
    can see a function that fetches and the call runs whatever the variable names
    (dispatch://INFRA-1545, round 37). `path` names the script, `word` is the variable as the
    command word writes it, and `seconds` is added to that script's figure once, so every step
    running it carries it; `why` writes it out. A script is the one body this tree declares such
    calls in: a workflow step or a composite action's step is matched by none, so one writing a
    call through a variable writes the function's name there instead, or moves the call into a
    script. Until round 37 an entry could name a step, and one entry two steps of one name match
    charged the first of them alone. An entry naming a file this tree holds and matching nothing
    fails this check."""

    def __init__(self, path: str, word: str, seconds: int, why: str) -> None:
        self.path = path
        self.word = word
        self.seconds = seconds
        self.why = why
        self.used = 0


DECLARED_CALLS: list[DeclaredCall] = [
    DeclaredCall(".github/scripts/release-push.sh", "$leak_engine", 0,
                 "the leak engine, gitleaks, a binary this script fetched into its own "
                 "directory or was handed in RELEASE_PUSH_LEAK_ENGINE: it names no function, "
                 "and scanning a text fetches nothing, so the calls add 0 s"),
    *(DeclaredCall(".github/scripts/release-push.sh", word, 0,
                   "a flag this script sets to `true` or `false` where it uses it, which runs "
                   "as the builtin of that name: it names no function and fetches nothing, so "
                   "the calls add 0 s")
      for word in ("$found", "$listed", "$refused", "$skip", "$on_head", "$named")),
]


def credentialed_jobs(path: str, document: Mapping) -> set[str]:
    """The jobs of a release workflow that hold a credential, by check-release-credentials.sh's
    own definition: a write scope of WRITE_SCOPES, or the deploy key."""
    if path not in RELEASE_WORKFLOWS:
        return set()
    found = set()
    for name, job in (document.get("jobs") or {}).items():
        if not isinstance(job, Mapping):
            continue
        permissions = job.get("permissions")
        scopes = permissions if isinstance(permissions, Mapping) else {}
        # repr, not a YAML dump: the loader's mappings are a subclass no dumper represents, and
        # what is wanted is only whether the job's text names the key anywhere in it.
        if any(scopes.get(scope) == "write" for scope in WRITE_SCOPES) or DEPLOY_KEY.search(
            repr(job)
        ):
            found.add(name)
    return found


# ---------------------------------------------------------------- the reading set

files = walk()
workflows = [
    path for path in files
    if Path(path).parent == WORKFLOWS and Path(path).suffix in {".yml", ".yaml"}
]
actions = [
    path for path in files
    if ACTIONS in Path(path).parents and Path(path).name in {"action.yml", "action.yaml"}
]
scripts = [
    path for path in files
    if Path(path).parent == SCRIPTS
    and path.endswith(".sh")
    and not path.endswith(TEST_HARNESS)
]
for dockerfile in DOCKERFILES:
    if dockerfile not in files:
        problem(SELF, 1,
                f"{dockerfile} is one of the Dockerfiles a CI job builds and this check "
                "reads, and the tree does not hold it. Point DOCKERFILES at where it moved, or "
                "drop it if CI no longer builds it.")

sites: list[Site] = []
exempted: list[Site] = []
# The scripts a credentialed publish job's steps call, and then the closure of that set: the
# loops below fill the first and the walk between them turns it into the second.
credentialed_roots: set[str] = set()
credentialed_scripts: set[str] = set()


class Budget:
    """One step of a workflow, with everything rule 8 compares: where the step is written, what
    it is called, the minutes its own `timeout-minutes` gives (None where it writes none, which
    is itself refused for a step holding a retried call), the minutes its job gives and where
    that is written, the arithmetic of the calls the step's own `run:` holds, the local
    composite action a `uses:` step runs, and the recognised calls of the step's shell — the
    scripts its `run:` runs and sources, each with how many times the step runs that line, and
    the action a `uses:` names, as a run of it — whose calls are the step's too. A cost of None
    is a step holding a site this check cannot count, and `uncounted` is what made it so, which
    the refusal names in place of a comparison.

    A composite action's steps get no entry here: GitHub takes no `timeout-minutes` on one, so
    the budget that holds an action's calls is the `uses:` step that runs it."""

    def __init__(self, path: str, line: int, step: str, minutes: int | None, cost: int | None,
                 uses: str | None, calls: list[Occurrence], uncounted: list[Uncounted],
                 job: str | None, job_minutes: int | None, job_line: int) -> None:
        self.path = path
        self.line = line
        self.step = step
        self.minutes = minutes
        self.cost = cost
        self.uses = uses
        self.calls = calls
        self.uncounted = uncounted
        self.job = job
        self.job_minutes = job_minutes
        self.job_line = job_line


budgets: list[Budget] = []
# The arithmetic of each composite action's own `run:` steps, and the recognised calls of each
# of its steps, one list per step: each step of an action is a shell of its own, so a library
# two of its steps source is sourced twice, and a `uses:` step of the action carries the whole
# of it (dispatch://INFRA-1545, round 37).
action_cost: dict[str, int | None] = {}
action_shells: dict[str, list[list[Occurrence]]] = {}
# What rule 8 can put no figure on in each composite action and in each script: a site, or a
# script the body runs, inside a loop whose passes it cannot read. A step reaching one of them
# is told about that loop instead of being judged against a sum that is missing it.
action_uncounted: dict[str, list[Uncounted]] = {}
action_by_directory = {str(Path(path).parent): path for path in actions}
action_files = set(actions)
# The arithmetic of each script's own calls, and the recognised calls it makes of other scripts:
# whoever runs one of them waits for the whole of it, once for each time it runs it.
script_cost: dict[str, int | None] = {}
script_reach: dict[str, list[Occurrence]] = {}
script_uncounted: dict[str, list[Uncounted]] = {}
# What one call of each of a script's own sites takes, for every site that takes anything at
# all: the question the sum in script_cost can no longer answer. A cost of 0 is both a body
# with no site in it and a body whose every site has a bound written as a variable, and rule
# 8's filter below must tell a wait of no length from a wait of unread length.
script_waits: dict[str, list[int | None]] = {}
# Every occurrence of a script's path this check does not read as a call or as a non-call, with
# the file and the line it is written on and what a refusal calls the body holding it. Rule 8
# refuses the ones naming a script that fetches, once every script is read.
unread_calls: list[tuple[str, int, str, Occurrence]] = []
# Every shape a body writes that this check refuses by name rather than reads, with the file,
# the line, what a refusal calls the body, the refusal's sentence, whether a fetch is written in
# the shape's reach, the scripts it reaches, the names its commands call and the libraries the
# body sources (unread_shapes_of). Rule 8 refuses the ones whose reach fetches, once every script
# is read.
unread_shapes: list[tuple[str, int, str, str, bool, set[str], set[str], set[str]]] = []
# Every shell body's reading, for the two readings made once every script is read: a call of a
# function through a variable, and a library's fetching functions called more than once
# (dispatch://INFRA-1545, round 37). Each is (file, step name or "-", (what a refusal calls the
# body, loops, sites, recognised calls), the step's Budget or None).
body_reads: list[tuple[str, str, tuple, "Budget | None"]] = []
script_files = set(scripts)
# Each Dockerfile a workflow step builds in one of rule 10's shapes and DOCKERFILES names, as
# (workflow, what a refusal calls the step, the Dockerfile, the step, its job's name, the job),
# for the key that holds the build once the Dockerfiles are read.
dockerfile_built: list[tuple[str, str, str, Mapping, str | None, object]] = []


def local_action(uses: object) -> str | None:
    """The action.yml a `uses:` names, where it is one of this repository's own composite
    actions. A third-party reference and a called workflow are neither of them read: exception
    (b) above, and a called workflow's jobs carry their own budgets."""
    if not isinstance(uses, str) or not uses.startswith("./"):
        return None
    return action_by_directory.get(uses[2:].rstrip("/"))


def defaults_shell(holder: object) -> object:
    """The `defaults.run.shell` a workflow or one of its jobs sets, or None."""
    if not isinstance(holder, Mapping):
        return None
    defaults = holder.get("defaults")
    run = defaults.get("run") if isinstance(defaults, Mapping) else None
    return run.get("shell") if isinstance(run, Mapping) else None


def sets_pipefail(shell: object) -> bool:
    """Whether a step's shell sets `-o pipefail`, which is rule 9's question. `bash` is the one
    keyword GitHub expands to a pipefail shell, `bash --noprofile --norc -eo pipefail {0}`; a
    custom command line writes the option itself; and everything else — `sh`, `pwsh`, `python`,
    a shell named by a path, and a step that names none at all — leaves it off."""
    if not isinstance(shell, str):
        return False
    return shell.strip() == "bash" or "pipefail" in shell


def written_minutes(holder: object) -> int | None:
    """The `timeout-minutes` a step or a job writes, where it writes a whole number of them."""
    if not isinstance(holder, Mapping):
        return None
    minutes = holder.get("timeout-minutes")
    return minutes if isinstance(minutes, int) and not isinstance(minutes, bool) else None


def odd_minutes(holder: object, source_lines: list[str]) -> tuple[int, str] | None:
    """The line and the text of a step's or a job's `timeout-minutes` where it writes them in a
    form this check does not read as GitHub does, or None. The forms read are a whole number with
    no leading zero and an expression, which is read as no key (A CEILING WRITTEN AS AN
    EXPRESSION, under rule 8): this check's YAML is a YAML 1.1 reader, which reads `08` and `09`
    as strings, so as no key, and `010` as the octal 8, where a YAML 1.2 reader takes the decimal
    digits (dispatch://INFRA-1545, round 37)."""
    if not isinstance(holder, Mapping) or "timeout-minutes" not in holder.lines:
        return None
    line = holder.lines["timeout-minutes"]
    # The value as written on the key's line, in a block mapping or a flow one, `{…,
    # timeout-minutes: 28, …}`, up to a `,`, a `}` or a comment.
    written = re.search(r"(?:^|[\s{,])timeout-minutes\s*:\s*([^,}]*?)\s*(?:[,}]|\s#|$)",
                        source_lines[line - 1] if line <= len(source_lines) else "")
    text = written.group(1) if written else ""
    if re.fullmatch(r"0|[1-9]\d*", text) or "${{" in text:
        return None
    return line, text


def steps_of(path: str, document: Mapping) -> list[tuple[str | None, str, Mapping, object,
                                                         object]]:
    """The steps of a workflow's jobs and of a composite action's `runs`, each with the job it
    sits in (None in an action), the name a problem calls it by, the shell it runs under — its
    own `shell:`, else its job's `defaults.run.shell`, else the workflow's — and the mapping of
    the job holding it, whose own `timeout-minutes` rule 8 holds above the step's arithmetic
    too."""
    found = []
    holders: list[tuple[str | None, object, object, object]] = []
    fallback = defaults_shell(document)
    runs = document.get("runs")
    if isinstance(runs, Mapping):
        holders.append((None, runs.get("steps"), fallback, None))
    for name, job in (document.get("jobs") or {}).items():
        if isinstance(job, Mapping):
            holders.append((name, job.get("steps"), defaults_shell(job) or fallback, job))
    for job, steps, shell, holder in holders:
        if not isinstance(steps, list):
            continue
        for step in steps:
            if not isinstance(step, Mapping):
                continue
            name = step.get("name") or step.get("id") or "-"
            found.append((job, str(name), step, step.get("shell") or shell, holder))
    return found


# Rule 10's two shapes of a build: the action, and the two `docker` subcommands, each written as
# the words after `docker` that name it.
BUILD_ACTION = "docker/build-push-action"
DOCKER_BUILDS = (("build",), ("buildx", "build"))


def dockerfile_builds(step: Mapping,
                      commands: list[Command]) -> list[tuple[int, str, str | None]]:
    """Each Dockerfile this step builds in one of rule 10's two shapes, as the line naming it,
    the shape, and what the step writes for the file, None where it writes none. `commands` are
    the simple commands of the step's `run:` as sites_of read them, so a GitHub expression in
    them reads as EXPR there."""
    found: list[tuple[int, str, str | None]] = []
    uses = step.get("uses")
    if isinstance(uses, str) and uses.split("@", 1)[0] == BUILD_ACTION:
        options = step.get("with")
        written = options.get("file") if isinstance(options, Mapping) else None
        line = (options.lines.get("file", step.lines.get("uses", 0))
                if isinstance(options, Mapping) else step.lines.get("uses", 0))
        found.append((line, f"{BUILD_ACTION}'s `file:`",
                      None if written is None else str(written)))
    for command in commands:
        words = reduce_words(command.words)[0]
        if not words or PROGRAM.sub("", words[0]).rsplit("/", 1)[-1] != "docker":
            continue
        shape = next((shape for shape in DOCKER_BUILDS
                      if tuple(words[1:1 + len(shape)]) == shape), None)
        if shape is None:
            continue
        written = None
        rest = words[1 + len(shape):]
        for at, word in enumerate(rest):
            if word in ("--file", "-f"):
                written = rest[at + 1] if at + 1 < len(rest) else ""
            elif word.startswith("--file="):
                written = word[len("--file="):]
        found.append((command.line, f"`docker {' '.join(shape)}`'s `--file`", written))
    return found


def refuse_unnamed_builds(path: str, who: str, step: Mapping,
                          commands: list[Command]) -> list[str]:
    """Rule 10: each Dockerfile this step builds is one of DOCKERFILES, and is written as a path
    this check can read. Returns the ones that are, for the key that holds the build."""
    named = []
    for line, shape, written in dockerfile_builds(step, commands):
        readable = (written is not None and "EXPR" not in written
                    and not written.startswith("-") and LITERAL_WORD.fullmatch(written))
        if not readable:
            shown = ("no file at all, so it builds its context's own `Dockerfile`"
                     if written is None else f"`{written}`")
            problem(path, line,
                    f"{who} builds a Dockerfile ({shape}) and names it as {shown}, which this "
                    "check cannot read as a path, so it cannot tell whether that file is one of "
                    "DOCKERFILES, and a Dockerfile this check does not name has its RUN lines "
                    "held to nothing (rule 10; dispatch://INFRA-1545, round 37). Write the "
                    "Dockerfile's own path, from the repository's root, and add it to DOCKERFILES "
                    "in .github/scripts/check-download-retry.sh if it is not there.")
            continue
        target = os.path.normpath(written)
        if target in DOCKERFILES:
            named.append(target)
            continue
        problem(path, line,
                f"{who} builds {target} ({shape}), and DOCKERFILES does not name it, so this "
                "check never reads its RUN lines and holds the downloads in them to nothing: that "
                "is how .github/docker/browser.Dockerfile came to be built by CI with a bare curl "
                "and a bare `apt-get update` (rule 10; dispatch://INFRA-1545, round 37). Add "
                f"{target} to DOCKERFILES in .github/scripts/check-download-retry.sh.")
    return named


# The 1-based lines of each workflow's job ids, by file. The priced-comment clause of rule 8 stops
# a block's reach at another job's id, so a step comment at the end of one job never prices the
# next job's key.
JOB_ID_LINES: dict[str, set[int]] = {}
# The lines of each workflow's `timeout-minutes` keys, by file, as TIMEOUT_KEY_LINES reads them.
TIMEOUT_KEYS: dict[str, set[int]] = {}

for path in workflows + actions:
    DUPLICATE_KEYS.clear()
    TIMEOUT_KEY_LINES.clear()
    source_lines = Path(path).read_text().split("\n")
    try:
        document = yaml.load("\n".join(source_lines), Loader=LineLoader)
    except yaml.YAMLError as error:
        problem(path, 1, f"this file is not valid YAML, so its downloads cannot be read: {error}")
        continue
    TIMEOUT_KEYS[path] = set(TIMEOUT_KEY_LINES)
    for name, line in DUPLICATE_KEYS:
        problem(path, line,
                f"`{name}` is written twice in one mapping here. GitHub refuses a workflow with a "
                "duplicate key and runs nothing of it - a run of zero jobs, with no annotation to "
                "read - while this check's own YAML reader takes the last of the two and says "
                "nothing, so without this line a file CI will not start passes every other rule "
                "of this check. "
                "Delete one of them, and say in the comment which figure survived and why.")
    if not isinstance(document, Mapping):
        continue
    if isinstance(document.get("jobs"), Mapping):
        JOB_ID_LINES[path] = set(document["jobs"].lines.values())
        # A job's or a step's `timeout-minutes` written in a form this check's YAML does not
        # read as GitHub's does is refused by name, wherever it is written: rule 8 holds such a
        # key as no key at all, or at another figure (odd_minutes; dispatch://INFRA-1545, round
        # 37). Every key of this tree is a whole number or an expression.
        for job_id, job in document["jobs"].items():
            if not isinstance(job, Mapping):
                continue
            keyed = [(f"job '{job_id}'", job)] + [
                (f"step '{step.get('name') or step.get('id') or '-'}' of job '{job_id}'", step)
                for step in (job.get("steps") if isinstance(job.get("steps"), list) else [])
                if isinstance(step, Mapping)]
            for owner, holder in keyed:
                odd = odd_minutes(holder, source_lines)
                if odd is not None:
                    problem(path, odd[0],
                            f"{owner} writes `timeout-minutes: {odd[1]}`, which is no whole "
                            "number with no leading zero and no expression, the forms this "
                            "check's YAML reads as GitHub's does: a YAML 1.1 reader, it reads "
                            "`08` and `09` as strings, so as no key, `010` as the octal 8, and "
                            "other text as no key, so no rule here holds that key at the figure "
                            "GitHub gives it. Write the minutes as a whole number with no leading "
                            "zero, or as an expression (dispatch://INFRA-1545, round 37).")
    credentialed = credentialed_jobs(path, document)
    for job, name, step, shell, holder in steps_of(path, document):
        cost: int | None = 0
        called: list[Occurrence] = []
        blocked: list[Uncounted] = []
        body_read = None
        commands: list[Command] = []
        who = (f"step '{name}' of job '{job}'" if job is not None
               else f"step '{name}' of {path}")
        if isinstance(step.get("run"), str):
            # The line the body's first line is written on: a block scalar's text starts on the
            # line after its `run:` key, and a plain scalar's on the key's own line. Every line
            # a refusal names is counted from here, so each names the line it reads; until
            # round 37 a block's lines were counted from the key, so rules 1 to 9 and the
            # declarations' refusals named the line before the one they meant, and only the
            # refusals of an occurrence and of a shape added it back (dispatch://INFRA-1545,
            # round 37).
            first_line = step.lines.get("run", 0)
            if re.match(r"\s*[|>][+-]?\d*\s*(#.*)?$",
                        source_lines[first_line - 1].split("run:", 1)[-1]):
                first_line += 1
            found, loops_read = sites_of(path, step["run"], first_line, name,
                                         sets_pipefail(shell))
            commands = loops_read.commands
            occurrences = occurrences_of(step["run"], first_line, loops_read)
            called = recognised(occurrences)
            blocked = uncounted_of(found) + blocked_calls(path, called)
            unread_calls += [(path, occurrence.shown, who, occurrence)
                             for occurrence in occurrences if occurrence.kind == CALL_UNREAD]
            unread_shapes.extend(
                (path, line, who, *shape)
                for line, *shape
                in unread_shapes_of(path, step["run"], first_line, name, loops_read, found, called))
            body_read = (who, loops_read, found, called)
            # Exception (d) reads every occurrence that might run the script it names, which is
            # every occurrence but a recognised non-call: it asks what a credentialed job could
            # run, and a call rule 8 cannot read can run all the same.
            if job in credentialed:
                credentialed_roots.update(occurrence.target for occurrence in occurrences
                                          if occurrence.kind != CALL_NONE)
            for site in found:
                cost = added(cost, arithmetic_of(site))
                if job in credentialed:
                    exempted.append(site)
                    continue
                covering = next((entry for entry in EXEMPTIONS if entry.covers(site, job)), None)
                if covering:
                    covering.used += 1
                    exempted.append(site)
                    continue
                sites.append(site)
        uses = local_action(step.get("uses"))
        if uses:
            # A `uses:` of a local composite action runs that action once, as a step's shell
            # runs a script.
            called.append(Occurrence(uses, CALL_RUN, step.lines.get("uses", 0)))
        for target in refuse_unnamed_builds(path, who, step, commands):
            if path not in action_files:
                dockerfile_built.append((path, who, target, step, job, holder))
        if path in action_files:
            action_cost[path] = added(action_cost.get(path, 0), cost)
            action_shells.setdefault(path, []).append(called)
            action_uncounted.setdefault(path, []).extend(blocked)
        # Every step of a workflow gets an entry, whether or not it writes a budget: a step
        # that holds a retried call and writes none is refused below, which is the shape that
        # let .github/actions/install-elan's 607 s ladder under ci.yaml's `airlock-lean` reach
        # no comparison at all. A composite action's step gets none, because GitHub takes no
        # `timeout-minutes` there and the `uses:` step that runs the action is its budget.
        if path not in action_files:
            minutes = written_minutes(step)
            job_minutes = written_minutes(holder)
            budgets.append(Budget(
                path,
                step.lines.get("timeout-minutes", min(step.lines.values(), default=0)),
                name, minutes, cost, uses, called, blocked, job, job_minutes,
                holder.lines.get("timeout-minutes", min(holder.lines.values(), default=0))
                if isinstance(holder, Mapping) else 0))
        if body_read is not None:
            # The step's own Budget, or None for a composite action's step, which the `uses:`
            # step running the action answers for (body_reads below).
            body_reads.append((path, name, body_read,
                               budgets[-1] if path not in action_files else None))

# Exception (d) for a script: a *.sh under .github/scripts/ that a credentialed publish job runs
# can carry no retry either, because the retry is `. .github/scripts/ci-retry.sh` and that source
# runs inside the credentialed job, where check-release-credentials.sh's rule 1(a) allows no
# dependency code — a command whose path holds a `scripts/` component other than release-push.sh
# is dependency code by its own dependency_command(). Read from the tree, like (b): the roots are
# the scripts such a job's steps call, and the closure below adds the scripts those call in turn,
# reading every occurrence but a recognised non-call, as the roots are read. It follows a call it
# can see rather than the branch a job takes, so it reaches a script a credentialed job could
# run; the sites it covers are named in exception (d) of this file's header.
script_text = {path: Path(path).read_text() for path in scripts}
script_read = {path: sites_of(path, script_text[path], 1, "-") for path in scripts}
script_occurrences = {path: occurrences_of(script_text[path], 1, script_read[path][1])
                      for path in scripts}
while credentialed_roots:
    path = credentialed_roots.pop()
    if path in credentialed_scripts:
        continue
    credentialed_scripts.add(path)
    credentialed_roots.update(occurrence.target for occurrence in script_occurrences[path]
                              if occurrence.kind != CALL_NONE)

for path in scripts:
    text = script_text[path]
    found = script_read[path][0]
    script_reach[path] = recognised(script_occurrences[path])
    script_uncounted[path] = uncounted_of(found) + blocked_calls(path, script_reach[path])
    script_waits[path] = [one for one in map(one_call, found) if one != 0]
    unread_calls += [(path, occurrence.shown, path, occurrence)
                     for occurrence in script_occurrences[path] if occurrence.kind == CALL_UNREAD]
    unread_shapes.extend(
        (path, line, path, *shape)
        for line, *shape
        in unread_shapes_of(path, text, 1, "-", script_read[path][1], found, script_reach[path]))
    body_reads.append((path, "-", (path, script_read[path][1], found, script_reach[path]),
                       None))
    for site in found:
        script_cost[path] = added(script_cost.get(path, 0), arithmetic_of(site))
        if path in credentialed_scripts:
            exempted.append(site)
            continue
        covering = next((entry for entry in EXEMPTIONS if entry.covers(site, None)), None)
        if covering:
            covering.used += 1
            exempted.append(site)
            continue
        sites.append(site)
    # Rule 6: a `urllib.request` read inside this script's Python goes through the one reader. A
    # read is inside a `def` where it is written in the same heredoc, each a program of its own,
    # with every line from the `def` to it indented deeper than the `def`: a line at the `def`'s
    # own indentation or shallower ends its body. Until round 37 one stack of `def`s ran across
    # every heredoc of the script and was trimmed only at another `def` or an unindented line, so
    # a read in a second heredoc, or after the reader's body at its own indentation, was read as
    # the reader's (dispatch://INFRA-1545, round 37).
    for number, _match, body_lines, _quoted in heredocs_of(EXPRESSION.sub("EXPR", text)):
        indents: list[tuple[int, str]] = []
        for offset, body in enumerate(body_lines):
            line = number + 2 + offset
            if body.strip() and not body.lstrip().startswith("#"):
                indent = len(body) - len(body.lstrip())
                indents = [entry for entry in indents if entry[0] < indent]
                if match := PYTHON_DEF.match(body):
                    indents.append((indent, match.group(2)))
            if not PYTHON_READ.search(body):
                continue
            inside = indents[-1][1] if indents else None
            if inside == PYTHON_READER and path == PYTHON_READER_FILE:
                continue
            problem(path, line,
                    f"this read goes straight to urllib.request. Every Python read of a CI script "
                    f"goes through {PYTHON_READER_FILE}'s {PYTHON_READER}(), which carries R1's "
                    f"floor in that check's idiom: {ATTEMPT_FLOOR} attempts, waits of 10, 20, 40 "
                    "and 80 seconds, each bounded by the read's own timeout. One definition, no "
                    "copies.")

# ---------------------------------------------------------------- the Dockerfiles


def instructions(text: str) -> list[tuple[int, str, str]]:
    """Each instruction of a Dockerfile as (line, keyword, the text after the keyword) with its
    backslash continuations joined as Docker joins them, so a scan of that text reads the lines
    the build runs. A comment line or a blank line inside a continuation is no part of the
    instruction: Docker drops it and reads on to the next line. Each is kept as a lone backslash,
    which continues the text the same way and leaves every later line on its own number. Until
    round 37 such a line ended the instruction here, so a RUN writing a comment after its first
    line was read no further than that comment: .github/docker/browser.Dockerfile, added to
    DOCKERFILES alone, read as a RUN with no fetch in it, its curl and its apt-get each written
    below such a comment (dispatch://INFRA-1545, round 37)."""
    found = []
    lines = text.split("\n")
    index = 0
    while index < len(lines):
        line = lines[index]
        start = index + 1
        stripped = line.strip()
        if not stripped or stripped.startswith("#"):
            index += 1
            continue
        body = [line]
        while body[-1].rstrip().endswith("\\") and index + 1 < len(lines):
            index += 1
            following = lines[index].strip()
            body.append("\\" if not following or following.startswith("#") else lines[index])
        index += 1
        whole = "\n".join(body)
        keyword, _, rest = whole.partition(" ")
        found.append((start, keyword.upper(), rest))
    return found


def joined(text: str) -> str:
    """One line, continuations joined and each run of blank space one space: the shape the
    canonical retry is compared in."""
    return " ".join(text.replace("\\\n", " ").split())


for path in DOCKERFILES:
    if path not in files:
        continue
    for line, keyword, rest in instructions(Path(path).read_text()):
        if keyword != "RUN":
            continue
        while match := re.match(r"\s*--(mount|network|security)=\S+", rest):
            rest = rest[match.end():]
        found, _ = sites_of(path, rest, line, "-")
        if not found:
            continue
        sites += found
        flat = joined(rest)
        opens = flat.startswith(CANONICAL)
        if "retry()" in flat and not opens:
            problem(path, line,
                    "this RUN defines a retry loop of its own text. Every network RUN of the "
                    "Dockerfiles this check reads opens with one canonical definition, byte for "
                    "byte, so a reader sees one loop and not a family of them. The text is in "
                    ".github/scripts/check-download-retry.sh's CANONICAL.")
        elif not opens and any(site.retry == "retry" for site in found):
            problem(path, line,
                    "this RUN calls `retry` and does not open with the canonical definition of it. "
                    "The text is in .github/scripts/check-download-retry.sh's CANONICAL.")

# Rule 10, the key that holds a build: above the longest single ladder of the Dockerfile it
# builds. A build runs its fetches one after another, so a fault window is paid once, through
# the first fetch that meets it, and one ladder is what the build can wait for above its measured
# duration, which no figure here reads; the header's rule 10 says what this does not cover.
def ladder_of(site: Site) -> int:
    """One ladder of a Dockerfile's fetch, read once whatever runs it: a `retry` call's
    5 x <bound> + 150 s, or the ladder of a curl making its own attempts."""
    if site.retry == "retry":
        return ATTEMPT_FLOOR * site.bound + HELPER_WAIT
    return one_call(site) or 0


longest_ladder: dict[str, tuple[int, Site]] = {}
for site in sites:
    if site.path in DOCKERFILES and (site.path not in longest_ladder
                                     or ladder_of(site) > longest_ladder[site.path][0]):
        longest_ladder[site.path] = (ladder_of(site), site)
held_keys: set[tuple[str, int]] = set()
for path, who, target, step, job, holder in dockerfile_built:
    if target not in longest_ladder or not isinstance(holder, Mapping):
        continue
    ladder, longest = longest_ladder[target]
    keys = []
    if written_minutes(step) is not None:
        keys.append((written_minutes(step), step.lines["timeout-minutes"], who, "it"))
    keys.append((written_minutes(holder) or JOB_DEFAULT_MINUTES,
                 holder.lines.get("timeout-minutes", min(holder.lines.values(), default=0)),
                 f"job '{job}'", who))
    for minutes, key_line, owner, builder in keys:
        if 60 * minutes > ladder or (path, key_line) in held_keys:
            continue
        held_keys.add((path, key_line))
        problem(path, key_line,
                f"{owner} carries `timeout-minutes: {minutes}`, {60 * minutes} s, and {builder} "
                f"builds {target}, whose longest single ladder is {ladder} s, the "
                f"`{longest.verb}` at {target}:{longest.line}. A Dockerfile build runs its "
                "fetches one after another, so a fault window is paid once, through the first "
                "fetch that meets it, and that one ladder is what the build can wait for above "
                "its measured duration: a key at or under it cuts that fetch before its last "
                "attempt can name it (rule 10; dispatch://INFRA-1545, round 37). Price the key "
                f"as the build's measured duration plus {ladder} s, both terms in the comment "
                "above it, and write the whole minute strictly above their sum.")

# ---------------------------------------------------------------- the verdict


def where_of(site: Site) -> str:
    """How a refusal names a site: its verb, and the step it sits in where it is a step's."""
    return f"`{site.verb}`" + (f" in step '{site.step}'" if site.step != "-" else "")


def variable_helper_bound(site: Site) -> str:
    """What the refusal of a `retry_fetch` call whose seconds this check cannot read as a number
    says after naming the site. The calls whose seconds the helper itself refuses, and those
    outside its signature, are no sites (odd_command), so what is left is seconds written as a
    variable or an expression: until round 37 this sentence told a curl writing `--max-time 300`
    under such a call to pass `--max-time` (dispatch://INFRA-1545, round 37)."""
    return (f"is retried by `retry_fetch` with its seconds written as `{site.helper_bound}`, which "
            "is no number this check reads: the helper bounds each attempt at what that word "
            "holds when the step runs, and no figure can be put on the attempts here. Write the "
            "helper's seconds as a whole number above 0 (dispatch://INFRA-1545, round 37).")


for site in sites:
    where = where_of(site)
    dockerfile = site.path in DOCKERFILES
    if site.silent:
        problem(site.path, site.line,
                f"{where} is retried, and that retry protects nothing: `mise x`, `mise exec`, "
                "`mise up` and `mise upgrade` exit 0 when they cannot resolve the tool. `mise x` "
                "warns (`Failed to resolve tool version list for …`) and then runs whatever "
                "binary of that name is already on PATH — a different build, at a version nobody "
                "asked for — and `mise up` says `All tools are up to date` having read nothing. "
                "So the retry waits for a failure that never comes, and the first attempt "
                "succeeds on a tool it never fetched. The cure is `mise install <tool>` under "
                "the retry, which exits 1 on the failed fetch, with the resolution after it: "
                "once the install has succeeded, a bare `mise x <tool> -- sh -c 'command -v "
                "<binary>'` reads the path off disk and fetches nothing.")
        continue
    if site.verb == "curl":
        retries, all_errors, bounded, fails = curl_options(site.words)
        # A bound written in a form this check does not read is refused by name below, and is
        # not also told it is missing (dispatch://INFRA-1545, round 37).
        spelled = curl_bound_refusal(site.words) if site.retry is None else None
        bounded = bounded or spelled is not None
        if site.retry:
            if not site.bounded:
                problem(site.path, site.line, f"{where} {variable_helper_bound(site)}")
        elif retries is None or retries < RETRY_FLOOR or not all_errors or not bounded or not fails:
            missing = []
            if retries is None:
                missing.append(f"`--retry {RETRY_FLOOR}` (it passes none)")
            elif retries < RETRY_FLOOR:
                missing.append(f"`--retry {RETRY_FLOOR}` (it passes `--retry {retries}`, whose "
                               "last attempt starts well inside 120 s: curl's waits double from "
                               f"one second, so only {RETRY_FLOOR} reaches 127 s)")
            if not all_errors:
                missing.append("`--retry-all-errors` (without it a connection reset is not a retry)")
            if not bounded:
                missing.append("`--max-time <seconds>`, which curl resets for each attempt, "
                               "above 0, since `--max-time 0` sets no limit")
            if not fails:
                missing.append("`--fail`, usually the `f` of `-fsSL` (without it curl exits 0 on a "
                               "persistent 504 and writes the error body to the output)")
            problem(site.path, site.line,
                    f"{where} fetches without the floor: it lacks " + ", ".join(missing) +
                    ". Pass curl's own options, or wrap the call in `retry_fetch` from "
                    f"{HELPER}.")
        else:
            delay, max_time, readable = curl_waits(site.words)
            ladder = curl_ladder(retries, delay, max_time)
            if not readable:
                problem(site.path, site.line,
                        f"{where} writes `--retry-delay` or `--retry-max-time` with a value "
                        "this check cannot read, so it cannot say when the last of curl's "
                        "attempts starts. Both options rewrite the waits between them — a "
                        "fixed wait, and a limit that stops the ladder — and R1's floor is "
                        f"that the last attempt starts at least {LADDER_FLOOR} s after the "
                        "first. Write the seconds as a number, or drop the option and take "
                        "curl's own doubling waits.")
            elif ladder < LADDER_FLOOR:
                wrote = []
                if delay is not None:
                    wrote.append(f"`--retry-delay {delay}`, which replaces curl's doubling "
                                 "waits with a fixed one")
                if max_time is not None:
                    wrote.append(f"`--retry-max-time {max_time}`, which stops the ladder once "
                                 "the timer reset at the first attempt reaches it")
                problem(site.path, site.line,
                        f"{where} carries curl's own attempts and its last one starts "
                        f"{ladder} s after the first, inside R1's floor of {LADDER_FLOOR} s: "
                        "it writes " + " and ".join(wrote) + ". A retry whose ladder is over "
                        "in seconds does not outlast the fault it exists to survive — the "
                        "measured incidents were hosts refusing for about a minute. Drop the "
                        f"option and take curl's own waits, which reach 127 s at `--retry "
                        f"{RETRY_FLOOR}`, or write one that reaches {LADDER_FLOOR} s.")
        if spelled is not None:
            problem(site.path, site.line, f"{where} {spelled} (dispatch://INFRA-1545, round 37).")
        continue
    if site.verb == "apt-get update" and not apt_error_mode(site.words):
        problem(site.path, site.line,
                f"{where} passes no `-o APT::Update::Error-Mode=any`, so a failed index read "
                "leaves it reporting success. apt treats every index it could not fetch as a "
                "warning (`W: Failed to fetch … 504 Gateway Timeout`, then `W: Some index files "
                "failed to download. They have been ignored, or old ones used instead.`) and "
                "exits 0, so the retry around it stops after one attempt having read no list, "
                "and the `apt-get install` after it fails with `has no installation candidate` "
                "against a package list that will never name the package — which no further "
                "attempt can cure. The option makes each of those warnings an error, so the "
                "command exits non-zero and the retry has something to retry. It is apt's "
                "`--fail`.")
    if dockerfile:
        if site.retry != "retry":
            problem(site.path, site.line,
                    f"{where} fetches inside a Dockerfile RUN with no retry around it. Wrap it in "
                    "the canonical inline loop this file's network RUN lines open with: "
                    "`retry <bound in seconds> \"<what>\" <command...>`.")
        continue
    if site.retry != "retry_fetch":
        problem(site.path, site.line,
                f"{where} fetches with no retry. Source {HELPER} and call "
                "`retry_fetch \"<what>\" <bound in seconds> -- <command...>`: five attempts, the "
                "last starting 150 s after the first, each under `timeout <bound>`.")
        continue
    if not site.bounded:
        problem(site.path, site.line, f"{where} {variable_helper_bound(site)}")

# Rule 9: a fetch left of a `|` in a step whose shell leaves pipefail off. The pipeline's status
# is its last command's there, so the fetch's own failure is the one thing the step never reads.
for site in sites:
    if not site.piped or site.silent:
        continue
    refusal = (
        f"{where_of(site)} is not the last command of its pipeline, and this step's shell "
        "leaves `pipefail` off: a workflow `run:` that names no shell runs under GitHub's "
        "`/usr/bin/bash -e {0}`, where a pipeline's status is its last command's alone. So "
        "`curl … | tar -xz` exits 0 when the fetch got a 504 and the unpack read nothing, and "
        "a `retry_fetch` around that pipeline reads that 0 and never retries: the site reads "
        "retried and is unprotected in fact. Read the fetch's own status: write the pipeline "
        "as two commands, with the fetch's output in a file or a variable, or give the step "
        "`shell: bash`, which GitHub expands to `bash --noprofile --norc -eo pipefail {0}`."
    )
    # One site, one annotation: a fetch this rule refuses is often refused by rule 1 as well,
    # and two `::error` lines at one line of one file read as two sites rather than as one
    # site's two faults.
    opening = f"::error file={site.path},line={site.line}::"
    for index, line in enumerate(problems):
        if line.startswith(opening):
            problems[index] = f"{line} {refusal}"
            break
    else:
        problem(site.path, site.line, refusal)

# An excepted site, which rules 1 to 6 do not read, that makes attempts of its own with no
# bound — a curl's own `--retry` with no `--max-time`, or a `retry_fetch` whose seconds are a
# variable: rule 4 refuses such an attempt everywhere else, and rule 8 leaves it to rule 4, so
# here nothing refused it and the step holding it passed with no figure (dispatch://INFRA-1545,
# round 37). No budget can be held above that wait. The `retry_fetch` is told its seconds are
# the variable they are: until round 37's class search it was told this check finds no bound on
# it, while its attempts are bounded at what that word holds (dispatch://INFRA-1545, round 37).
# An unretried fetch is one attempt, which rule 8 prices at nothing wherever
# it sits, as exception (d) leaves it, but an unretried curl is priced at the floor its own
# options make, and one writing no `--max-time` has no figure: the step holding it was compared
# with nothing, its other retries included, and nothing refused it; and a curl writing its bound
# in a form curl_bound_refusal names was priced at a bound curl does not use
# (dispatch://INFRA-1545, round 37). No excepted site of this tree is any of these.
for site in exempted:
    spelled = (curl_bound_refusal(site.words) if site.verb == "curl" and site.retry is None
               else None)
    if spelled is not None:
        problem(site.path, site.line,
                f"{where_of(site)} is a site an exception above leaves unretried, and it "
                f"{spelled} (dispatch://INFRA-1545, round 37).")
    elif one_call(site) is None and not bound_written(site) and site.retry == "retry_fetch":
        problem(site.path, site.line,
                f"{where_of(site)} is a site an exception above leaves unretried, and it is "
                f"retried by `retry_fetch` with its seconds written as `{site.helper_bound}`, "
                "which is no number this check reads: each attempt is bounded at what that word "
                "holds when the step runs, so no figure can be put on the attempts and no budget "
                "can be held above the wait for them; rule 4, which refuses such seconds "
                "everywhere else, does not read an excepted site. Write the helper's seconds as a "
                "whole number above 0, or drop its retry, as the excepted sites do "
                "(dispatch://INFRA-1545, round 37).")
    elif (one_call(site) is None and not bound_written(site)
          and curl_options(site.words)[0] is not None):
        problem(site.path, site.line,
                f"{where_of(site)} is a site an exception above leaves unretried, and it makes "
                "attempts of its own with no bound this check finds, so whatever runs it waits "
                "for it without limit and no budget can be held above that wait; rule 4, which "
                "refuses such an attempt everywhere else, does not read an excepted site. Bound "
                "each attempt with a number of seconds above 0 — `--max-time <seconds>` on a "
                "curl, or the seconds `retry_fetch` takes before its `--` — or drop its retry, "
                "as the excepted sites do (dispatch://INFRA-1545, round 37).")
    elif one_call(site) is None and not bound_written(site):
        problem(site.path, site.line,
                f"{where_of(site)} is a site an exception above leaves unretried, and it writes "
                "no `--max-time`, so this check can put no figure on its one attempt, and the "
                "step that runs it is compared with nothing: the step's other retries reach no "
                "budget either. Bound the attempt with `--max-time <seconds>`, a number above 0 "
                "(dispatch://INFRA-1545, round 37).")

# Rule 11: a job that compiles Go holds a retried `go mod download` of its own. A `go build`,
# `go test`, `go vet` or `go run` fetches this module's dependencies itself when the cache misses,
# and that fetch is in none of THE VERBS above, because it sits inside a command whose purpose is
# something else: the rules above read it as no site at all. A cache restore is nothing to this
# rule, which refuses a job that compiles Go with no download step whether or not it restores one.
# What this rule reads, and the five things it does not, are written at AND A BUILD CAN FETCH
# above, each beside the line that decides it; it reads less than its neighbours, so a reader who
# assumes otherwise is reading the neighbours.
#
# The one job this rule reads and must not refuse carries its reason in GO_BUILD_EXEMPT: a job
# that compiles a package importing the standard library alone downloads nothing, and its own
# comment says so. An entry matching no job compiling Go here fails this check, as EXEMPTIONS
# does, so a renamed or deleted job takes its exception with it rather than leaving one live for
# the next job of that name.
GO_BUILD = re.compile(r"\bgo\b(?:\s+-C\s+\S+)?\s+(?:build|test|vet|run)\b")
GO_MOD_DOWNLOAD = re.compile(r"\bgo\b(?:\s+-C\s+\S+)?\s+mod\s+download\b")
GO_BUILD_EXEMPT = {
    (".github/workflows/ci-job-airlock-lean.yaml", "airlock-lean"):
        "the airlock package imports the standard library alone, so this job downloads no module "
        "and compiles no third-party code, as its own comment says",
}

go_build_jobs: dict[tuple[str, str], tuple[int, str]] = {}
go_download_jobs: set[tuple[str, str]] = set()
for path in workflows:
    try:
        document = yaml.load(Path(path).read_text(), Loader=LineLoader)
    except yaml.YAMLError:
        continue
    if not isinstance(document, Mapping):
        continue
    for job, name, step, _shell, _holder in steps_of(path, document):
        body = step.get("run")
        if not isinstance(body, str) or job is None:
            continue
        if GO_MOD_DOWNLOAD.search(body):
            go_download_jobs.add((path, job))
        elif GO_BUILD.search(body) and (path, job) not in go_build_jobs:
            go_build_jobs[(path, job)] = (step.lines.get("run", 0), name)

for (path, job), (line, name) in sorted(go_build_jobs.items()):
    if (path, job) in go_download_jobs or (path, job) in GO_BUILD_EXEMPT:
        continue
    problem(path, line,
            f"job '{job}' compiles Go in step '{name}' and holds no retried `go mod download` of "
            "its own. A go command fetches this module's dependencies itself when the cache "
            "misses, and that fetch is inside a command whose purpose is something else, so no "
            "other rule here reads it: the job downloads from proxy.golang.org with no retry and "
            "nothing says so. Write the step the browser suite's jobs carry, which this rule "
            "asks for by presence and never by position: `retry_fetch "
            "\"<module>'s Go modules\" 180 -- go -C <module> mod download`, under a "
            "`timeout-minutes` above its 5 x 180 + 150 = 1050 s. Write it after the cache restore "
            "and before the first Go command, which is where it does the work.")

for (path, job), why in sorted(GO_BUILD_EXEMPT.items()):
    # Every entry must match a job compiling Go in the tree being read, as EXEMPTIONS requires of
    # its own, so a renamed or deleted job takes its exception with it. The harness's fixture
    # trees carry an `airlock-lean` job for that reason.
    if (path, job) not in go_build_jobs:
        problem(path, 1,
                f"GO_BUILD_EXEMPT holds '{job}' of this file, and this check finds no job of that "
                "name compiling Go here. An exception that matches nothing hides nothing and "
                f"rots: delete it, or correct the name. Its reason was: {why}")

# Rule 2: a loop that holds a fetch is a copy of the one helper. Not in a Dockerfile, whose
# canonical definition is itself a loop and which carries no helper file to copy.
seen_loops: set[tuple[str, int]] = set()
for site in sites:
    if site.loop is None or site.retry == "retry_fetch" or site.path in DOCKERFILES:
        continue
    if (site.path, site.loop) in seen_loops:
        continue
    seen_loops.add((site.path, site.loop))
    problem(site.path, site.loop,
            f"this loop retries `{site.verb}` itself, which makes it a second copy of the retry "
            f"{HELPER} holds. One rule holds the attempts (dispatch://INFRA-1545, R2): delete the "
            "loop and call `retry_fetch \"<what>\" <bound in seconds> -- <command...>`.")

# Rule 8: a retried step's own budget strictly exceeds the attempts it allows, so the last
# attempt's own error reaches the log before the runner's kill does. A call is counted once for
# each time the step runs it — every pass of every loop around it, every call of the function
# holding it, and the loops around those calls — and a step reaching a loop whose passes this
# check cannot read, and for which no DeclaredArithmetic entry writes the seconds, is told about
# that loop in place of a comparison: a sum short by one of its loops would send the step's
# author to the wrong line.


# While one step is charged, the seconds the DeclaredLibrary entries covering it write for each
# library, which replace that library's own arithmetic (shell_arithmetic); and the libraries the
# step's shell, or a composite action step's it runs, reaches along more than one path or along
# one path more than once, each with that action or None for the step itself, and with that one
# path where it is the second form, or None (dispatch://INFRA-1545, round 37).
PRICED_LIBRARIES: dict[str, int] = {}
SHARED_LIBRARIES: dict[str, tuple[str | None, tuple[str, ...] | None]] = {}


def file_arithmetic(path: str, seen: set[str], inside: tuple[str, ...] = ()) -> int | None:
    """What one run of a script, or one `uses:` of a composite action, carries, read as a shell
    of its own: the script's own sites with what its recognised calls reach, as
    shell_arithmetic charges them, or the action's own sites with each of its steps charged as
    the shell it is (dispatch://INFRA-1545, round 37). None where one of those calls is a site
    this check cannot count, or a call of a script that costs anything inside a loop whose
    passes it cannot read. `seen` gains every file it reaches, and `inside` is the path the run
    is made from, which a cycle never re-enters."""
    here = inside + (path,)
    if path in action_files:
        total = action_cost.get(path, 0)
        for calls in action_shells.get(path, []):
            total = added(total, shell_arithmetic(calls, seen, here))
        return total
    return added(script_cost.get(path, 0),
                 shell_arithmetic(script_reach.get(path, []), seen, here))


def shell_arithmetic(calls: list[Occurrence], seen: set[str],
                     inside: tuple[str, ...]) -> int | None:
    """The arithmetic of what one shell's recognised calls reach: a workflow step's, or a
    composite action step's, and every script it runs in turn (dispatch://INFRA-1545, round 37).

    A RUN is charged for every time the shell runs it: each run adds the run script's own
    sites and its own runs, multiplied by the loops around it, with those loops' waits where
    that script, read as a shell of its own, costs anything. A step that runs a.sh and then
    b.sh, each running c.sh, runs c.sh twice and waits for both. Until round 37 every file a
    step reached was charged once, at its most expensive path, which charged that step one
    c.sh: review round 29's input, a step running zz-a.sh three times in a loop and zz-b.sh
    once, both running zz-c.sh at 180 s (1050 s), is 4 x 1050 = 4200 s, which that reading
    charged 3150 s. A `uses:` of a composite action is a run of it, charged what
    file_arithmetic reads of it: each step of an action is a shell of its own, so install-jj,
    which fetches through .github/scripts/ci-tools.sh in two of its steps, is charged for both.

    A library SOURCED anywhere the shell reaches — by the shell itself, by another library, or
    by a script the shell runs — is charged once for the shell, at its most expensive path,
    which is the architect's reading of round 37: a library is a set of functions, and its
    sites are the step's once. Every path from the shell to the library is walked, through
    runs and sources alike, and its own arithmetic, with the runs written in it, is multiplied
    by the largest product of the loop factors along any of them; where two paths give the
    same product, the path whose names sort first is taken. A source inside a loop is no
    recognised call (occurrences_of), so no source along a path adds a loop's waits: the waits
    along a path are its runs', which runs charges with each run. Until that refusal, round 37
    charged a source's loop waits along the path that charged its library, by the path's names
    and then by its calls, which was a model no step of this tree needs (dispatch://INFRA-1545,
    round 37). A library the shell reaches along more than one path is put in
    SHARED_LIBRARIES, since each path may call its functions, and the step holding it is
    refused by name unless a DeclaredLibrary entry writes what the library contributes; that
    entry's seconds, in PRICED_LIBRARIES while the step is charged, replace the library's own
    arithmetic at its most expensive path (dispatch://INFRA-1545, round 37). So is a library one
    shell text sources more than once, the step's own or a script's it runs: its own commands
    run at each source, and until round 37 the two sources were one path, so it was charged once
    and refused nothing (dispatch://INFRA-1545, round 37). And so is a library the shell reaches
    along one path more than once, as a script that sources it and that the step runs in more
    than one place: each run is a shell of its own that sources the library and can call its
    functions, while the library is charged once for the step. Until round 37's class search it
    was charged once and refused nothing: a step running zz-a.sh on two lines, which sources
    zz-lib.sh and calls its zz_fetch, one `retry_fetch` at 180 s, waits 2100 s and was charged
    1050 s, where the same two runs written as a `for pass in 1 2` are charged 2100 s. This tree
    writes it, as ci-job-tool-fallback's 'Seed mise's install of jj with the staged binary, one
    byte changed' runs ci-tools-proof.sh three times, and that step's DeclaredLibrary entry
    writes its figure (dispatch://INFRA-1545, round 37).

    A call whose loop writes no count (factor None) is charged nothing where the file it
    reaches, with what that file calls in turn, costs 0: the figure those passes would multiply
    is zero either way (review round 5 of dispatch://INFRA-1545). That keeps a priced script
    priced. A script holding a 450 s ladder and, beside it, such a loop around a costless script
    of its own keeps its 450 s, where an answer of None would leave the step running it
    compared with nothing and passed in silence (review round 6 of dispatch://INFRA-1545). Such
    a call of a file that costs above 0, or whose own answer is None, makes the whole answer
    None.

    `seen` gains every file the calls reach, which is what the step's refusals read for the
    files it runs and for their uncounted loops. `inside` is the path the calls are made from:
    a path never enters a file it is already inside, so a cycle, which the shell would run until
    something failed but which must not hang this check, stops."""
    unread = False
    # The most expensive path to each library the shell reaches: the product of its factors and
    # the names along it.
    best: dict[str, tuple[int, tuple[str, ...]]] = {}
    # Every path along which the shell sources each library, how many times the shell walks to
    # it along any of them, and the libraries one shell text sources more than once.
    paths: dict[str, set[tuple[str, ...]]] = {}
    walks: dict[str, int] = {}
    again: set[str] = set()

    def survey(path: tuple[str, ...], product: int, calls: list[Occurrence]) -> None:
        nonlocal unread
        sourced: set[str] = set()
        for call in calls:
            if call.target in path:
                continue
            seen.add(call.target)
            if call.factor is None:
                unread = unread or file_arithmetic(call.target, seen, path) != 0
                continue
            if call.target in action_files:
                continue
            here = path + (call.target,)
            reached = (product * call.factor, here)
            if call.kind == CALL_SOURCED:
                if call.target in sourced:
                    again.add(call.target)
                sourced.add(call.target)
                paths.setdefault(call.target, set()).add(here)
                walks[call.target] = walks.get(call.target, 0) + 1
                known = best.get(call.target)
                if (known is None or reached[0] > known[0]
                        or (reached[0] == known[0] and here < known[1])):
                    best[call.target] = reached
            survey(here, reached[0], script_reach.get(call.target, []))

    def runs(calls: list[Occurrence], path: tuple[str, ...]) -> int:
        """The runs among these calls, each charged once for every time it runs, with the
        runs each run script makes in turn; its libraries are the shell's, charged below."""
        nonlocal unread
        total = 0
        for call in calls:
            if call.kind != CALL_RUN or call.target in path or call.factor is None:
                continue
            value = file_arithmetic(call.target, seen, path)
            if value is None:
                unread = True
                continue
            if call.target in action_files:
                total += call.factor * value
            else:
                own = added(script_cost.get(call.target, 0),
                            runs(script_reach.get(call.target, []), path + (call.target,)))
                if own is None:
                    unread = True
                    continue
                total += call.factor * own
            total += call.wait if value else 0
        return total

    survey(inside, 1, calls)
    total = runs(calls, inside)
    for target, (product, path) in sorted(best.items()):
        cost = added(script_cost.get(target, 0), runs(script_reach.get(target, []), path))
        if cost is None:
            return None
        # The shell's own paths are the step's, or a composite action step's: a script's are
        # walked again, and refused once, in the shell of the step that runs it. One path walked
        # more than once is a script on it that the shell runs, or sources, in more than one
        # place, and the refusal names that path (dispatch://INFRA-1545, round 37).
        if cost and (not inside or inside[-1] in action_files):
            holder = inside[-1] if inside else None
            if len(paths[target]) > 1 or target in again:
                SHARED_LIBRARIES.setdefault(target, (holder, None))
            elif walks[target] > 1:
                SHARED_LIBRARIES.setdefault(target, (holder, path[len(inside):]))
        total += PRICED_LIBRARIES.get(target, product * cost)
    if unread:
        return None
    return total


def retries_nothing(path: str, seen: set[str]) -> bool:
    """Whether this script holds no call anything waits for, and neither does any script it
    calls in turn. A script that only calls a script that fetches is a script the step waits
    for, however bare its own body reads, which is why this walks the calls rather than
    stopping at the one body. `seen` stops a cycle — the shell would run it until something
    failed, and this check must not hang on it — and a path already walked answers True,
    because whatever it holds was read where it was first walked."""
    if path in seen:
        return True
    seen.add(path)
    if script_waits.get(path):
        return False
    return all(retries_nothing(call.target, seen) for call in script_reach.get(path, []))


def unpriced_waits(path: str, seen: set[str]) -> set[str]:
    """How this script, or one it calls in turn, holds a call whose attempts this check cannot
    price, as the kinds of each call one_call answers None for: "variable" for a bound written
    as a variable, "none" for no bound this check finds, as bound_spelled answers, and "spelled"
    for a curl's bound written in a form curl_bound_refusal refuses by name, such as `--max-time
    1.5`. Empty where it holds no such call. The refusal below says which in place of a figure,
    since what one run of such a script takes is a number nobody can write; it said a variable
    for every one, and so told the author of a call with no bound that the bound was written,
    and that of `--max-time 1.5` that it was a variable (dispatch://INFRA-1545, round 37)."""
    if path in seen:
        return set()
    seen.add(path)
    found = {"spelled" if site.verb == "curl" and site.retry is None
             and curl_bound_refusal(site.words) is not None
             else "variable" if bound_spelled(site) else "none"
             for site in script_read.get(path, ([], None))[0] if one_call(site) is None}
    for call in script_reach.get(path, []):
        found |= unpriced_waits(call.target, seen)
    return found


def waits_for_nothing(item: Uncounted) -> bool:
    """Whether one of blocked_calls's items is a wait of no length: a script a body runs inside
    a loop this check cannot count, which makes no attempt the step waits for and holds no site
    anybody is owed a figure for.

    The question is asked of the sites themselves, through retries_nothing, and not of the
    arithmetic, because the arithmetic answers 0 to two different questions. A script whose
    every site has a bound written as a variable costs 0 as surely as a script with no site in
    it, and the first is a wait this check cannot price while the second is a wait that is not
    there. Reading the pair as one answer dropped the item for a step whose loop ran a script
    holding a `curl -fsSL --retry 7 --retry-all-errors --max-time "$T"`, which is a fetch the
    step really waits for (review round 6 of dispatch://INFRA-1545).

    It is asked of every script the item's script reaches, too, and not of that one body: a
    loop around a script that loops, uncounted, over a costless script holds no attempt
    either, and reading only the first body left that shape refused with the `0 s` sentence
    review round 5 wrote this filter to retire (review round 6 of dispatch://INFRA-1545).

    Rule 8 exists to compare a step's budget with the attempts it waits for. A loop around a
    script that makes none is a loop with no attempt under it, so there is nothing to compare
    and no pass count to ask the author for: refusing it told a step holding no retry to write
    one, and the refusal priced the run it was refusing at `0 s` (review round 5 of
    dispatch://INFRA-1545).

    The item is recognised by its kind and not by `one is None`. That inference read a site
    whose bound is written as a variable as one of blocked_calls's script calls, and then
    asked retries_nothing for a path that is a verb, which answers True for anything no script
    is named by: the site was dropped, and the step waiting for it was refused by nobody
    (review round 8 of dispatch://INFRA-1545). uncounted_of's sites are never this filter's business: a site
    that waits for nothing is already dropped where it is found."""
    return item.kind == UNCOUNTED_SCRIPT_CALL and retries_nothing(item.what, set())


JOB_SLOWEST: dict[tuple[str, str], dict] = {}
def step_name(budget) -> str:
    """What a refusal calls this step. A `uses:` step with no `name:` reads as '-' in the
    workflow, which names nothing a reader can search for, so the action it runs is what the
    message gives them instead (dispatch://INFRA-1545, review round 26)."""
    if budget.step == "-" and budget.uses:
        return f"the step running {budget.uses}"
    return budget.step


# An occurrence of a script's path this check reads as neither a call nor a non-call, naming a
# script that fetches, is refused by name (dispatch://INFRA-1545, round 37): the step that runs
# that line may wait for every attempt the script makes, and rule 8 can neither charge those
# attempts nor say they are absent, so charging the occurrence nothing would be this check
# stating a figure it did not compute. One naming a script that fetches nothing is a wait of no
# length either way, and is left alone. Where the script fetches is read through its recognised
# calls alone, which is all this check reads of what it runs.
for path, line, who, occurrence in unread_calls:
    if retries_nothing(occurrence.target, set()):
        continue
    if occurrence.form == "a function body":
        cure = ("Call the script outside the function: let the function compute what the call "
                "needs, and write the call itself where the function is called, with the "
                "script's path as its command word. A function runs the script once for every "
                "time it is called, from wherever it is called, and this check does not follow "
                "a function's calls to a script")
    elif occurrence.form == "a process substitution":
        # The path may be the command word already, inside the `<(…)`, so telling the reader
        # to write it as one is advice they cannot follow: the substitution is what this check
        # does not read, and the rewrite takes the run out of it (dispatch://INFRA-1545, round 37).
        cure = ("Take the run out of the substitution: run the script as a command of its own, "
                "with its output written to a file, and name that file where the substitution "
                f"stood, so `cmd <({occurrence.target} …)` becomes `{occurrence.target} … > "
                "out.txt` and then `cmd out.txt`; for a `>(…)`, write what fed it to a file and "
                f"run `{occurrence.target} … < that-file`. A command of its own is a run, which "
                "this check charges once for each time the step runs it")
    elif occurrence.form == "a source inside a loop":
        cure = ("Source the library once, before the loop: its functions stay defined for every "
                "pass, and this check charges a library once for each shell that sources it. A "
                "library whose own commands fetch each time it is sourced is a script: run it "
                f"in the loop as `{occurrence.target}`, a run this check charges once for each "
                "pass")
    else:
        cure = (f"Write the path as the command word of a command of its own — "
                f"`{occurrence.target} …`, with nothing before it but variable assignments, at "
                "top level or as the whole of a `$( … )` — or source it at top level with "
                f"`. {occurrence.target}`, which are the two calls this check reads: a run it "
                "charges once for each time the step runs it, and a library it charges once for "
                "each shell that sources it")
    problem(path, line,
            f"{who} names {occurrence.target} in {occurrence.form}, a place this check does "
            "not read as a call, and that script fetches: it holds a retried call, or runs or "
            "sources a script that does. So whatever runs this line may wait for every attempt "
            "that script makes, and rule 8 can put no figure on that wait: it is not charged "
            f"to any budget, and this refusal stands in its place. {cure}. A path a step only "
            "prints is a path in a comment or an argument of `echo` or `grep` whose output no "
            "shell runs, which this check charges nothing and refuses nothing "
            "(dispatch://INFRA-1545, round 37).")


# A script that reaches itself, running or sourcing a script that runs or sources it again, there
# or through others, is refused by name where it fetches (dispatch://INFRA-1545, round 37): a
# path stops at a script it is already inside, so the run the shell makes on entering it again is
# charged nothing, and a guard that ends the cycle is a count this check does not read. The
# refusal stands at the first call of the script that leads back to it. No script of this tree
# reaches itself.
def leads_back(path: str, call: Occurrence) -> bool:
    """Whether this recognised call written in `path` reaches `path` again, through the
    recognised calls of the scripts it reaches."""
    reached: set[str] = set()
    waiting = [call.target]
    while waiting:
        target = waiting.pop()
        if target == path:
            return True
        if target not in reached:
            reached.add(target)
            waiting += [later.target for later in script_reach.get(target, [])]
    return False


for path in scripts:
    back = next((call for call in script_reach[path] if leads_back(path, call)), None)
    if back is not None and not retries_nothing(path, set()):
        problem(path, back.shown,
                f"{path} runs or sources {back.target}, and that call reaches {path} again, "
                "there or through the scripts it calls in turn, so one run of it can run it once "
                f"more, and {path} fetches: it holds a retried call, or runs or sources a script "
                "that does. This check stops a path at a script it is already inside, so that "
                "second run is charged nothing, and a guard that ends the cycle is a count it "
                "does not read. Write the calls so that no script reaches itself: move what both "
                f"need into a library neither of them runs, and run {path} from one place "
                "(dispatch://INFRA-1545, round 37).")

# A script under .github/scripts/ whose name SCRIPT_CALL reads no occurrence of — one holding an
# underscore, a capital or another dot — is refused by name where it fetches
# (dispatch://INFRA-1545, round 37): a run or a source of it is no occurrence, so it is charged
# nothing and refused nothing, while rules 1 to 6 read its own sites. Every script of this tree
# is named in that form.
for path in scripts:
    if not SCRIPT_CALL.fullmatch("/" + Path(path).name) and not retries_nothing(path, set()):
        problem(path, 1,
                f"{path} is a script under .github/scripts/ that fetches, and its name is one "
                "this check finds no call of: a script's name is read as lowercase letters, "
                "digits and `-` before `.sh`, so a step that runs or sources this one is charged "
                "nothing for it and refused nothing, while its own fetches are held to the "
                "floor. Rename it in that form (dispatch://INFRA-1545, round 37).")


# Three shapes rule 8 charges under what the step waits for, each written in this tree, so each is
# refused by name unless a declaration writes its figure by hand (dispatch://INFRA-1545, round
# 37): a function called through a variable, which no reading charges; a library a step reaches
# along more than one path, which is charged once at its most expensive path; and a library's
# fetching functions called more than once after sourcing it, which are charged as one call.


def commands_read(commands: list[Command], text: str) -> list[tuple[int, Command, bool]]:
    """The commands CallScanner read of a body, `text`, that bash runs as commands, each with
    its index and whether it may be a pattern's last alternative instead. A `case` pattern
    `x)` after `in`, `;;` or `;&` and an operand of a `[[ … ]]` read as commands of their own
    words, and are left out, with the words of a `case`, a `for` or a `select` and an array's
    elements. An operand is the command a `[[` opens, as its first word after the reserved words,
    unquoted, and each command after it that `||` or `&&` splits off, up to the one ending in an
    unquoted `]]`. A `x)` after a `|` is a pattern's last alternative, `a|"$x")`, or the last
    command of a pipeline inside a `( … )` or a `$( … )`, `: | "$x")`, a call: this reading does
    not tell the two apart, so it is kept, and marked (dispatch://INFRA-1545, round 37)."""
    read = []
    in_test = False
    for index, command in enumerate(commands):
        words = command.words
        quoted = command.quoted or [False] * len(words)
        offset = command.offset or 0
        lead = next((at for at, word in enumerate(words)
                     if word not in ("if", "elif", "while", "until", "then", "do", "else", "!",
                                     "time")), len(words))
        opens = lead < len(words) and words[lead] == "[[" and not quoted[lead]
        split = re.search(r"(?:&&|\|\|)$", text[:offset].rstrip()) is not None
        operand = opens or (in_test and split)
        in_test = operand and not (words[-1] == "]]" and not quoted[-1])
        if words[0] in ("case", "for", "select") or operand or "array" in command.context:
            continue
        after_bar = False
        if len(words) == 1 and re.match(r"\S+\s*\)", text[offset:]):
            before = text[:offset].rstrip()
            if re.search(r"(?:\bin|;;|;&)$", before):
                continue
            after_bar = re.search(r"(?<!\|)\|$", before) is not None
        read.append((index, command, after_bar))
    return read


def variable_calls(loops: Loops) -> list[tuple[Command, str]]:
    """Each command of a body whose command word, once the wrappers reduce_words strips are
    gone, is a variable's value, with that word: a call of whatever the variable names. It is
    read with CallScanner, which keeps a `$( … )` inside the word holding it and an array's
    elements apart, and commands_read says which of its commands are commands. Until round 37
    every command holding the word `[[` or `]]` was left out, so `"$fn" ]]` ran a fetching
    function this check read as a test (dispatch://INFRA-1545, round 37).

    A `$x)` after a `|`, which may be a pattern's last alternative, is left out here and kept in
    VARIABLE_PATTERNS_READ, and refused by name where a function the text can see fetches
    (dispatch://INFRA-1545, round 37)."""
    if id(loops) in VARIABLE_CALLS_READ:
        return VARIABLE_CALLS_READ[id(loops)]
    found = []
    patterns = []
    commands = CallScanner(loops.first_line).scan(loops.text)
    for _index, command, after_bar in commands_read(commands, loops.text):
        reduced = reduce_words(command.words)[0]
        if not reduced or not VARIABLE_CALL.fullmatch(reduced[0]):
            continue
        (patterns if after_bar else found).append((command, reduced[0]))
    VARIABLE_CALLS_READ[id(loops)] = found
    VARIABLE_PATTERNS_READ[id(loops)] = patterns
    return found


# variable_calls's answer for each body it has read, by the body's Loops, and the `$x)` after a
# `|` it left out of that answer.
VARIABLE_CALLS_READ: dict[int, list[tuple[Command, str]]] = {}
VARIABLE_PATTERNS_READ: dict[int, list[tuple[Command, str]]] = {}


# THE ONE REFUSAL OF A RUN THIS CHECK CANNOT NAME (dispatch://INFRA-1545, round 37). Rule 8 reads
# a script's run where its literal path is written, and charges a script under .github/scripts/
# alone, so a run whose target is built from data as the step runs, or is a file the step wrote,
# may run such a script with its attempts charged to no budget. Until round 37 each such shape
# the reviews found had a model of its own, and each round's newest model held the next
# undercount: `cat > run.sh` holding a path and then `bash run.sh`, `echo <path> > run.sh` and
# then `bash run.sh`, `read -r p` from a heredoc and then `"$p"`, `p=$(echo <path>)` and then
# `"$p"`, and `echo <path> | xargs bash` each passed a 1650 s call under a one-minute key. So the
# run itself is refused, whatever printed, wrote or assembled the path (unnamed_runs). HANDED_ON
# is the arguments a wrapper function hands on to its command, `"$@"`, `"${@}"` and an array's
# `"${cmd[@]}"`: the reading of calls through a variable decides what such a call runs
# (variable_calls), so this refusal takes none of them for a run's target. A function whose body
# runs one of its own positional parameters, `run() { "$@" >> "$log"; }`, runs a word its caller
# hands it, so a call of it is a run too, of the word in that place: `run "$p"` and `run
# "$(cmd)"` are refused as `"$p"` is. A function handing it its own parameter, `wrap() { run
# "$@"; }`, is read as one too, so `wrap "$p"` is refused, and a script handing on its own
# arguments, `run "$@"` at its top level, is refused, since its callers are not followed
# (runners_of; dispatch://INFRA-1545, round 37).
HANDED_ON = re.compile(r"\$(?:@|\{@\}|\{[A-Za-z_]\w*\[@\]\})")
# A positional parameter as a command word: `$1` to `$9`, `${n}`, `$@`, `$*`, `${@}` and `${*}`.
POSITIONAL = re.compile(r"\$(?:([1-9])|\{([1-9][0-9]*)\}|[@*]|\{[@*]\})")
UNNAMED_RUN = (
    "{does}, {why}, so this check cannot name the script that runs there. This check reads a "
    "run of a script only where the script's literal path is written, as the command word or as "
    "the operand of `bash`, `sh`, `.` or `source`, and refuses a path built from data, or a file "
    "written, as the step runs rather than guess what it holds: a script under .github/scripts/ "
    "that fetches may run there, and its attempts would be charged to no budget. {cure}")
UNNAMED_CURE = ("Write the script's literal path at the run, `.github/scripts/<name>.sh …`, and "
                "hand it what varies as its arguments")
UNNAMED_CALL_CURE = ("Hand `{name}` the script or the command it runs by its literal name, "
                     "`{name} .github/scripts/<name>.sh …` or `{name} grep …`, and what varies as "
                     "the arguments after it")


def runners_of(loops: Loops) -> dict[str, tuple[int, bool]]:
    """Each function a body defines whose body runs one of its own positional parameters, with
    the place of the argument a call of it runs, counted from 1, and whether that place is
    exact (dispatch://INFRA-1545, round 37). `"$1"` runs the first argument and `"$@"` the
    first one left, each after the `shift`s written in the body before that run, and a `shift`
    whose count is no literal number leaves the place inexact, the place or any after it. A
    variable the body sets to one of them, `local s=$1` or `s=$1`, is that parameter, so a body
    running `"$s"` runs it too: until round 37 only a parameter written as the command word was
    read, and `acc_run() { local s=$1; "$s"; }` handed a path built from data ran a script no
    reading named. A function whose body hands such a function one of its own positional
    parameters in the place that function runs, `wrap() { run "$@"; }`, runs that parameter too,
    as the reading of calls through a variable follows a library's holders up their callers
    (variable_holders), so the call refused is the one handing `wrap` a word built from data.
    This tree defines three: ci-retry.sh's `retry_fetch`, which reduce_words reads as a wrapper
    instead, and check-plugin-names.sh's and check-private-names.sh's `run`."""
    if id(loops) in RUNNERS_READ:
        return RUNNERS_READ[id(loops)]
    text = without_arithmetic(loops.text)
    commands = CallScanner(loops.first_line).scan(text)
    holders = function_bodies(commands)[0]
    bodies: dict[str, list[list[str]]] = {}
    for index, command, _after_bar in commands_read(commands, text):
        if holders[index] is not None:
            bodies.setdefault(holders[index], []).append(command.words)
    found: dict[str, tuple[int, bool]] = {}
    grown = True
    while grown:
        grown = False
        for holder, body in bodies.items():
            count, exact = 0, True
            copies: dict[str, int] = {}
            for words in body if holder not in found else ():
                for word in words:
                    copy = re.fullmatch(r"([A-Za-z_]\w*)=(.*)", word)
                    if copy and (value := POSITIONAL.fullmatch(copy.group(2))):
                        copies[copy.group(1)] = int(value.group(1) or value.group(2) or 1) + count
                reduced = reduce_words(words)[0]
                if not reduced:
                    continue
                if reduced[0] == "shift":
                    step = reduced[1] if len(reduced) > 1 else "1"
                    count, exact = (count + int(step), exact) if step.isdigit() else (count, False)
                    continue
                runs = POSITIONAL.fullmatch(reduced[0])
                place = int(runs.group(1) or runs.group(2) or 1) + count if runs else None
                name = re.fullmatch(r"\$\{?([A-Za-z_]\w*)\}?", reduced[0])
                if place is None and name and name.group(1) in copies:
                    place = copies[name.group(1)]
                sure_here = exact
                if place is None and reduced[0] in found and reduced[0] != holder:
                    inner, sure = found[reduced[0]]
                    handed = reduced[inner:inner + 1] if sure else reduced[inner:]
                    runs = next(filter(None, map(POSITIONAL.fullmatch, handed)), None)
                    place = int(runs.group(1) or runs.group(2) or 1) + count if runs else None
                    sure_here = exact and sure and not any(
                        POSITIONAL.fullmatch(word) for word in reduced[1:inner])
                if place is not None:
                    found[holder] = (place, sure_here)
                    grown = True
                    break
    RUNNERS_READ[id(loops)] = found
    return found


# runners_of's answer for each body it has read, by the body's Loops.
RUNNERS_READ: dict[int, dict[str, tuple[int, bool]]] = {}


def without_arithmetic(text: str) -> str:
    """The text with each `(( … ))` and `$(( … ))` blanked, its newlines kept: the words of an
    arithmetic expression are numbers and names, and run nothing."""
    chars = list(text)
    for match in re.finditer(r"\(\(", text):
        depth = 0
        for index in range(match.start(), len(text)):
            depth += (text[index] == "(") - (text[index] == ")")
            if chars[index] != "\n":
                chars[index] = " "
            if depth == 0:
                break
    return "".join(chars)


def expansions_in(word: str) -> list[tuple[int, int]]:
    """The spans of the expansions in a word, its quotes removed: a parameter, a `${…}`, a
    `$(…)`, backticks, and the EXPR a GitHub expression is written as before the shell is read
    (commands_of)."""
    spans: list[tuple[int, int]] = []
    index = 0
    while index < len(word):
        start = index
        parameter = re.match(r"\$(?:[A-Za-z_]\w*|[0-9@*#?$!-])", word[index:])
        if word.startswith("EXPR", index):
            index += 4
        elif word[index] == "`":
            close = word.find("`", index + 1)
            index = len(word) if close < 0 else close + 1
        elif word[index] == "$" and word[index + 1:index + 2] in ("(", "{"):
            opener = word[index + 1]
            closer = ")" if opener == "(" else "}"
            depth = 0
            index += 1
            while index < len(word):
                depth += (word[index] == opener) - (word[index] == closer)
                index += 1
                if depth == 0:
                    break
        elif parameter:
            index += parameter.end()
        else:
            index += 1
            continue
        spans.append((start, index))
    return spans


def unnamed_target(word: str, action: str | None) -> str | None:
    """Why a run's target, `word`, could be a script under .github/scripts/ that this check
    cannot name, or None where it cannot (dispatch://INFRA-1545, round 37). It could be one where
    its last path element is wholly expansions, where that element holds an expansion and ends in
    `.sh`, or where its literal directory is .github/scripts/ or one inside it and that element
    holds an expansion. It cannot where it is written with no expansion; where its last element
    is literal, since a script under .github/scripts/ is a file directly in it and its literal
    name is what SCRIPT_CALL reads, so `"$script_dir/ci-retry.sh"` is that script's run or
    source and `"$RUNNER_TEMP/run.sh"` is no script of that directory; where that element is no
    `.sh` and the directory no .github/scripts/, a tool or a data file such as
    `"$state_dir/$name.state"`; and where its directory is literal and outside
    .github/scripts/, as `bash "scripts/e2e/$SCRIPT"` is, a script outside the one directory
    this check reads, the reach the header states. `$GITHUB_WORKSPACE/` is the repository's
    root, and in a composite action `$GITHUB_ACTION_PATH/` is the action's own directory,
    `action`."""
    if HANDED_ON.fullmatch(word):
        return None
    if action is not None:
        word = re.sub(r"^\$\{?GITHUB_ACTION_PATH\}?(?=/)", action, word)
    word = re.sub(r"^\$\{?GITHUB_WORKSPACE\}?(?=/)", ".", word)
    spans = expansions_in(word)
    cut = max((at for at, char in enumerate(word)
               if char == "/" and not any(start <= at < end for start, end in spans)), default=-1)
    last = word[cut + 1:]
    last_spans = expansions_in(last)
    if not last_spans:
        return None
    inside = False
    if cut >= 0 and all(start > cut for start, _end in spans):
        inside = f"/{os.path.normpath(word[:cut] or '/')}/".find(f"/{SCRIPTS}/") >= 0
        if not inside:
            return None
    if sum(end - start for start, end in last_spans) == len(last):
        return "whose last path element is an expansion"
    if last.endswith(".sh"):
        return "which ends in `.sh` and holds an expansion"
    if inside:
        return f"which names a file under {SCRIPTS}/ through an expansion"
    return None


def written_files(scanner: CallScanner, commands: list[Command],
                  action: str | None) -> dict[str, bool]:
    """The files a body writes, each as its word is written, normalised, with whether what it
    holds could be a script under .github/scripts/ or text the step makes, True, or is a tool,
    False (dispatch://INFRA-1545, round 37). Text: the target of a redirect of output, but of a
    download; each file `tee` names; and the value of `of=` and of any other command's `-o`,
    `--output` or `--output=`, as dd and `sort -o` write. A tool: what curl or wget downloads, by
    a redirect, `-o`, `-O`, `--output` or `--output-document`, and what `go build -o` builds. A
    copy, the last operand of `cp`, `mv`, `install`, `ln`, `rsync` or `scp`, holds what its
    source holds: text where a source could be such a script (unnamed_target), names one, or is
    text this body wrote, and a tool otherwise, as install-jj's `install -m 0755
    "$dir/mise/bin/mise" "$HOME/.local/bin/mise"` is. Until round 37 `dd of=` was none of these,
    so `echo <path> | dd of=run.sh` and then `bash run.sh` ran a script no reading named."""
    files = {os.path.normpath(word): True for word in scanner.written if word}

    def holds_text(word: str) -> bool:
        return bool(unnamed_target(word, action)
                    or any(str(SCRIPTS / match.group(1)) in script_files
                           for match in SCRIPT_CALL.finditer(word))
                    or files.get(os.path.normpath(word)))

    for command in commands:
        reduced = reduce_words(command.words)[0]
        head = PROGRAM.sub("", reduced[0]).rsplit("/", 1)[-1] if reduced else ""
        tool = head in ("curl", "wget") or (head == "go" and "build" in reduced)
        operands = [word for word in reduced[1:] if not word.startswith("-")]
        found = [(word, not tool) for word in command.writes]
        if head == "tee":
            found += [(word, True) for word in operands]
        elif head in ("cp", "mv", "install", "ln", "rsync", "scp") and len(operands) > 1:
            found.append((operands[-1], any(map(holds_text, operands[:-1]))))
        found += [(after, not tool) for word, after in zip(reduced, reduced[1:])
                  if word in ("-o", "-O", "--output", "--output-document")]
        found += [(word.split("=", 1)[1], not tool or word.startswith("of=")) for word in reduced
                  if word.startswith(("--output=", "--output-document=", "of="))]
        for word, text in found:
            if word:
                files[os.path.normpath(word)] = files.get(os.path.normpath(word), False) or text
    return files


def shell_operand(words: list[str]) -> tuple[str, str]:
    """What a shell, `.` or `source`, its words once the wrappers are gone, runs: ("script",
    the operand), ("text", the text a `-c` names), or ("stdin", "") where it reads its standard
    input. A shell's options before the operand are skipped, `-o`, `+o`, `-O`, `+O`, `--rcfile`
    and `--init-file` with their values, and `--` or `-` ends them."""
    if PROGRAM.sub("", words[0]).rsplit("/", 1)[-1] in (".", "source"):
        return ("script", words[1]) if len(words) > 1 else ("stdin", "")
    at = 1
    while at < len(words):
        word = words[at]
        if word in ("--", "-"):
            at += 1
            break
        if word in ("-o", "+o", "-O", "+O", "--rcfile", "--init-file"):
            at += 2
        elif word.startswith(("-", "+")):
            if not word.startswith("--") and "c" in word[1:]:
                return "text", words[at + 1] if at + 1 < len(words) else ""
            if not word.startswith("--") and "s" in word[1:]:
                return "stdin", ""
            at += 1
        else:
            break
    return ("script", words[at]) if at < len(words) else ("stdin", "")


# The words of `find` that run a command on each file it finds (unnamed_runs).
FIND_RUNS = {"-exec", "-execdir", "-ok", "-okdir"}
# Where a shell reading its program from its standard input takes it, by the redirect of that
# input CallScanner read; a heredoc's body is read by unread_heredocs instead (unnamed_runs).
STDIN_PROGRAM = {"<<<": "a here-string", "<": "a file redirected into it",
                 "<(": "a process substitution", "": "its standard input"}


def unnamed_runs(path: str, loops: Loops, sees_fetching: bool,
                 runners: dict[str, tuple[int, bool]],
                 library: bool) -> list[tuple[int, int, str, str, str]]:
    """Each run of a body whose target could be a script under .github/scripts/ that this check
    cannot name, or whose route from text to a program this check does not follow, as (the
    first and the last line of its command, what the command does, why it is refused, the form
    to write instead), UNNAMED_RUN's three parts (dispatch://INFRA-1545, round 37). One class,
    refused by its route rather than modelled shape by shape:
      - a target, the operand of `bash`, `sh`, `dash`, `zsh`, `.` or `source`, the text of a
        shell's `-c` where it is no single-quoted text, or a command word, refused where
        unnamed_target says it could be such a script; where it names a script with no
        directory, `bash x.sh` or `x.sh`, which bash finds through the working directory or
        PATH, so after a `cd` into .github/scripts/ or with PATH reaching it; and where it is a
        file this body writes (written_files), by any name, compared by its path and, where the
        body holds a `cd` or a `pushd`, by its name alone, since this reading does not follow
        the directory. A command word holding no `/` is a name PATH finds, and no such file;
      - a shell reading its program from its standard input, a here-string, a file redirected
        into it or a process substitution, and a `.` or `source` of a process substitution; a
        heredoc's body is read by unread_heredocs, and a shell a pipe feeds is refused at the
        command whose output the pipe hands it;
      - `eval`, whatever it is handed, and `find` with `-exec`, `-execdir`, `-ok` or `-okdir` and
        `xargs` where the command they run on what they read could run such a script: a shell,
        `eval`, `.` or `source`; a wrapper and nothing else, `xargs env`, which runs each word
        it reads; the placeholder itself, `-exec {} \\;` or `xargs -I{} {}`; or a target
        refused as above. `xargs go test …` and `-exec rm -rf {} +` run a command named
        literally, and pass: the tree writes three of the first and one of the second.
    Until round 37 each of those routes ran a script that fetches and was charged nothing:
    round 37's probe at aaab38b5 found 14, from `eval "$p"` to `xargs env`. A file this body
    writes is refused as a command word where it holds text or a copy of such a script, and
    passes where it holds a tool the body downloads or installs from a file named literally,
    as install-jj's `"$HOME/.local/bin/mise"` does; as the operand of a shell, `.` or `source`
    it is refused whatever it holds, since the shell runs it as shell text.

    A command word that is one parameter's value, `"$fn"`, is a call through a variable, which
    the reading of such calls holds where the text can see a function that fetches,
    `sees_fetching` (variable_calls): such a call is read there and not here. Inside a
    function's body, one whose value the function's callers hand it is read at those callers,
    and not here: a positional parameter or a variable the body sets to one, `"$1"` or `local
    s=$1; "$s"` (runners_of), and, in a library some text sources (`library`), a variable that
    library sets nowhere, `r37_go() { "$r37_fn"; }`, whose value the sourcing text hands it
    and whose call the holder reading follows there (variable_holders). Any other, `s=$(…);
    "$s"`, or one a step's own text runs inside a function, is a target like any other: until
    round 37 every call through a variable inside a function's body was left to those
    readings, which follow callers and never what the body sets. And a call of one of
    `runners`, a function whose body runs one of its positional parameters or a variable set to
    one (runners_of), runs the word in that place, which is refused as any other target is. A
    positional parameter handed on there is the holder's own run where the holder is one of
    `runners` too (runners_of), read at its callers, and refused anywhere else, since this
    reading does not follow a script's callers. A literal word, `run grep …`, and a literal path
    pass."""
    text = without_arithmetic(loops.text)
    scanner = CallScanner(loops.first_line)
    commands = scanner.scan(text)
    action = str(Path(path).parent) if path in actions else None
    written = written_files(scanner, commands, action)
    moved = any(reduce_words(command.words)[0][:1] in (["cd"], ["pushd"])
                for command in commands)
    # The names the text sets, or writes as a word, `s=…`, `s+=…`, `s[…]=…`, `read -r s`, `for s
    # in`, and those each function's body sets to one of its positional parameters.
    named: set[str] = set()
    copies: dict[str | None, set[str]] = {}
    for command in commands:
        for word in command.words:
            setting = re.fullmatch(r"([A-Za-z_]\w*)(=|\+=|\[)(.*)", word, re.DOTALL)
            if setting and setting.group(2) == "=" and POSITIONAL.fullmatch(setting.group(3)):
                copies.setdefault(loops.function_of(command.line), set()).add(setting.group(1))
            elif setting or re.fullmatch(r"[A-Za-z_]\w*", word):
                named.add(setting.group(1) if setting else word)

    def handed_in(command: Command, word: str) -> bool:
        """Whether a call through the variable `word`, inside a function's body, runs a value
        the function's callers hand it, which the readings of runners and holders follow."""
        holder = loops.function_of(command.line)
        if holder is None:
            return False
        name = re.match(r"\$\{?([A-Za-z_]\w*)", word)
        if name is None:
            return True
        return (name.group(1) in copies.get(holder, set())
                or (library and name.group(1) not in named))

    def unseen(target: str, operand: bool) -> str | None:
        """Why a literal target is a file this check cannot name, or None: an operand of a shell,
        `.` or `source` where it is any file this body writes, a command word holding a `/`
        where it is one holding text or a copy of a script (written_files), and either where it
        names a script with no directory."""
        if operand or "/" in target:
            files = {word for word, text in written.items() if operand or text}
            if os.path.normpath(target) in files:
                return "a file this text writes before it runs it"
            if moved and os.path.basename(os.path.normpath(target)) in map(os.path.basename,
                                                                          files):
                return ("the name of a file this text writes elsewhere, which a `cd` or a "
                        "`pushd` this check does not follow may make the file it runs")
        if "/" not in target and target.endswith(".sh") and not expansions_in(target):
            return ("which names a script with no directory, so bash finds it through the "
                    "working directory or `PATH`, neither of which this check follows")
        return None

    def handed(words: list[str], placeholder: str | None, read: str) -> str | None:
        """Why the command `xargs` or `find` runs on `read`, what it reads, could run a script
        this check cannot name, or None where it names it literally."""
        if not words:
            return None
        reduced = reduce_words(words)[0]
        if not reduced:
            return f"which hands {read} to `{words[0]}`, which runs it as a command"
        if placeholder is not None and placeholder in reduced[0]:
            return f"which runs {read} as a command"
        if PROGRAM.sub("", reduced[0]).rsplit("/", 1)[-1] in INTERPRETERS | {"eval", ".",
                                                                          "source"}:
            return f"which runs `{reduced[0]}` on {read}"
        why = unnamed_target(reduced[0], action) or unseen(reduced[0], False)
        return why and f"which runs `{reduced[0]}`, {why}"

    fed: set[int] = set()
    found = []
    for index, command, after_bar in commands_read(commands, text):
        reduced = reduce_words(command.words)[0]
        if not reduced or after_bar:
            continue
        head = PROGRAM.sub("", reduced[0]).rsplit("/", 1)[-1]
        why = None
        if head == "eval":
            does = f"runs `eval` on `{' '.join(reduced[1:])}`"
            why = "which runs those words as shell text"
        elif head == "find":
            for at, word in enumerate(reduced):
                if word in FIND_RUNS:
                    end = next((stop for stop in range(at + 1, len(reduced))
                                if reduced[stop] in (";", "+")), len(reduced))
                    why = handed(reduced[at + 1:end], "{}", "each file it finds")
                    if why:
                        does = f"runs `find` with `{word}`"
                        break
        elif head == "xargs":
            placeholder = None
            at = 1
            while at < len(reduced) and reduced[at].startswith("-"):
                word = reduced[at]
                if word in ("-I", "-n", "-L", "-P", "-d", "-a", "-E", "-s"):
                    placeholder = reduced[at + 1] if word == "-I" and at + 1 < len(reduced) else (
                        placeholder)
                    at += 2
                    continue
                if word.startswith("--replace"):
                    placeholder = word.partition("=")[2] or "{}"
                elif word.startswith("-i"):
                    placeholder = word[2:] or "{}"
                elif word.startswith("-I"):
                    placeholder = word[2:]
                at += 1
            does, why = "runs `xargs`", handed(reduced[at:], placeholder, "each word it reads")
        elif head in INTERPRETERS | {".", "source"} and not expansions_in(reduced[0]):
            kind, target = shell_operand(reduced)
            at = len(command.words) - len(reduced) + reduced.index(target) if target else 0
            span = command.spans[at] if target else None
            if (kind == "stdin" and index not in fed and command.input != "<<"
                    and (head in INTERPRETERS or command.input == "<(")):
                # A `.` or a `source` given no file runs nothing, but given a process
                # substitution it runs what that prints.
                does = f"runs `{head}`"
                why = f"which reads the program it runs from {STDIN_PROGRAM[command.input]}"
            elif kind == "text" and span and text[span[0] - 1:span[0]] != "'":
                does = f"runs `{head} -c` on `{target}`"
                why = unnamed_target(target, action) and "text built from data"
            elif kind == "script":
                does = (f"sources `{target}`" if head in (".", "source")
                        else f"runs `{target}` with `{head}`")
                why = unnamed_target(target, action) or unseen(target, True)
        elif not (VARIABLE_CALL.fullmatch(reduced[0])
                  and (sees_fetching or handed_in(command, reduced[0]))):
            target = reduced[0]
            does = f"runs `{target}` as a command"
            why = unnamed_target(target, action) or unseen(target, False)
        ends = [span[1] for span in command.spans if span]
        last = loops.first_line + text.count("\n", 0, max(ends)) if ends else command.line
        if why:
            found.append((command.line, last, does, why, UNNAMED_CURE))
        callee = reduced[0]
        holder = loops.function_of(command.line)
        if callee in runners and holder != callee:
            place, exact = runners[callee]
            handed = reduced[place:place + 1] if exact else reduced[place:]
            for word in handed:
                if holder in runners and POSITIONAL.fullmatch(word):
                    # The holder runs that parameter itself, and its callers are read.
                    continue
                why = ("which hands on an argument of the function or the script it is written "
                       "in, whose callers this check does not follow"
                       if HANDED_ON.fullmatch(word) or POSITIONAL.fullmatch(word)
                       else unnamed_target(word, action))
                if why:
                    found.append((command.line, last,
                                  f"calls `{callee}`, which runs the word it is handed, with "
                                  f"`{word}`", why, UNNAMED_CALL_CURE.format(name=callee)))
                    break
        if command.piped:
            consumer = next((at for at, other in enumerate(commands[index + 1:], index + 1)
                             if other.context == command.context), None)
            words = reduce_words(commands[consumer].words)[0] if consumer is not None else []
            if (words and PROGRAM.sub("", words[0]).rsplit("/", 1)[-1] in INTERPRETERS
                    and shell_operand(words)[0] == "stdin"):
                fed.add(consumer)
                found.append((command.line, last, f"pipes the output of `{head}` into "
                              f"`{words[0]}`", "which runs it as shell text", UNNAMED_CURE))
    return found


def fetching_functions(found: list[Site], loops: Loops) -> set[str]:
    """The functions a shell body defines whose body holds a fetch, or which call such a
    function in turn, as structure reads them. Where any does, a function holding a call
    through a variable is one of them too, since that call can run any of them."""
    names = {name for name, (start, end) in loops.function_span.items()
             if any(start <= site.line <= end and one_call(site) != 0 for site in found)}
    if names:
        names |= {name for command, _ in variable_calls(loops)
                  if (name := loops.function_of(command.line)) is not None}
    grown = True
    while grown:
        grown = False
        for name in list(names):
            for line in loops.function_calls.get(name, ()):
                caller = loops.function_of(line)
                if caller is not None and caller not in names:
                    names.add(caller)
                    grown = True
    return names


def libraries_of(path: str, seen: tuple[str, ...] = ()) -> list[str]:
    """This script and every library it sources in turn, in the order the sources are written."""
    if path in seen or path not in script_read:
        return []
    found = [path]
    for call in script_reach.get(path, []):
        if call.kind == CALL_SOURCED:
            found += [library for library in libraries_of(call.target, seen + (path,))
                      if library not in found]
    return found


def library_definitions(path: str) -> dict[str, str]:
    """Every function a library defines, and every library it sources in turn, each with the
    library defining it. A name two of them define is refused by name in the reading of the
    file sourcing both (dispatch://INFRA-1545, round 37)."""
    named: dict[str, str] = {}
    for library in libraries_of(path):
        for name in script_read[library][1].function_span:
            named.setdefault(name, library)
    return named


def library_functions(path: str) -> dict[str, str]:
    """The fetching functions a library defines, each with the library defining it, and those
    of every library it sources in turn."""
    named = library_definitions(path)
    fetching = {library: fetching_functions(*script_read[library])
                for library in set(named.values())}
    return {name: library for name, library in named.items() if name in fetching[library]}


def variable_holders(path: str) -> dict[str, tuple[str, str]]:
    """The functions of a library, and of every library it sources in turn, that call a function
    through a variable, directly or through a call of another such function, each with the
    library defining it and the variable's word. Such a function runs whatever its caller
    hands it, which no reading of the library sees (dispatch://INFRA-1545, round 37)."""
    libraries = libraries_of(path)
    holders: dict[str, tuple[str, str]] = {}
    for library in libraries:
        loops = script_read[library][1]
        for command, word in variable_calls(loops):
            holder = loops.function_of(command.line)
            if holder is not None:
                holders.setdefault(holder, (library, word))
    grown = True
    while grown:
        grown = False
        for library in libraries:
            loops = script_read[library][1]
            for command in loops.commands:
                reduced = reduce_words(command.words)[0]
                holder = loops.function_of(command.line)
                if (holder is not None and holder not in holders and reduced
                        and reduced[0] in holders):
                    holders[holder] = (library, holders[reduced[0]][1])
                    grown = True
    return holders


def function_sites(found: list[Site], loops: Loops, name: str) -> list[Site]:
    """The fetches written in one function's body."""
    start, end = loops.function_span[name]
    return [site for site in found if start <= site.line <= end and one_call(site) != 0]


# A shape this check refuses by name rather than reads (unread_shapes_of), where what it can hide
# fetches: a call or a fetch it holds may run more often than, or where, this check would read it,
# so it is refused rather than charged (dispatch://INFRA-1545, round 37). It is read once the
# libraries are, since a call in its reach of a library's fetching function is such a call.
for path, line, who, sentence, waits, targets, names, sources in unread_shapes:
    if (waits or not all(retries_nothing(target, set()) for target in targets)
            or any(name in library_functions(source) for source in sources for name in names)):
        problem(path, line, f"{who} {sentence} (dispatch://INFRA-1545, round 37).")

# The library calls each body makes more than once, as (file, step name or "-", the step's
# Budget or None, what a refusal calls the body, {library: (line, the functions called)}).
library_calls: list[tuple[str, str, "Budget | None", str, dict[str, tuple[int, list[str]]]]] = []
# Every library some text sources, there or through another library: a function such a library
# defines runs what the sourcing text hands it, which the reading of holders follows there
# (unnamed_runs; dispatch://INFRA-1545, round 37).
SOURCED_LIBRARIES = {library for _path, _name, (_who, _loops, _found, called), _owner in body_reads
                     for call in called if call.kind == CALL_SOURCED
                     for library in libraries_of(call.target)}
for path, name, (who, loops, found, called), owner in body_reads:
    libraries: dict[str, str] = {}
    holders: dict[str, tuple[str, str]] = {}
    # Each function this text defines and each its libraries define, with the files defining it
    # in the order they are read: bash runs the definition it read last, and this check reads a
    # name as one function, so a name two of them define is refused by name where either
    # definition fetches. A name two libraries of one source define is that source's own
    # reading's (dispatch://INFRA-1545, round 37).
    places: dict[str, list[str]] = {function: [path] for function in loops.function_span}
    twice: dict[str, int] = {}
    for call in called:
        if call.kind != CALL_SOURCED:
            continue
        for function, library in library_functions(call.target).items():
            libraries.setdefault(function, library)
        for function, held in variable_holders(call.target).items():
            holders.setdefault(function, held)
        for function, library in library_definitions(call.target).items():
            if any(place != library for place in places.get(function, [])):
                twice.setdefault(function, call.shown)
            places.setdefault(function, []).append(library)
    local = fetching_functions(found, loops)
    # A function of this text or of a library holding a call through a variable runs whatever
    # its caller hands it, so a name two definitions share where one of them does is refused as
    # one that fetches is, and the refusal of a call of it below names no one definition: until
    # round 37 it named the first, and bash runs the one it read last (dispatch://INFRA-1545,
    # round 37).
    local_holders = {holder for command, _ in variable_calls(loops)
                     if (holder := loops.function_of(command.line)) is not None}
    for function, line in sorted(twice.items()):
        if function in local or function in libraries:
            does = "fetches"
        elif function in holders or function in local_holders:
            does = ("calls a function through a variable, which runs whatever its caller hands "
                    "it")
        else:
            continue
        files = " and ".join("this text" if place == path else place
                             for place in dict.fromkeys(places[function]))
        problem(path, line,
                f"{who} reaches two definitions of `{function}`, in {files}, and one of them "
                f"{does}. Bash runs the definition it read last, and this check reads one "
                "name as one function, so the calls of it can be charged at a body the step "
                "never runs. Give each definition a name of its own (dispatch://INFRA-1545, "
                "round 37).")
    defining: dict[str, set[str]] = {function: {path} for function in loops.function_span}
    waits = (any(one_call(site) != 0 for site in found)
             or not all(retries_nothing(call.target, set()) for call in called))
    for call in called:
        if call.kind != CALL_SOURCED:
            continue
        own = library_definitions(call.target)
        for library in libraries_of(call.target):
            reading = script_read[library][1]
            for function in reading.function_span:
                defining.setdefault(function, set()).add(library)
            # An alias a library defines applies to every line read after the source, this
            # text's included, so it is refused here too where this text fetches
            # (dispatch://INFRA-1545, round 37).
            if reading.aliases and waits:
                problem(path, call.shown,
                        f"{who} sources {library}, which defines an alias, and a fetch, or a "
                        "call of a script that fetches, is written in this text or in what it "
                        "calls. An alias holds from the line it is defined on to the end of the "
                        "shell that reads it, this text's lines after the source included, and "
                        "this check does not read one: `alias nice='for pass in 1 2 3; do'` "
                        "makes `nice retry_fetch …; done` three calls, which this check charges "
                        "as one. Write the words the alias stands for at each place it is used "
                        "(dispatch://INFRA-1545, round 37).")
            # A library's call of a function it neither defines nor sources runs whichever
            # definition the shell holds when the call runs, this text's or another library's,
            # and the library's own reading charges that call nothing (dispatch://INFRA-1545,
            # round 37).
            for command in reading.commands:
                reduced = reduce_words(command.words)[0]
                callee = reduced[0] if reduced else ""
                if callee in own or not (callee in local or callee in libraries):
                    continue
                holder = reading.function_of(command.line)
                where = f"in its function `{holder}`" if holder else "at its top level"
                problem(path, call.shown,
                        f"{who} sources {call.target}, and {library} calls `{callee}` {where}, "
                        f"a function {call.target} neither defines nor sources, which this "
                        "text defines or sources and which fetches. Bash finds the function a "
                        "name calls when the call runs, so that call runs the "
                        f"`{callee}` this text holds, and this check reads a library's functions "
                        "from the library alone, so the fetches it makes there are charged "
                        f"nothing. Define `{callee}` in {library}, or source the library that "
                        "defines it there (dispatch://INFRA-1545, round 37).")
                break
    # A call through a variable can run any of these functions, and is charged nothing: it is
    # refused unless a DECLARED_LOOPS entry writes the whole of each fetch it can reach, which
    # is how ci-tools.sh's `$fetch` calls are priced, by its `curl` entry, or, in a script, a
    # DeclaredCall entry writes what the calls through that variable add.
    reached = [site for function in sorted(local)
               for site in function_sites(found, loops, function)]
    reached += [site for function, library in sorted(libraries.items())
                for site in function_sites(*script_read[library], function)]
    owed = [site for site in reached if declared_arithmetic(site) is None]
    named = "; ".join(f"`{site.verb}` at {site.path}:{site.line}" for site in owed)
    # A run this check cannot name is refused by name, a call through a variable excepted where
    # the reading above holds it, and a command some refusal above already names this text at,
    # on any of its lines, excepted too: that command fails the check already, as `echo <path> |
    # bash` fails as a pipe into a shell, and a second sentence for it would say nothing the
    # first does not (unnamed_runs; dispatch://INFRA-1545, round 37).
    runners = dict(runners_of(loops))
    for call in called:
        for library in libraries_of(call.target) if call.kind == CALL_SOURCED else ():
            for function, place in runners_of(script_read[library][1]).items():
                runners.setdefault(function, place)
    for line, last, does, why, cure in unnamed_runs(path, loops, bool(reached), runners,
                                                    name == "-" and path in SOURCED_LIBRARIES):
        named_at = tuple(f"::error file={path},line={at}::{who} " for at in range(line, last + 1))
        if not any(earlier.startswith(named_at) for earlier in problems):
            problem(path, line, f"{who} " + UNNAMED_RUN.format(does=does, why=why, cure=cure)
                    + " (dispatch://INFRA-1545, round 37).")
    for command, word in variable_calls(loops) if owed else ():
        entry = next((entry for entry in DECLARED_CALLS
                      if name == "-" and (entry.path, entry.word) == (path, word)), None)
        if entry is not None:
            if not entry.used:
                script_cost[path] = added(script_cost.get(path, 0), entry.seconds)
            entry.used += 1
            continue
        problem(path, command.line,
                f"{who} calls a function through a variable, `{word}`, and a function this "
                f"text defines or sources fetches — {named}. This check reads a call of a "
                "function only where its name is written, so the fetches such a call runs are "
                "charged nothing. Write the function's name at the call; or declare each of "
                "those fetches in DECLARED_LOOPS in .github/scripts/check-download-retry.sh, "
                "whose figure is that fetch's whole contribution for every caller, or, in a "
                "script, declare what the calls through that variable add in DECLARED_CALLS "
                "there, with the sum it comes from (dispatch://INFRA-1545, round 37).")
        break
    # A `$x)` after a `|`, which variable_calls cannot tell from a pattern's last alternative, is
    # refused by name where a call there could run a function that fetches
    # (dispatch://INFRA-1545, round 37).
    for command, word in VARIABLE_PATTERNS_READ.get(id(loops), []) if owed else ():
        problem(path, command.line,
                f"{who} writes `{word})` after a `|`, which this check reads as the last "
                f"alternative of a `case` pattern, `a|{word})`, and not as a call through a "
                "variable, while bash reads it as the last command of a pipeline wherever no "
                f"`case` pattern holds it, `: | {word})`, a call of whatever that variable "
                f"names; and a function this text defines or sources fetches — {named}. Write "
                "the function's name at the call, or write a pattern's alternatives as words "
                "that are no variable (dispatch://INFRA-1545, round 37).")
        break
    # A library is charged once for each shell, so a body calling its fetching functions more
    # than once, or inside a loop whose passes this check cannot read, is charged one call. A
    # call of one after a word CALL_PREFIXES does not hold is no call this check reads, and one
    # of a library function that calls through a variable runs whatever this text hands it,
    # which the library's own reading cannot see: each is refused by name
    # (dispatch://INFRA-1545, round 37).
    counts: dict[str, int | None] = {}
    over: dict[str, tuple[int, list[str]]] = {}
    for command in loops.commands:
        reduced = reduce_words(command.words)[0]
        function = reduced[0] if reduced else ""
        if function in loops.function_span:
            continue
        if function in holders and owed and len(defining.get(function, ())) < 2:
            library, word = holders[function]
            problem(path, command.line,
                    f"{who} calls `{function}`, which {library} defines and which calls a "
                    f"function through a variable, `{word}`, and a function this text defines "
                    f"or sources fetches — {named}. Such a call runs whatever this text hands "
                    "it, and this check reads a call of a function only where its name is "
                    "written, so the fetches it runs are charged nothing. Call the function "
                    "that fetches by its name here (dispatch://INFRA-1545, round 37).")
        library = libraries.get(function)
        if library is None:
            continue
        prefix = command.words[:len(command.words) - len(reduced)]
        if tuple(prefix) not in CALL_PREFIXES:
            problem(path, command.line,
                    f"{who} " + CALL_REFUSAL.format(name=function, prefix=" ".join(prefix))
                    + " (dispatch://INFRA-1545, round 37).")
            continue
        times = times_run(loops, command.line, command.loops)[0]
        line, functions = over.setdefault(library, (command.line, []))
        functions.append(function)
        known = counts.get(library, 0)
        counts[library] = None if times is None or known is None else known + times
    over = {library: (line, sorted(set(functions))) for library, (line, functions) in over.items()
            if counts[library] is None or counts[library] > 1}
    if over:
        library_calls.append((path, name, owner, who, over))
# Each library reading refused, once, by its file, its line and its library.
refused_libraries: set[tuple[str, int, str]] = set()

for budget in budgets:
    # One shell for the whole step: the scripts its `run:` runs and sources and the action it
    # uses are its recognised calls, each run charged for every time the step runs it and each
    # library once, at its most expensive path. `seen` is every file the step reaches.
    seen: set[str] = set()
    SHARED_LIBRARIES.clear()
    cost = added(budget.cost, shell_arithmetic(budget.calls, seen, ()))
    # The libraries this step may run the fetches of more often than once: one it reaches along
    # more than one path, one it reaches along one path more than once, and one whose fetching
    # functions a body it runs calls more than once. Each is refused by name unless a
    # DeclaredLibrary entry covers it — the step's own entry first, then one for the file whose
    # calls are the reading — and the entries' seconds then replace each library's own
    # arithmetic in the step's figure (dispatch://INFRA-1545, round 37).
    mine = (budget.path, budget.step)
    owed: list[tuple[str, str, int, str, list[tuple[str, str | None]]]] = []
    for library, (holder, rerun) in sorted(SHARED_LIBRARIES.items()):
        where, line, who = ((budget.path, budget.line, f"step '{step_name(budget)}'")
                            if holder is None else (holder, 1, holder))
        sentence = (
            f"{who} reaches {library}, a library that fetches, along more than one path: it "
            "sources it, or runs scripts that source it, in more than one place, and each of "
            "those can call its functions. This check charges a library once for each shell, "
            "at its most expensive path, so what the other paths run of it is charged nothing. "
            "Declare what that library contributes to this step in DECLARED_LIBRARIES in "
            ".github/scripts/check-download-retry.sh, with the sum it comes from, or reach it "
            "along one path" if rerun is None else
            # A script that sources the library, run in more than one place: each run is a
            # shell of its own, and the path the runs give is one (dispatch://INFRA-1545, round
            # 37).
            f"{who} reaches {library}, a library that fetches, along one path more than once, "
            f"{' to '.join(rerun)}: it runs a script on that path, or sources one, in more than "
            "one place, and each run is a shell of its own that sources the library and can "
            "call its functions. This check charges a library once for the step, at its most "
            "expensive path, however many of the shells it runs source it, so what the other "
            "runs make of it is charged nothing. Declare what that library contributes to this "
            "step in DECLARED_LIBRARIES in .github/scripts/check-download-retry.sh, with the sum "
            "it comes from, or write the runs as one call in a `for` over literal words, whose "
            "passes this check multiplies")
        owed.append((library, where, line, sentence,
                     [mine] + ([] if holder is None else [(holder, None)])))
    for path, name, owner, who, over in library_calls:
        if owner is budget:
            keys = [mine]
        elif owner is None and path in seen:
            # The file whose calls these are, as a whole: a composite action's step is keyed by
            # the action, which is the form this tree writes, so the steps of one action cannot
            # match two entries that price one library differently; until round 37 one could,
            # and the first entry matched priced the library in every step of the action
            # (dispatch://INFRA-1545, round 37).
            keys = [mine, (path, None)]
        else:
            continue
        for library, (line, functions) in sorted(over.items()):
            owed.append((library, path, line,
                         f"{who} calls {', '.join(f'`{function}`' for function in functions)}, "
                         f"which {library} defines and which fetch, more than once or inside a "
                         "loop whose passes this check cannot read, after sourcing that library. "
                         "This check charges a library once for each shell that reaches it, "
                         "whatever its functions' calls, so every call after the first is charged "
                         f"nothing; step '{step_name(budget)}' of {budget.path} reaches it. "
                         "Declare what that library contributes to the step in DECLARED_LIBRARIES "
                         "in .github/scripts/check-download-retry.sh, with the sum it comes from, "
                         "or write each fetch where it is called", keys))
    # Where two entries price one library for this step, the first in `owed` is taken: the
    # step's own, which every reading asks for first, else the entry the shared reading names,
    # which is the composite action the step uses and so covers the scripts that action runs,
    # else a script's. The ci-ok report steps are this tree's instance: ci-ok-report/action.yml's
    # 1560 s over ci-ok-report.sh's 780 s (dispatch://INFRA-1545, round 37).
    priced: dict[str, int] = {}
    for library, where, line, sentence, keys in owed:
        entry = next((entry for key in keys for entry in DECLARED_LIBRARIES
                      if (entry.path, entry.step) == key and entry.library == library), None)
        if entry is None:
            if (where, line, library) not in refused_libraries:
                refused_libraries.add((where, line, library))
                problem(where, line, f"{sentence} (dispatch://INFRA-1545, round 37).")
            continue
        entry.used += 1
        priced.setdefault(library, entry.seconds)
    if priced:
        PRICED_LIBRARIES.update(priced)
        cost = added(budget.cost, shell_arithmetic(budget.calls, set(), ()))
        PRICED_LIBRARIES.clear()
    # The slowest retried step of each job, kept as this loop computes it — the step's own calls,
    # the scripts it runs and the composite action it uses — for the job-derivation clause below.
    # It is taken here and never recomputed there, because a `uses:` step's whole arithmetic is
    # the action's and is known only at this point. Beside it, the longest key a retried step of
    # the job writes, which the last refusal of this loop holds the job's key above, so the
    # advice of the job-derivation clause can name a key that refusal passes too
    # (dispatch://INFRA-1545, round 37).
    if cost is not None and budget.job_minutes is not None and budget.job_line is not None:
        slowest = JOB_SLOWEST.setdefault((budget.path, budget.job),
                                         {"line": budget.job_line, "minutes": budget.job_minutes,
                                          "cost": 0, "step": "", "key": 0, "key_step": ""})
        if cost > slowest["cost"]:
            slowest["cost"], slowest["step"] = cost, step_name(budget)
        if cost and budget.minutes is not None and budget.minutes > slowest["key"]:
            slowest["key"], slowest["key_step"] = budget.minutes, step_name(budget)
    blocking = list(budget.uncounted)
    for target in sorted(seen):
        blocking += script_uncounted.get(target, []) + action_uncounted.get(target, [])
    # The scripts are read by now, so each blocked script call can be asked what it waits for,
    # which is a question blocked_calls could not ask when it listed them. The ones that wait
    # for nothing are dropped here rather than refused: a loop around a script that retries
    # nothing holds no attempt this budget has to exceed. One filter answers the step's own
    # list and the lists of the scripts and actions it reaches alike, because a call of a
    # script that waits for nothing is the same wait of no length wherever it is written.
    blocking = [item for item in blocking if not waits_for_nothing(item)]
    # Two refusals and not one, so the items are told apart here. A loop with no pass count is
    # refused in the loop's place and its sentence names the line that loop sits on; a call
    # whose per-attempt bound is written as a variable is refused in the bound's place, and
    # such a call needs no loop around it to be unpriceable, so a sentence naming one would
    # name nothing. Round 8 refused the first and priced the second at 0 wherever its passes
    # could be read, which is every tool install of this tree (review round 9 of dispatch://INFRA-1545).
    unpriced = [item for item in blocking if item.kind == UNCOUNTED_UNREAD_BOUND]
    blocking = [item for item in blocking if item.kind != UNCOUNTED_UNREAD_BOUND]
    carries = (f"carries `timeout-minutes: {budget.minutes}`" if budget.minutes is not None
               else "carries no `timeout-minutes`")
    if blocking:
        named = "; ".join(
            f"`{item.what}` at {item.path}:{item.line}, inside the loop at line {item.loop}"
            for item in blocking
        )
        # The figure and the advice are written for the first of them, which is the step's own
        # uncounted site wherever it has one: uncounted_of's sites come before blocked_calls's
        # script calls. Which of the two an item is is read off its kind. `one` is a figure,
        # and it answers None for reasons of its own, so inferring the kind from it read a
        # site whose bound is a variable as a call of a script and sent it to the filter above
        # (review round 8 of dispatch://INFRA-1545). The two take different advice, because a declaration
        # names a site by the verb it retries and a call of a script writes a path where that
        # verb would be.
        #
        # A site carries what one call of it costs, read where the site was found. A script
        # call carries None there, because the scripts are read after the steps that run them,
        # and what one run of it costs is read here instead.
        #
        # That read of a script answers None in its turn when one run of the script reaches an
        # uncounted loop — its own, or one in a script it calls in turn, which costs the run
        # that reaches it the same way — and then there is no figure to print: a number written
        # there would be this check stating a cost it did not compute, which is the defect rule
        # 8 exists to refuse, one layer in and in its own voice. So the sentence says what is
        # true instead, that the run reaches an uncounted loop wherever it sits, and the list
        # above names the site that stopped the reading.
        #
        # A script can be unreadable the other way too: a call in it, or in one it calls,
        # bounds its attempts with a variable, or writes no bound for them at all, so one_call
        # puts no seconds behind that call and the sum leaves it out. What comes back is a
        # plain 0 where that call is the only one the script makes and an undercount where it
        # is not, and neither is a figure to print, so the sentence names the unread bound
        # instead, as a variable or as no bound, whichever unpriced_waits found. That is the
        # item this filter dropped before review round 6 of dispatch://INFRA-1545.
        #
        # A site is unreadable in the same two ways: `--max-time "$T"` bounds the attempt
        # where this check cannot read the bound, and a curl with no `--max-time` bounds it
        # nowhere. Then both halves of what the step waits for are unread — what one call
        # takes, and how many calls the loop makes — so no number is printed for it either.
        # Its advice is a site's all the same, because the verb is there for a declaration to
        # name (review round 8 of dispatch://INFRA-1545). The sentence says which of the two
        # it is: a site with no bound was told its bound was written as a variable, and to
        # write as a number a bound it does not write (dispatch://INFRA-1545, round 37).
        script_call = blocking[0].kind == UNCOUNTED_SCRIPT_CALL
        unread_bound = not script_call and blocking[0].one is None
        unbounded = unread_bound and blocking[0].unbounded
        variable_bound = unread_bound and not unbounded
        # A variable that is a `retry_fetch`'s seconds is refused at the site by rule 4, which no
        # declaration answers, so the advice asks for the number first and offers a declaration
        # only for the loop after it (dispatch://INFRA-1545, round 37).
        helper = variable_bound and blocking[0].helper
        one = (file_arithmetic(blocking[0].what, set()) if script_call
               else blocking[0].one)
        unread_kinds = unpriced_waits(blocking[0].what, set()) if script_call else set()
        if unread_bound:
            why = ("this check finds no bound on one attempt of it" if unbounded
                   else "its bound is written as a variable rather than as a number")
            cost_read = (f"What one call of `{blocking[0].what}` there takes is a figure this "
                         f"check cannot make, because {why}, and the step waits for that once "
                         "per pass of a loop whose passes this check cannot read either")
        elif unread_kinds:
            # Before the loop sentence below, because a script holding a call whose bound is
            # unread answers None for its whole arithmetic now, as a script reaching an
            # uncounted loop does, and the two read the same from here. The unread bound is
            # the one this sentence can name a cure for, so it is asked first; a script with
            # both faults is told about the bound and told about the loop the next time
            # (review round 9 of dispatch://INFRA-1545).
            made = " and ".join(
                {"variable": "attempts whose bound is written as a variable rather than as a "
                             "number",
                 "none": "attempts on which this check finds no bound",
                 "spelled": "attempts whose bound is written in a form this check refuses by "
                            "name where it is written, such as `--max-time 1.5`"}[kind]
                for kind in ("variable", "none", "spelled") if kind in unread_kinds)
            cost_read = (f"One run of `{blocking[0].what}` makes {made}, there or in a script "
                         "it calls, so what one run of it takes is a figure this check cannot "
                         "make either, and the step waits for that once per pass of the loop "
                         "above it")
        elif one is None:
            cost_read = (f"One run of `{blocking[0].what}` reaches a loop, its own or one in a "
                         "script it calls, whose passes this check cannot read either, so what "
                         "one run of it takes is a figure this check cannot make, and the step "
                         "waits for that once per pass of the loop above it")
        else:
            waiting = ("One run of that script takes" if script_call
                       else "One call there takes")
            cost_read = (f"{waiting} {one} s to exhaust its attempts and the step waits for "
                         "that once per pass")
        if script_call:
            instead = (
                "take the call out of the loop, or write the loop inside that script, where "
                "the call it retries is a site a declaration can name. No entry of "
                "DECLARED_LOOPS answers this one: an entry matches a site by its file, its "
                "step and the verb it retries, and a call of a script writes a path where "
                "that verb would be, so an entry pointed at it matches no retried call and "
                "fails this check in its own right.")
        elif variable_bound and not helper:
            # The bound first and the loop second, and in that order rather than as a choice:
            # a bound written as a number leaves the loop still unread, which is this rule's
            # ordinary refusal, while a pass count written over an unread bound leaves the
            # site with no per-call figure for those passes to multiply. A declaration answers
            # this site whole, as it answers the ordinary one: arithmetic_of asks
            # DECLARED_LOOPS for the seconds of a site one_call puts no figure on, before it
            # answers anything else about it, so an entry pointed here is counted and the
            # site is priced at that entry's figure (review round 9 of dispatch://INFRA-1545).
            instead = (
                "write that bound as a number, which is the figure this check multiplies by "
                "the passes it reads, or declare that site in DECLARED_LOOPS in "
                ".github/scripts/check-download-retry.sh. An entry there writes the seconds "
                "the site contributes to this budget, whole, because neither half of what the "
                "step waits for — what one call takes, or how many calls the loop makes — is "
                "a figure this check can make, and its `why` writes that sum out. It matches "
                "a site by its file, the verb it retries and the step where the site is "
                "written in a workflow, a site inside a script being read with no step at "
                "all.")
        else:
            instead = (
                "where the passes are as many as the run finds work, declare that site in "
                "DECLARED_LOOPS in .github/scripts/check-download-retry.sh: a "
                "DeclaredArithmetic entry there writes the seconds the site contributes to "
                "this budget and the sum they come from.")
        count_advice = ("" if variable_bound and not helper else
                        ("then write" if helper else "write") + " the count where this check "
                        "reads it, as literal words — a `for x in 1 2 3 4 5 6 7` — or ")
        # A site with no bound is told to write one first, as a number, which is the figure
        # the passes multiply, and then the ordinary advice about the loop: a bound alone
        # leaves the loop unread (dispatch://INFRA-1545, round 37). So is a site whose bound is
        # a `retry_fetch`'s seconds written as a variable: a declaration answers this refusal,
        # but the refusal of the site stands whatever is declared, and until round 37's class
        # search the advice offered that declaration in place of the number
        # (dispatch://INFRA-1545, round 37).
        bound_advice = ("Bound each attempt with a number of seconds — `--max-time "
                        "<seconds>` on a curl, or the seconds `retry_fetch` takes before its "
                        "`--` — which is the figure this check multiplies by the passes it "
                        "reads. " if unbounded else
                        "Write that bound as a number, the seconds `retry_fetch` takes before "
                        "its `--`, which is the figure this check multiplies by the passes it "
                        "reads: the refusal of the site asks for that number too, and no "
                        "declaration answers that refusal. " if helper else "")
        # The advice after the sentence above starts a sentence of its own whichever pieces it
        # is built from: with the count left out, it began with the lowercase word of the
        # clause that follows the count (dispatch://INFRA-1545, round 37).
        advice = count_advice + instead
        advice = bound_advice + advice[:1].upper() + advice[1:]
        problem(budget.path, budget.line,
                f"step '{step_name(budget)}' {carries} and this "
                "check can put no figure on it: it runs a retried call inside a loop whose "
                f"passes it cannot read — {named}. {cost_read}, so a budget written for one "
                "pass is a step the runner "
                f"cuts in the middle of the retry it exists to allow. {advice}")
        continue
    if unpriced:
        named = "; ".join(f"`{item.what}` at {item.path}:{item.line}" for item in unpriced)
        problem(budget.path, budget.line,
                f"step '{step_name(budget)}' {carries} and this check can put no figure on it: it "
                "runs a retried call whose attempts are bounded by a variable rather than by "
                f"a number — {named}. What one call there takes is therefore a figure this "
                "check cannot make, and a budget cannot be held above a wait nobody can "
                "price: a number written for it would be this check stating a cost it did "
                "not compute, which is the defect this rule exists to refuse. Two things "
                "answer it. Write that bound as a number, which is the figure this check "
                "multiplies by the passes it reads; or, where the seconds are the caller's "
                "to choose and no number belongs at the call, declare that site in "
                "DECLARED_LOOPS in .github/scripts/check-download-retry.sh: a "
                "DeclaredArithmetic entry there writes the seconds the site contributes to "
                "this budget and the sum they come from, and it matches a site by its file, "
                "the verb it retries and the step where the site is written in a workflow, a "
                "site inside a script being read with no step at all.")
    # No `continue` after the refusal above: an item whose bound is unread makes the step's own
    # arithmetic None — arithmetic_of answers None for a site one_call cannot price and no
    # declaration answers, and a script holding one answers None as a whole — so the test
    # below already ends this step, and a `continue` there was a statement no input could tell
    # from its absence (dispatch://INFRA-1545, round 32's mutation pass).
    if cost is None or not cost:
        continue
    if budget.minutes is None:
        problem(budget.path, budget.line,
                f"step '{step_name(budget)}' holds {cost} s of retries and carries no "
                "`timeout-minutes` of its own, so nothing holds that arithmetic above "
                "anything: this rule compares a step's attempts with the budget that step "
                "writes, and a step writing none is judged by nothing while the job's ceiling "
                "cuts it with GitHub's bare `The action '…' has timed out after N minutes.` "
                "That is how .github/actions/install-elan's 8 x 60 + 127 = 607 s ladder sat "
                "under ci.yaml's `airlock-lean` with no term for it and nothing said so. "
                "GitHub's granularity is the minute, so write "
                f"`timeout-minutes: {cost // 60 + 1}` or more on this step.")
        continue
    if 60 * budget.minutes <= cost:
        followed = sorted(target for target in seen if target in script_files)
        through = (f" That arithmetic is the step's own and that of {', '.join(followed)}, "
                   "which it runs: a step waits for every attempt the scripts it calls make."
                   if followed else "")
        problem(budget.path, budget.line,
                f"step '{step_name(budget)}' carries `timeout-minutes: {budget.minutes}`, "
                f"{60 * budget.minutes} s, and its own retries take {cost} s to exhaust, which "
                f"that budget does not strictly exceed.{through} A budget its own retries reach "
                "is a race between the last attempt's error and the runner's kill, and the "
                "retry can lose it: "
                "in run 42386541065's job 128445139635 a `gh api` read bounded at 30 s under "
                "`timeout-minutes: 5` — 5 x 30 + 150 = 300 s against 300 s — printed "
                "`attempt 5 of 5 reached its 30s bound` at 17:22:16.7444966Z and was killed at "
                "17:22:16.7483230Z, four milliseconds later. Lose that race and the step "
                "reports GitHub's bare `The action '…' has timed out after N minutes.` rather "
                "than naming the site and its attempts, which is the loud failure the retry "
                "exists to give. GitHub's granularity is the minute, so write "
                f"`timeout-minutes: {cost // 60 + 1}` or more.")
        continue
    allowed = JOB_DEFAULT_MINUTES if budget.job_minutes is None else budget.job_minutes
    if 60 * allowed <= cost:
        job_carries = (
            f"job '{budget.job}' carries `timeout-minutes: {budget.job_minutes}`, "
            f"{60 * budget.job_minutes} s" if budget.job_minutes is not None
            else f"job '{budget.job}' writes no `timeout-minutes`, so GitHub gives it "
                 f"{JOB_DEFAULT_MINUTES} minutes, {60 * JOB_DEFAULT_MINUTES} s")
        # The figure the advice names passes both of this rule's readings of a job: it is above
        # this arithmetic and above the step's own key, which the reading below holds the job's
        # key over. The step's key already exceeds this arithmetic wherever this is reached, so
        # the first minimum alone, which the advice named, was a job key the reading below
        # refused (dispatch://INFRA-1545, round 37).
        least = max(cost // 60 + 1, budget.minutes + 1)
        problem(budget.path, budget.job_line,
                f"{job_carries}, and its step '{step_name(budget)}' holds retries taking {cost} s to "
                "exhaust, which that budget does not strictly exceed. A job whose ceiling sits "
                "at or under one of its own steps' arithmetic cuts that step before the step's own "
                "budget can name the site that failed, so the step's `timeout-minutes` buys "
                "nothing: a job carries its measured duration plus the arithmetic of its "
                "slowest retried step, which is the rule ci-budgets.md's Budgets note writes out. "
                "The job's key must also strictly exceed that step's own `timeout-minutes: "
                f"{budget.minutes}`, which this rule refuses a job for too, so the least key "
                f"passing both is the larger of {cost // 60 + 1} and {budget.minutes + 1}. "
                f"Write `timeout-minutes: {least}` or more on this job.")
        continue
    if allowed <= budget.minutes:
        job_carries = (
            f"job '{budget.job}' carries `timeout-minutes: {budget.job_minutes}`"
            if budget.job_minutes is not None
            else f"job '{budget.job}' writes no `timeout-minutes`, so GitHub gives it "
                 f"{JOB_DEFAULT_MINUTES} minutes")
        problem(budget.path, budget.job_line,
                f"{job_carries}, and its step '{step_name(budget)}' carries "
                f"`timeout-minutes: {budget.minutes}`, which that job budget does not strictly "
                "exceed. The step's retries fit inside the step's own budget here, so this "
                "rule's step-budget refusal, which refuses a step whose `timeout-minutes` does "
                "not strictly exceed its own retries, says nothing; what cuts the step is the "
                "job, because the job holds that "
                "step AND everything else it runs. A job ceiling at or below one of its steps' "
                "ceilings therefore ends the run mid-ladder and reports GitHub's bare "
                "`The job running on runner … has exceeded the maximum execution time` instead "
                "of the step naming the site that failed, which is the same loss the step-budget "
                "refusal refuses by another road. The Budgets note derives a job budget as its "
                "measured duration plus its slowest retried step's whole ladder, and a program "
                "cannot read a measurement, so this is the half of that rule a check can hold: "
                f"write `timeout-minutes: {budget.minutes + 1}` or more on this job, or the "
                "figure that job's own comment derives, whichever is larger. "
                "(dispatch://INFRA-1545, round 27: twenty-two job keys stood below their own "
                "documented derivation while this check was green, because it read only a step's "
                "arithmetic against its job's key.)")

# Rule 8, the priced comment against its own key, which is the control dispatch://INFRA-1545's
# tester ran by `python3` at every round, moved into this check. EVERY ROUND OF THAT ISSUE
# MOVED A NUMBER AND LEFT A DERIVATION NAMING ITS OLD VALUE — keys, sentences, job sums, a
# proof and this check's own header — and the reading that caught each of them was a comparison
# of a stated minute figure with the key written under it. It found 142 sentences in one round
# and eight job sums in the next, it parses no prose, and without it the clause below stands
# aside in exactly the case that matters: a key lowered while its comment is left alone states
# an arrow that no longer matches, so no derivation is found and nothing is refused. Each of
# the nine keys this issue raised was put back that way by a reviewer, and this check stayed
# green in all nine. The two clauses together are mechanical — THE SENTENCE AGREES WITH THE
# KEY, AND THE KEY CARRIES THE SUM THE SENTENCE STATES.
PRICED = re.compile(r"->\s*(\d+)\s*min")
# A key line, bare or carrying a trailing YAML comment: `timeout-minutes: 28  # the sum above` is
# key 28, as YAML reads it, while `timeout-minutes: 28 junk` is no key line, since YAML reads that
# value as a string and written_minutes takes no string.
KEY_LINE = re.compile(r"^\s*timeout-minutes:\s*(\d+)(?:\s+#.*|\s*)$")


def indent_of(text: str) -> int:
    """How deep a line is indented: the blank before its first character."""
    return len(text) - len(text.lstrip())


# Who owns each key a step or a job writes, as (file, 1-based line), so a refusal of a key names
# the job or the step it belongs to. A Budget's `line` and `job_line` are the key's line only
# where that key is written: a step or a job writing none holds a fallback line there, and gets
# no entry.
KEY_OWNERS: dict[tuple[str, int], str] = {}
for budget in budgets:
    if budget.job_minutes is not None:
        KEY_OWNERS[(budget.path, budget.job_line)] = f"job '{budget.job}'"
    if budget.minutes is not None:
        KEY_OWNERS[(budget.path, budget.line)] = (f"step '{step_name(budget)}' of job "
                                                  f"'{budget.job}'")
# Every key line a priced block is paired with, as (file, 1-based line), and the block that
# prices it: the 1-based first and last line of that comment block, and its text joined as this
# loop joins it, in reading order. The pricing clause after the job-derivation clause refuses a
# retried job whose own key is not in it, so what "a job prices itself" means is exactly what
# this loop pairs. The job-derivation clause reads a job's derivation here and nowhere else, so
# the block it holds a key to is the block that prices that key (dispatch://INFRA-1545, round
# 35), and `--jobs` prints the block's two lines beside each job. A block's keys are the
# `timeout-minutes` lines below it, at most one per figure it states, read down to the first
# comment line or to the first job id, except a job id on the line directly below the block whose
# last line is indented no deeper than that id: that block is the comment the job's own id
# carries. There is no window and no distance, and an arrow never reaches past another comment
# or into a job its block does not head or sit inside (dispatch://INFRA-1545, review round 27,
# where `typecheck` lowered to 20 was priced by `tool-fallback-arm`'s step comment 28 lines above
# it).
PRICING_BLOCK: dict[tuple[str, int], tuple[int, int, str]] = {}
for path in sorted(set(budget.path for budget in budgets)):
    lines = Path(path).read_text().split("\n")
    job_ids = JOB_ID_LINES.get(path, set())
    number = 0
    while number < len(lines):
        # A line that is not a comment is stepped over: only a comment block prices a key, so
        # the walk moves to the next line and reads a block only where one starts.
        if not lines[number].lstrip().startswith("#"):
            number += 1
            continue
        # A comment block is read whole, because its figures are spread over its lines: one site
        # writes a step's own arithmetic on one line and the floor that overrides it on the next.
        first = number
        while number < len(lines) and lines[number].lstrip().startswith("#"):
            number += 1
        block = " ".join(lines[index].lstrip().lstrip("#").strip()
                         for index in range(first, number))
        figures = [int(figure) for figure in PRICED.findall(block)]
        # The keys this block prices are the ones written below it, at most one per figure the
        # block states: a block may derive two steps' budgets at once, as
        # `dispatch-browser-nightly.yaml` derives setup-bun's and the install's together, and
        # both keys are right. So each key below is held to BEING ONE OF the block's figures,
        # never to the last figure or the first: a `first key within N lines` pairing refuses
        # that correct site, and a gate that refuses a correct job is one the next author
        # silences. Three hand readers of this class each got that pairing wrong.
        keys: list[tuple[int, int]] = []
        for below in range(number, len(lines)):
            if lines[below].lstrip().startswith("#") or len(keys) == len(figures):
                break
            # Another job's id ends the block's reach, so a step comment at the end of one job
            # prices nothing in the next. The one id it passes is the job's own, written directly
            # below a block indented no deeper than it.
            if below + 1 in job_ids and not (
                    below == number and indent_of(lines[number - 1]) <= indent_of(lines[below])):
                break
            found = KEY_LINE.match(lines[below])
            if found and below + 1 not in TIMEOUT_KEYS.get(path, set()):
                # A line of text that reads as a key, a line of a `run:` body's heredoc, is no
                # key: it is refused by name and the walk reads on to the key the block prices.
                # Until round 37 it took the block's key, so the job's own went unpaired, and a
                # job retrying nothing passed under the sum its block states
                # (dispatch://INFRA-1545, round 37).
                problem(path, below + 1,
                        f"this line reads `{lines[below].strip()}` below the comment at line "
                        f"{first + 1}, and it is no `timeout-minutes` key of the YAML but a line "
                        "of text, such as a line of a `run:` body: the priced-comment clause "
                        "reads a key off a line, and would pair that comment with this text and "
                        "leave the key it prices unpaired. Put a comment line between them, or "
                        "write the text so that no line of it reads as a key "
                        "(dispatch://INFRA-1545, round 37).")
                continue
            if found:
                keys.append((int(found.group(1)), below + 1))
                # `first` is the block's first line counted from 0 and `number` the line after
                # its last, so the block runs from line first + 1 to line number counted from 1.
                PRICING_BLOCK[(path, below + 1)] = (first + 1, number, block)
        for key, key_line in keys:
            if key in figures:
                continue
            owner = KEY_OWNERS.get((path, key_line))
            problem(path, first + 1,
                    "this comment prices what it sits above at "
                    + " and ".join(f"{one} min" for one in figures)
                    + f", and the `timeout-minutes: {key}` below it"
                    + (f", the key of {owner}," if owner else "")
                    + f" carries {key}. A budget "
                    "comment that names a figure its key does not is how every drift of this "
                    "class has looked: the bound moved, the sentence kept the old arithmetic, "
                    "and the key was raised from a refusal while the sentence beside it was "
                    "left alone — 142 such sentences in one round of dispatch://INFRA-1545 and "
                    "eight job sums in the next, each green under every other rule here. "
                    "Rewrite the sentence from the figures this check prices, or correct the "
                    "key.")

# Rule 8, the job's own derivation, read against the sum the job's own comment states — the
# Budgets note's whole rule, a measured term plus the arithmetic of the single slowest retried
# step — where the comment ties that sum to this key as `<measured> + <step> = <total> s ->
# <key> min`. The second term is recomputed from THIS CHECK'S OWN STEP ARITHMETIC and never
# from a figure scraped from prose, because three hand readers of this class each mis-paired a
# figure with a block that did not own it, one reading a job as 268 minutes by swallowing a
# neighbouring comment's 14322 s.
#
# WHICH BLOCK IT READS: the one the priced-comment clause above paired with this job's key, as
# PRICING_BLOCK holds it, and no other. That is the block directly above the job's id or the one
# below the id and above the key, with no comment line between it and the key, so a derivation
# is read wherever the job prices itself; a key no block prices has no derivation here, and the
# job-that-prices-nothing clause below refuses it wherever the job's retries cost anything.
# Until round 35 this clause walked up from the key over comment lines alone, so a derivation
# written above the job's id was not read: `download-retry`, `download-retry-images`,
# `re-record`, `embedder` and `visual` state their sums there, and each could be lowered below
# its own sum, with every arrow of its block rewritten to the lowered key, and no refusal named
# that job's key. The harness's real-tree case drove that on this repository's own tree and named
# those five jobs (dispatch://INFRA-1545, round 35).
#
# WHAT IT DOES NOT READ, each checkable by mutating that site and watching this check stay
# green. These are measured, not believed (dispatch://INFRA-1545, round 29, where a reviewer
# drove every one of them):
#   - a sum that DOES NOT ADD UP (`1200 + 1281 = 1941 s`): it is passed over rather than
#     refused, since a clause that recomputed a total it cannot trust would be guessing;
#   - a sum with WORDS BETWEEN IT AND ITS ARROW (`2250 s, of which the key carries ... -> 30
#     min`): the pattern ties a sum to a key only where the arrow follows the sum's `s` with
#     nothing between them, so such a sum is no derivation here, and its key is read by the
#     priced-comment clause alone, which checks the arrow against the key and no sum;
#   - a job whose comment derives its key by a CONSTRUCTION rather than a sum: `tool-fallback`
#     carries a position rule, and .github/ci-budgets.md's "The tool-fallback job's budget,
#     part by part" states the construction its key comes from — `114 is the 104 this
#     construction gives with ten minutes added`. A clause that refused that job would be a
#     correct job refused, which is how a check gets silenced.
# Each of the three has a harness case that pins it, so a change that starts or stops reading one
# of them is one a reader sees in the harness rather than in the tree.
# A KEY LOWERED ON ITS OWN IS NOT IN THAT LIST ANY MORE: it leaves an arrow its key no longer
# matches, which the priced-comment clause above refuses by name. Before that clause existed,
# each of the nine keys this issue raised could be put back with its comment left alone and
# this check stayed green — the reviewer drove all nine, and three of them again afterwards to
# see them refused.
DERIVATION = re.compile(r"(\d+)\s*\+\s*(\d+)\s*=\s*(\d+)\s*s\s*->\s*(\d+)\s*min")


def job_key_floor(row: dict, least: int) -> tuple[int, str]:
    """The key a job-derivation refusal advises, and the sentence saying why where it is not
    `least`, the first whole minute above the sum that refusal compares: the larger of `least`
    and the first minute above the longest key a retried step of the job writes, which the
    step-budget loop's last refusal holds a job's key above. Until round 37's class search the
    advice named `least` alone, and a job written at it could sit at a step's key and be refused
    there: a comment deriving 0 + 450 = 450 s over a step keyed at 8 minutes was told 8
    (dispatch://INFRA-1545, round 37)."""
    if not row["key"]:
        return least, ""
    return (max(least, row["key"] + 1),
            f"The job's key must also strictly exceed the `timeout-minutes: {row['key']}` of its "
            f"step '{row['key_step']}', which this rule refuses a job for too, so the least key "
            f"passing both is the larger of {least} and {row['key'] + 1}. ")


for (path, job), row in sorted(JOB_SLOWEST.items()):
    # The block that prices this job's key, and nothing else: a figure outside it belongs to
    # another job or another step and is never read here. A key no block prices has no
    # derivation to read, and the job-that-prices-nothing clause below refuses it wherever the
    # job's retries cost anything.
    pricing = PRICING_BLOCK.get((path, row["line"]))
    if pricing is None:
        continue
    stated = None
    for match in DERIVATION.finditer(pricing[2]):
        if int(match.group(4)) == row["minutes"]:
            stated = match
    if stated is None:
        continue
    measured, term, total = (int(stated.group(1)), int(stated.group(2)), int(stated.group(3)))
    if measured + term != total:
        continue
    allowed = 60 * row["minutes"]
    if allowed <= total:
        least, why = job_key_floor(row, total // 60 + 1)
        problem(path, row["line"],
                f"job '{job}' carries `timeout-minutes: {row['minutes']}`, {allowed} s, and its "
                f"own comment derives {measured} + {term} = {total} s, which that key does not strictly exceed. A job "
                "is its measured duration plus the arithmetic of its slowest retried step "
                "(ci-budgets.md's Budgets note), so a key at or under the sum the comment itself "
                "states reports GitHub's bare timeout line part way through a step that is still "
                f"inside its own budget. {why}Write `timeout-minutes: {least}` or more, or "
                "correct the derivation.")
        continue
    recomputed = measured + row["cost"]
    # No `row["cost"] > term` beside this comparison: the key already strictly exceeds
    # measured + term here, so it can sit at or under measured + cost only when cost is the
    # larger, and a test of that would be a condition no input can tell from its absence.
    if allowed <= recomputed:
        least, why = job_key_floor(row, recomputed // 60 + 1)
        problem(path, row["line"],
                f"job '{job}' carries `timeout-minutes: {row['minutes']}`, {allowed} s, and its "
                f"own comment derives {measured} + {term} = {total} s, but the slowest retried "
                f"step of this job is '{row['step']}', whose retries take {row['cost']} s to "
                f"exhaust — more than the {term} s the comment adds. The sum is therefore "
                f"{measured} + {row['cost']} = {recomputed} s, which this key does not strictly exceed. THE "
                "COMMENT IS SELF-CONSISTENT AND STILL WRONG: it names a step that stopped being "
                "the slowest when a bound moved under it, which is how eight jobs of this "
                "repository came to stand under their own derivation while every other clause of "
                "this rule was green (dispatch://INFRA-1545). Re-pick the slowest step and rewrite "
                f"the derivation. {why}Write `timeout-minutes: {least}` or more.")

# Rule 8, the job that prices nothing. The priced-comment clause and the job-derivation clause
# above each read a figure a comment states; this one refuses a retried job that states none
# for its own key, which is what makes their reach total rather than a sentence in a header.
# THE HEADER OF THIS RULE CLAIMED A REACH IT DID NOT HAVE THREE ROUNDS RUNNING
# (dispatch://INFRA-1545): `download-retry` — the very job whose cancellation this issue exists
# to prevent — could be put back to its old key and every clause stayed silent, because its
# derivation names its minutes in words rather than as `-> N min`. A key nothing measures is a
# key that drifts, so a job holding a retried step must price itself. That holds a job that
# writes its own `timeout-minutes`, which is the only kind JOB_SLOWEST enters: a job that writes
# no key gets GitHub's 360 minutes, the job-ceiling refusals of the step-budget loop hold its
# steps under that, and there is no key for a comment to price.
#
# A job prices itself when the priced-comment clause pairs one of its blocks with that job's own
# key: a comment block holding `-> N min` whose keys, one per figure, include this key. A block's
# keys are the `timeout-minutes` lines below it, read down to the first comment line, or to the
# first job id other than one directly below a block indented no deeper than it. So the block
# sits inside this job — directly above its id, or below the id and above its key — with no
# comment line between it and the key, however many lines of other YAML lie between them.
# Nothing else is asked — not the shape of the sum, not the distance — and another job's arrow
# never counts, because the pairing stops at the next job's id (review round 27: a 40-line
# window took `tool-fallback-arm`'s step comment as `typecheck`'s price).
for (path, job), row in sorted(JOB_SLOWEST.items()):
    if row["cost"] <= 0:
        continue
    if (path, row["line"]) in PRICING_BLOCK:
        continue
    problem(path, row["line"],
            f"job '{job}' carries `timeout-minutes: {row['minutes']}` and holds a retried step "
            f"— '{row['step']}', whose retries take {row['cost']} s to exhaust — and no comment "
            "block prices that key: none writes a `-> N min` whose keys below it include this "
            "one. A key no comment prices is a key rule 8's priced-comment and job-derivation "
            "clauses in .github/scripts/check-download-retry.sh cannot read, so it drifts in "
            "silence: at the head that earned this rule, `download-retry` could be put back "
            "from 240 to the 221 it carried before this issue raised it, and every rule here "
            "stayed green, because its own derivation ends `takes the whole minute above it: "
            "240` rather than `-> 240 min` (dispatch://INFRA-1545). Write this job's own figure "
            "as `-> N min` in a comment block inside this job, either directly above the "
            "job's id at that id's own indentation or below the id and above this key, with no "
            "comment line and no other `timeout-minutes` between that block and this key, "
            "and the priced-comment clause pairs that block with this key.")

for entry in EXEMPTIONS:
    if entry.used:
        continue
    problem(SELF, 1,
            f"the exception for {entry.path}"
            + (f" job '{entry.job}'" if entry.job else "")
            + (f" step '{entry.step}'" if entry.step else "")
            + " matches no fetch in the tree. An exception outliving its site is an exception "
            "nobody reads again: drop it, or point it at where that site moved. It was written "
            f"for {entry.why}.")

for entry in DECLARED_LOOPS:
    if len(entry.loops) > 1:
        problem(SELF, 1,
                f"the declared loop for {entry.names()} covers the loops at lines "
                + ", ".join(str(line) for line in sorted(entry.loops))
                + ", and it writes one figure. A loop's passes are that loop's own, so an "
                "entry that two of them match hands the second a figure nobody wrote for it: "
                "one of those loops is counting something else. Write the count where this "
                "check reads it off the loop, or give the two loops steps or verbs that tell "
                f"them apart. This entry said {entry.figure()}, because {entry.why}.")
        continue
    if entry.below:
        line, floor = sorted(entry.below.items())[0]
        problem(SELF, 1,
                f"the declared arithmetic for {entry.names()} writes {entry.figure()}, and "
                f"one call of that site at {entry.path}:{line} takes {floor} s to exhaust its "
                "attempts. A site costs at least one call of itself, whatever the run finds "
                "for it to do, so a figure under that floor is a budget written for a step "
                f"the runner cuts inside the first ladder. Write {floor} s or more, or price "
                f"the attempts the site actually makes. It said {entry.figure()}, because "
                f"{entry.why}.")
    # An entry carrying `below` was matched, since arithmetic_of counts the entry as used before
    # it records a floor, so the test here already ends it after the refusal above.
    if entry.used:
        continue
    problem(SELF, 1,
            f"the declared arithmetic for {entry.names()} matches no retried call this check "
            "could not price of itself: one inside a loop whose passes it cannot read, or "
            "one whose per-attempt bound is written as a variable rather than as a number. A "
            "declaration outliving what it answered for is a figure nobody checks again: "
            "drop it, or point it at where that call moved. It said the budgets reaching it "
            f"are written for {entry.figure()}, because {entry.why}.")

# A DECLARED_CALLS or DECLARED_LIBRARIES entry naming a file this tree holds and matching no
# reading of it fails, as a stale DECLARED_LOOPS entry does, and one keyed in a form this check
# does not read is told so. One naming a file the tree does not hold covers nothing, so it can
# charge nothing short, and is left alone (dispatch://INFRA-1545, round 37).
read_files = set(workflows) | action_files | script_files
for entry in DECLARED_CALLS:
    if entry.used or entry.path not in read_files:
        continue
    problem(SELF, 1,
            f"the declared call figure for `{entry.word}` in {entry.path} "
            + ("matches no call through that variable in a script that can see a function that "
               "fetches. A declaration outliving what it answered for is a figure nobody checks "
               "again: drop it, or point it at where that call moved" if entry.path in script_files
               else "names a workflow or a composite action, and a declared call is read for a "
                    "script's own calls alone, the form this tree writes. Write the function's "
                    "name at the call, or move the call into a script under .github/scripts/ and "
                    "declare it there")
            + f". It said {entry.seconds} s, because {entry.why} (dispatch://INFRA-1545, "
            "round 37).")

for entry in DECLARED_LIBRARIES:
    if entry.used or entry.path not in read_files:
        continue
    problem(SELF, 1,
            f"the declared library figure for {entry.names()} "
            + ("matches no step that reaches that library along more than one path or through "
               "more than one call of its fetching functions, or along one path more than once. "
               "A declaration outliving what it answered for is a figure nobody checks again: "
               "drop it, or point it at where that reach moved"
               if (entry.step is not None) == (entry.path in workflows)
               else "is keyed in a form this check does not read: a workflow step by its file "
                    "and its name, or a script or a composite action by its path, with no step, "
                    "which are the forms this tree writes. Key it in one of them")
            + f". It said {entry.seconds} s, because {entry.why} (dispatch://INFRA-1545, "
            "round 37).")

if MODE in ("--list", "--jobs") and not problems:
    # A reader of these lists pipes them (`| head`, `| cut`), so a closed pipe ends this the way
    # any other tool ends: on the signal, with no traceback.
    signal.signal(signal.SIGPIPE, signal.SIG_DFL)
    if MODE == "--list":
        for site in sorted(sites, key=lambda site: (site.path, site.line)):
            print(f"{site.path}\t{site.line}\t{site.verb}\t{site.step}")
        print(f"{len(sites)} site(s)", file=sys.stderr)
        raise SystemExit(0)
    # The jobs the job-that-prices-nothing clause holds to a priced key, as that clause reads
    # them: the rows of JOB_SLOWEST whose slowest retried step costs more than 0 s, each with the
    # first and last line of the block PRICING_BLOCK pairs with its key, or `-` for both where no
    # block prices it.
    priced = sorted(((path, job, row) for (path, job), row in JOB_SLOWEST.items()
                     if row["cost"] > 0), key=lambda item: (item[0], item[2]["line"]))
    for path, job, row in priced:
        first, last, _block = PRICING_BLOCK.get((path, row["line"]), ("-", "-", ""))
        print(f"{path}\t{job}\t{row['line']}\t{row['minutes']}\t{row['cost']}\t{first}\t{last}"
              f"\t{row['step']}")
    print(f"{len(priced)} job(s)", file=sys.stderr)
    raise SystemExit(0)

if problems:
    for line in problems:
        print(line)
    print(f"{len(problems)} download(s) outside the one retry rule", file=sys.stderr)
    raise SystemExit(1)

print(
    f"every network fetch in CI retries at the floor: {len(sites)} site(s) over "
    f"{len(workflows)} workflow(s), {len(actions)} composite action(s), {len(scripts)} script(s) "
    f"and {len(DOCKERFILES)} Dockerfile(s), with {len(exempted)} site(s) the exceptions name"
)
PY
