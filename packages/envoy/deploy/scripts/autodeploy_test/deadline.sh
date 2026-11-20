# shellcheck shell=bash disable=SC2154 # fixture.sh, which autodeploy_test.sh sources first, sets these
# Case group of autodeploy_test.sh: the bound each call of a deploy and of its rollback is given,
# against the pass's deadline, the check that a deploy starts only where the pass leaves it all it
# needs, and a step the pass cut, which is not recorded as a failure of the image. It runs on the
# stand-ins of fixture.sh, which autodeploy_test.sh sources first. The pass's clock is the
# fixture's, which only the stand-ins move, so every bound below is an exact number, whatever the
# box's load.

# deploys_next_pass <what> [<setting>=<value>…]: the next pass, with the whole deadline and the
# registry answering at once, deploys new-image (with the settings given) and keeps no record: the
# record held only new-image's own stopped line, so its removal is worth no line either.
deploys_next_pass() {
  local what=$1
  shift
  : >"$manifests"
  : >"$log"
  if ! output=$(for setting in AUTODEPLOY_TEST_ORIGIN_OK=1 "$@"; do export "${setting?}"; done && deploy_once); then
    fail "the next pass after $what, with the whole deadline, did not deploy new-image"
  fi
  if [[ "$output" != *"deployed new-image"* ]] || [[ $(<"$compose/.env") != *"ENVOY_IMAGE_TAG=new-image"* ]] ||
    [[ -e "$failed_record" ]] || [[ "$output" == *"removed $failed_record"* ]]; then
    fail "the next pass after $what did not deploy new-image, kept a record, or logged the removal of a record that held only new-image's own stopped line"
  fi
}

# Each call of a deploy is given what the pass's deadline leaves less the 180 s the pass keeps for
# a rollback, and the pull, the up and the origin check their own bound where that is less: the
# pull's 900 s, after which nothing has changed, the up's 300 s, the deploy's limit for the new
# image's start, and the origin check's 3 s, one request as a health request is. Each health check
# request is given its 3 s. The rollback's stop, its confirmation, its schema read and its own up
# have no bound of their own: each gets what the pass leaves, all of it. Here the pass starts at 0
# on its clock and the deploy's one health check try moves it 3 s on.
case_begin 'the bounds of a deploy and of its rollback'
fresh_env
if output=$(deploy_once); then
  fail 'deployer did not fail after the origin check rejected the deployment'
fi
is_exactly "the pull's bound" 900 "$(bounds_of 'compose pull')"
is_exactly "the bounds of the deploy's up and of the rollback's up" '300 3597' "$(bounds_of 'compose up' | paste -sd' ')"
is_exactly "the health check requests' bounds, the deploy's and the rollback's" '3 3' "$(bounds_of 'curl /healthz' | paste -sd' ')"
is_exactly "the origin check's bound" 3 "$(bounds_of 'curl /auth/start')"
is_exactly "the rollback's stop's bound" 3597 "$(bounds_of 'compose stop')"
is_exactly "the rollback's confirmation's bound" 3597 "$(bounds_of 'compose ps')"

# A deploy starts only where the pass leaves PASS_DEADLINE_MINIMUM, 1,563 s: each step of the deploy
# its own bound (the pull's 900 s, the up's 300, the health check's 180 and the origin check's 3)
# and the rollback its 180. One started with less could have a step cut below its own bound, and a
# good image recorded as failed. Here the newest commit's manifest read answers 3,200 s into the
# pass, and new-image's up would take 250 s, inside its own 300: nothing is backed up, pulled or
# started, nothing is recorded, old-image still serves, and the next pass, with the whole hour,
# deploys new-image, its up given its 300 s.
case_begin 'a deploy the pass has too little time for waits for the next pass'
fresh_env
printf 'new-image clock:3200\n' >"$manifests"
status=0
output=$(AUTODEPLOY_TEST_UP_TAKES=250 AUTODEPLOY_TEST_ORIGIN_OK=1 deploy_once) || status=$?
if ((status != 1)) ||
  [[ "$output" != *"the pass leaves 400s, less than the 1563s a deploy needs (PASS_DEADLINE_MINIMUM: its steps' own bounds and the 180s of its rollback), before the deploy of new-image could start; old-image still serves, and the next pass starts with the whole deadline"* ]] ||
  [[ -s "$log" ]] || grep -q '^pg_dump ' "$argv" || [[ -e "$failed_record" ]]; then
  fail "deployer started a deploy the pass had too little time for, or recorded it, or did not name why it did not start it (status $status)"
fi
deploys_next_pass 'a deploy the pass had too little time for' AUTODEPLOY_TEST_UP_TAKES=250
is_exactly "the next pass's up's bound" 300 "$(bounds_of 'compose up' | head -n1)"

# The start check's boundary: a pass that leaves a deploy exactly the 1,563 s it needs deploys, and
# one that leaves it 1,562 s does not.
for at in 2037 2038; do
  case_begin "a deploy with $((3600 - at)) s of the pass left"
  fresh_env
  printf 'new-image clock:%s\n' "$at" >"$manifests"
  status=0
  output=$(AUTODEPLOY_TEST_ORIGIN_OK=1 deploy_once) || status=$?
  if ((at == 2037)) && { ((status != 0)) || [[ "$output" != *"deployed new-image"* ]]; }; then
    fail "deployer did not deploy with the 1563 s a deploy needs left (status $status)"
  fi
  if ((at == 2038)) && { ((status != 1)) || [[ "$output" != *"the pass leaves 1562s, less than the 1563s a deploy needs"* ]] || [[ -s "$log" ]]; }; then
    fail "deployer started a deploy with 1562 s left, one less than it needs, or did not name why not (status $status)"
  fi
done

# The backup has no bound of its own but what the pass leaves, so the pass is read again before the
# pull. A backup that takes 2,038 s of the pass leaves 1,562 s: nothing is pulled or started,
# nothing is recorded, old-image still serves, and the next pass deploys new-image. The control, a
# backup of 2,037 s, leaves 1,563 s, and the same pass deploys.
for takes in 2038 2037; do
  case_begin "a backup that leaves the pull $((3600 - takes)) s of the pass"
  fresh_env
  status=0
  output=$(AUTODEPLOY_TEST_BACKUP_TAKES=$takes AUTODEPLOY_TEST_ORIGIN_OK=1 deploy_once) || status=$?
  if ! grep -q '^pg_dump ' "$argv"; then
    fail 'the deploy took no backup, so this case proves nothing'
  fi
  if ((takes == 2037)); then
    if ((status != 0)) || [[ "$output" != *"deployed new-image"* ]]; then
      fail "the control: deployer did not deploy after a backup that left the pull the 1563 s a deploy needs (status $status)"
    fi
    continue
  fi
  if ((status != 1)) ||
    [[ "$output" != *"the pass leaves 1562s, less than the 1563s a deploy needs (PASS_DEADLINE_MINIMUM: its steps' own bounds and the 180s of its rollback), before the pull of new-image could start; old-image still serves, and the next pass starts with the whole deadline"* ]] ||
    [[ -s "$log" ]] || [[ -e "$failed_record" ]]; then
    fail "deployer pulled or started new-image after a backup that left the deploy too little of the pass, or recorded it, or did not name why not (status $status)"
  fi
  deploys_next_pass 'a backup that left the pull too little of the pass'
done

# A pull that runs out its own 900 s is named with that bound, and nothing changed: the previous
# image still serves, nothing is recorded as a failed deploy, and the next pass pulls again. (The
# timeout stand-in's cap stops the hung pull two seconds in and moves the pass's clock on to where
# its bound ends.)
case_begin 'a pull that runs out its own bound'
fresh_env
if output=$(AUTODEPLOY_TEST_PULL_HANG=1 AUTODEPLOY_TEST_TIMEOUT_CAP=2 deploy_once); then
  fail 'deployer passed a pass whose pull never answered'
fi
if [[ "$output" != *"could not pull new-image (it ran out its own 900s); old-image still serves"* ]] ||
  [[ $(grep -c ' up ' "$log") -ne 0 ]] || [[ -e "$failed_record" ]]; then
  fail 'deployer did not name a pull that ran out its own bound, or went on past it'
fi

# An up that runs out its own 300 s is named with that bound, rolled back, and recorded as a failure
# of the image: the control of the cut up below.
case_begin 'an up that runs out its own bound'
fresh_env
if output=$(AUTODEPLOY_TEST_UP_TAKES=400 deploy_once); then
  fail 'deployer passed a pass whose up ran out its bound'
fi
if [[ "$output" != *"compose up failed for new-image (it ran out its own 300s)"*"rolling back to old-image"* ]] ||
  [[ "$output" == *"the pass's deadline cut"* ]] || [[ $(cut -d' ' -f1,2 "$failed_record") != 'new-image rolled-back' ]]; then
  fail 'deployer did not name an up that ran out its own bound, roll it back, and record it as rolled-back'
fi

# A step after the image started is cut by the pass, not failed by the image, where the pass left it
# less than its own bound and it ran that out. After the start check only a clock that moves on past
# the steps' bounds can do that: here it moves on 3,300 s as the pull returns. new-image's up, which
# takes 250 s, is then given the 120 s the pass leaves less the rollback's 180, and stopped. It is
# rolled back, as a failed up is, but its stopped line is taken out rather than rewritten as
# rolled-back, and the next pass deploys it.
case_begin "an up the pass's deadline cut"
fresh_env
status=0
output=$(AUTODEPLOY_TEST_CLOCK_STEP=pull:3300 AUTODEPLOY_TEST_UP_TAKES=250 AUTODEPLOY_TEST_ORIGIN_OK=1 deploy_once) || status=$?
if ((status != 1)) ||
  [[ "$output" != *"compose up failed for new-image (it had not answered when the pass had only the 180s it keeps for a rollback left)"*"rolling back to old-image"*"the pass's deadline cut the deploy of new-image, not a failure of the image; old-image serves again, and the next pass deploys new-image with the whole deadline"* ]] ||
  [[ -e "$failed_record" ]] || [[ $(<"$compose/.env") != *"ENVOY_IMAGE_TAG=old-image"* ]]; then
  fail "deployer recorded an up the pass's deadline cut as a failure of the image, or did not roll it back (status $status)"
fi
is_exactly "the cut up's bound" 120 "$(bounds_of 'compose up' | head -n1)"
deploys_next_pass "an up the pass's deadline cut" AUTODEPLOY_TEST_UP_TAKES=250

# A stop that never answers fails, named as having run out the pass's deadline, and the rollback is
# refused as for a failed stop: the line says new-image may still run and that whether it migrated
# is not known. So does a schema read whose container never answers. Each leaves the record saying
# the deployer stopped. (The timeout stand-in's cap ends each two seconds in and moves the pass's
# clock on to where its bound ends.)
case_begin 'a rollback stop that never answers'
fresh_env
status=0
output=$(AUTODEPLOY_TEST_STOP_HANG=1 AUTODEPLOY_TEST_TIMEOUT_CAP=2 deploy_once) || status=$?
if ((status != 3)) ||
  [[ "$output" != *"could not stop new-image (it had not answered when the pass's deadline, PASS_DEADLINE=3600s, passed), so refusing to roll back to old-image, which runs against schema 112 (112 applied); new-image may still run, and whether it changed the applied migrations is not known, since the schema was not read: forward recovery is required"* ]] ||
  [[ $(grep -c ' up ' "$log") -ne 1 ]] || [[ $(cut -d' ' -f1,2 "$failed_record") != 'new-image stopped' ]]; then
  fail "deployer did not name a rollback stop that ran out the pass's deadline and stop (status $status), or rolled back without it"
fi
case_begin 'a schema read after the stop whose container never answers'
fresh_env
status=0
output=$(AUTODEPLOY_TEST_RUN_HANG_AFTER_UP=1 AUTODEPLOY_TEST_TIMEOUT_CAP=2 deploy_once) || status=$?
if ((status != 3)) ||
  [[ "$output" != *"could not read the schema version after stopping new-image (status 124 (it had not answered when the pass's deadline, PASS_DEADLINE=3600s, passed)), so refusing to roll back to old-image"*"; new-image is stopped, and whether it changed the applied migrations is not known, since the schema was not read: forward recovery is required"* ]] ||
  [[ $(grep -c ' up ' "$log") -ne 1 ]]; then
  fail "deployer did not name a schema read that ran out the pass's deadline, or rolled back without it"
fi

# The read after the stop waits on the migration key for as long as the pass's deadline leaves, not
# a bound of its own: a migration the stopped image sent still holds the key, and still works. Here
# it holds it for 75 tries a second apart after the stop, past the read before a deploy's 60 s; the
# read takes the key on the 76th and rolls back. Each refused try read the key's holder.
case_begin 'the migration key held for 75 s after the stop'
fresh_env
if output=$(AUTODEPLOY_TEST_KEY_HELD_AFTER_STOP=75 deploy_once); then
  fail 'deployer did not fail after the origin check rejected the deployment'
fi
if [[ "$output" != *"rolling back to old-image"* ]] || [[ "$output" == *"refusing to roll back"* ]] ||
  [[ $(cut -d' ' -f1,2 "$failed_record") != 'new-image rolled-back' ]]; then
  fail 'deployer did not wait for a migration key held 75 s after the stop, and roll back'
fi
is_exactly 'the holder reads of the refused tries after the stop' 75 "$(grep -cF "$holder_read" "$argv")"
# The read's tries after the stop are given what the pass leaves, from 3597 s at its first try down
# to 3522 at the one that took the key; the rollback's up gets what is left after them.
is_exactly 'the first and the last bound of the tries after the stop' '3597 3522' \
  "$(grep -o '^container-timeout [0-9]*$' "$argv" | awk 'NR == 2 {first = $2} {last = $2} END {print first, last}')"
is_exactly "the rollback's up's bound" 3522 "$(bounds_of 'compose up' | tail -n1)"

# The control: a key held past the pass's deadline is not waited out. The read refuses at that
# deadline, naming the key and the session the server named as its holder, and says new-image is
# stopped and whether it migrated is not known; the deployer stops. (Each refused try here takes
# 500 s of the pass's clock, as a statement waiting out a long lock would, so the pass's hour is
# reached in eight tries.)
case_begin 'the migration key held past the pass deadline after the stop'
fresh_env
status=0
output=$(AUTODEPLOY_TEST_KEY_HELD_AFTER_STOP=always AUTODEPLOY_TEST_KEY_TRY_TAKES=500 deploy_once) || status=$?
if ((status != 3)) ||
  [[ "$output" != *"could not read the schema version after stopping new-image (the migration key, 8150001, was still held by pid 4242 (dispatch-migrate, active, in a transaction for 00:01:15) when the pass's deadline (PASS_DEADLINE=3600s) passed), so refusing to roll back to old-image, which runs against schema 112 (112 applied); new-image is stopped, and whether it changed the applied migrations is not known, since the schema was not read: forward recovery is required"* ]] ||
  [[ $(grep -c ' up ' "$log") -ne 1 ]]; then
  fail "deployer did not refuse at the pass's deadline, naming the held key and its holder (status $status)"
fi
is_exactly 'the tries after the stop' 8 "$(($(grep -cF "$schema_try" "$argv") - 1))"

# A health check never takes the rollback's time: each try is cut to what the deploy's part of the
# pass leaves, and the check ends at the try that has none, so the rollback still has its 180 s.
# After the start check that takes a clock that moved on: here 3,300 s as the pull returns, so the
# check starts with 120 s left before the rollback's 180. Each health request of new-image runs out
# its own 3 s: twenty tries of 6 s fit, the 21st does not start, and the rollback runs in the 180 s.
# The pass cut that check, which new-image might have passed at a later try, so new-image is not
# recorded as failed. The next pass, with the whole hour, gives it all 30 tries; each runs out its
# own 3 s, and new-image is then recorded as rolled-back: a check that runs out its 30 tries is the
# image's failure.
case_begin "a health check the pass's deadline cut, and the same check with all its tries"
fresh_env
status=0
output=$(AUTODEPLOY_TEST_CLOCK_STEP=pull:3300 AUTODEPLOY_TEST_HEALTH_TAKES=5 deploy_once) || status=$?
if ((status != 1)) ||
  [[ "$output" != *"the pass leaves 180s, no more than the 180s it keeps for a rollback, before try 21 of the health check of new-image could start"*"rolling back to old-image"*"the pass's deadline cut the deploy of new-image, not a failure of the image"* ]] ||
  [[ "$output" == *"did not answer 200 with NATS connected"* ]] || [[ -e "$failed_record" ]]; then
  fail "deployer did not end the health check at the rollback's time and roll back, or recorded a check the pass cut as the image's failure (status $status)"
fi
is_exactly "the deploy's health check tries" 20 "$(awk '$3 == "curl" && $4 == "/healthz" && $2 < 3420' "$bounds_log" | wc -l)"
is_exactly "the rollback's stop, at the start of the 180 s kept for it" '180@3420' \
  "$(awk '$3 == "compose" && $4 == "stop" {print $1 "@" $2}' "$bounds_log")"
: >"$log"
: >"$bounds_log"
status=0
output=$(AUTODEPLOY_TEST_HEALTH_TAKES=5 deploy_once) || status=$?
if ((status != 1)) || [[ "$output" != *"deployed new-image but /healthz did not answer 200 with NATS connected in its 30 tries, 3 s apart"*"rolling back to old-image"* ]] ||
  [[ "$output" == *"the pass's deadline cut"* ]] || [[ $(cut -d' ' -f1,2 "$failed_record") != 'new-image rolled-back' ]]; then
  fail "the control: deployer did not record as rolled-back an image whose health check ran out its 30 tries (status $status)"
fi
is_exactly "the tries of the check with the whole hour" 30 \
  "$(awk '$3 == "compose" && $4 == "stop" {exit} $3 == "curl" && $4 == "/healthz" {n++} END {print n + 0}' "$bounds_log")"

# A good image whose health check the pass cut is not recorded as failed either, and the next pass
# deploys it. Here the clock moves on 3,416 s as the pull returns, so the first try is given 4 s:
# its 3 s wait, and 1 s for a request new-image answers in 2. That request runs out what the pass
# left it, less than its own 3 s: the check ends there, and new-image is rolled back, unrecorded.
# The next pass gives the request its 3 s, and new-image deploys.
case_begin "a good image whose health check the pass's deadline cut"
fresh_env
status=0
output=$(AUTODEPLOY_TEST_CLOCK_STEP=pull:3416 AUTODEPLOY_TEST_HEALTH_TAKES=2 AUTODEPLOY_TEST_ORIGIN_OK=1 deploy_once) || status=$?
if ((status != 1)) ||
  [[ "$output" != *"try 1 of the health check of new-image had not answered in the 1s the pass left it, less than its own 3s"*"rolling back to old-image"*"the pass's deadline cut the deploy of new-image, not a failure of the image; old-image serves again, and the next pass deploys new-image with the whole deadline"* ]] ||
  [[ -e "$failed_record" ]]; then
  fail "deployer recorded a good image whose health check the pass cut as failed, or did not name the cut try (status $status)"
fi
deploys_next_pass "a health check the pass's deadline cut" AUTODEPLOY_TEST_HEALTH_TAKES=2

# A good image whose origin check the pass cut is not recorded as failed, and the next pass deploys
# it. The clock moves on at new-image's first good /healthz answer, 3 s into the pass: by 3,415 s,
# so the origin check is given 2 s, less than its own 3, and new-image's /auth/start, which answers
# in 2, runs them out; or by 3,417 s, so the pass leaves the check no time at all. Either way
# new-image is rolled back, unrecorded, and the next pass gives the check its 3 s.
for step in 3415 3417; do
  case_begin "a good image whose origin check the pass's deadline cut (the clock 3 s and then $step s on)"
  fresh_env
  status=0
  output=$(AUTODEPLOY_TEST_CLOCK_STEP=healthy:$step AUTODEPLOY_TEST_ORIGIN_TAKES=2 AUTODEPLOY_TEST_ORIGIN_OK=1 deploy_once) || status=$?
  cut="the origin check of new-image had not answered in the 2s the pass left it, less than its own 3s"
  ((step == 3415)) || cut="the pass leaves 180s, no more than the 180s it keeps for a rollback, before the origin check of new-image could start"
  if ((status != 1)) ||
    [[ "$output" != *"$cut"*"rolling back to old-image"*"the pass's deadline cut the deploy of new-image, not a failure of the image; old-image serves again, and the next pass deploys new-image with the whole deadline"* ]] ||
    [[ "$output" == *"OAuth redirect_uri"* ]] || [[ -e "$failed_record" ]]; then
    fail "deployer recorded a good image whose origin check the pass cut as failed, or did not name the cut (status $status)"
  fi
  deploys_next_pass "an origin check the pass's deadline cut" AUTODEPLOY_TEST_ORIGIN_TAKES=2
done

# The controls: an origin check that fails inside its own bound is the image's failure, and is
# recorded as rolled-back. One answers the wrong redirect with exactly its own 3 s left (the clock
# moves on 3,414 s at the first good /healthz answer); the other runs out its own 3 s.
for control in wrong-redirect runs-out-its-bound; do
  case_begin "an origin check that fails inside its own bound ($control)"
  fresh_env
  status=0
  if [[ "$control" == wrong-redirect ]]; then
    redirect='http://wrong.test:8766/auth/callback'
    output=$(AUTODEPLOY_TEST_CLOCK_STEP=healthy:3414 deploy_once) || status=$?
  else
    redirect='(no answer in its own 3s: curl status 28)'
    output=$(AUTODEPLOY_TEST_ORIGIN_TAKES=3 AUTODEPLOY_TEST_ORIGIN_OK=1 deploy_once) || status=$?
  fi
  if ((status != 1)) ||
    [[ "$output" != *"deployed new-image but the OAuth redirect_uri is '$redirect', expected 'http://browser.test:8766/auth/callback' - login would break"*"rolling back to old-image"* ]] ||
    [[ "$output" == *"the pass's deadline cut"* ]] || [[ $(cut -d' ' -f1,2 "$failed_record") != 'new-image rolled-back' ]]; then
    fail "deployer did not record as rolled-back an image whose origin check failed inside its own bound (status $status)"
  fi
  is_exactly "the origin check's bound ($control)" 3 "$(bounds_of 'curl /auth/start')"
done
