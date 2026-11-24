#!/usr/bin/env bash
# .github/scripts/test-fence.test.sh
#
# Tests .github/scripts/test-fence.sh with no root, no cgroup made and no rule applied: fakes of
# sudo, iptables, ip6tables, docker and dmesg on PATH record what they were asked, sudo runs what
# it was given as this user, and a directory of this test stands for /sys/fs/cgroup. It checks,
# one CHECK line each: that fence refuses a machine with no cgroup v2 hierarchy before any rule;
# that it makes the cgroup; that each family's chain passes established connections, loopback
# and the bridge of each named network (and of each network of a named container), then logs
# with the prefix and rejects; that each family's OUTPUT sends the cgroup's packets, matched by
# its path, through that chain; that no rule names an address; that a network that is no bridge
# is refused; that a step run through the installed shell moves its own process into the cgroup,
# fails when the cgroup does not then hold it, runs as this user with the Go settings and the
# step's environment, and exits with the step's status, and is refused before any fence; that
# report prints both families' rules, match rows and counters, passes on a match in both families
# with no refusal and no pull, and fails on a missing match, a refused packet and a pull; and that
# a usage error exits 2.
#
# Run from anywhere: .github/scripts/test-fence.test.sh
set -euo pipefail

script="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/test-fence.sh"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
bin=$work/bin
out=$work/out
mkdir -p "$bin" "$out"
calls=$work/calls

failed=0
check() { # <name> <0 for a pass> <what failed>
  if [ "$2" = 0 ]; then echo "CHECK $1: PASS"; else echo "CHECK $1: FAIL: $3"; failed=1; fi
}
has() { grep -qxF -- "$2" <<<"$1" && echo 0 || echo 1; }

# Each family records what it was asked and prints, for a listing (-S or -L of OUTPUT or the
# chain), the file of $out this test wrote for it, if any.
for family in iptables ip6tables; do
  cat >"$bin/$family" <<EOF
#!/usr/bin/env bash
echo "$family \$*" >>"$calls"
case "\$1" in -S | -L) [ ! -f "$out/$family\$1\$2" ] || cat "$out/$family\$1\$2" ;; esac
EOF
done
# sudo records its options and the command, then runs the command as this user; with
# FAKE_SUDO_LOSES_THE_MOVE set, a tee into a cgroup.procs writes another pid, as a move the kernel
# did not make would leave the cgroup.
cat >"$bin/sudo" <<EOF
#!/usr/bin/env bash
options=()
while [ \$# -gt 0 ]; do
  case "\$1" in
    -n | -H | -E | --preserve-env) options+=("\$1"); shift ;;
    -u) options+=("-u \$2"); shift 2 ;;
    --) shift; break ;;
    *) break ;;
  esac
done
echo "sudo \${options[*]} :: \$*" >>"$calls"
if [ -n "\${FAKE_SUDO_LOSES_THE_MOVE:-}" ] && [ "\$1" = tee ] && [[ \$2 == */cgroup.procs ]]; then
  cat >/dev/null
  echo 1 >"\$2"
  exit 0
fi
exec "\$@"
EOF
cat >"$bin/docker" <<EOF
#!/usr/bin/env bash
case "\$*" in
  "network inspect kind --format "*) echo "bridge 0123456789abcdef0123456789abcdef " ;;
  "network inspect github_network_1 --format "*) echo "bridge fedcba9876543210fedcba9876543210 <no value>" ;;
  "network inspect bridge --format "*) echo "bridge 1111111111111111 docker0" ;;
  "network inspect host --format "*) echo "host 2222222222222222 " ;;
  "inspect postgres-1 --format "*) printf 'github_network_1\nbridge\n' ;;
  events*) cat "$work/events" ;;
  *) echo "docker: unexpected \$*" >&2; exit 1 ;;
esac
EOF
printf '#!/usr/bin/env bash\ncat "%s"\n' "$work/dmesg" >"$bin/dmesg"
chmod +x "$bin"/*
: >"$work/events"
: >"$work/dmesg"

export PATH="$bin:$PATH" LEGION_TEST_SHELL="$work/legion-test-shell" LEGION_TEST_CGROUP_ROOT="$work/no-cgroup2"
export RUNNER_TEMP=$work/runner
mkdir -p "$RUNNER_TEMP" "$LEGION_TEST_CGROUP_ROOT"

# --- fence -------------------------------------------------------------------------------------
status=0
said=$(bash "$script" fence --network kind 2>&1) || status=$?
check fence-needs-cgroup-v2 "$([ "$status" = 1 ] && [[ $said == *"holds no cgroup v2 hierarchy"* ]] && [ ! -s "$calls" ] && echo 0 || echo 1)" \
  "it exited $status and asked $(cat "$calls" 2>/dev/null): $said"

export LEGION_TEST_CGROUP_ROOT=$work/cgroup
mkdir -p "$LEGION_TEST_CGROUP_ROOT"
echo "cpuset cpu io memory pids" >"$LEGION_TEST_CGROUP_ROOT/cgroup.controllers"
said=$(bash "$script" fence --network kind --container postgres-1 2>&1) || { echo "$said"; check fence-runs 1 "fence exited non-zero"; }
check fence-makes-the-cgroup "$([ -d "$LEGION_TEST_CGROUP_ROOT/legion-test" ] && [ "$(has "$(cat "$calls")" "sudo  :: mkdir -p $LEGION_TEST_CGROUP_ROOT/legion-test")" = 0 ] && echo 0 || echo 1)" \
  "no sudo mkdir of $LEGION_TEST_CGROUP_ROOT/legion-test: $(cat "$calls")"
chain_of() { grep "^$1 -[NA] " "$calls" | sed "s/^$1 //"; }
want=$(printf '%s\n' "-N LEGION-TEST" \
  "-A LEGION-TEST -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN" \
  "-A LEGION-TEST -o lo -j RETURN" \
  "-A LEGION-TEST -o br-0123456789ab -j RETURN" \
  "-A LEGION-TEST -o br-fedcba987654 -j RETURN" \
  "-A LEGION-TEST -o docker0 -j RETURN" \
  "-A LEGION-TEST -j LOG --log-prefix legion-test refused: " \
  "-A LEGION-TEST -j REJECT")
check fence-ipv4-chain "$([ "$(chain_of iptables)" = "$want" ] && echo 0 || echo 1)" "iptables was asked: $(chain_of iptables)"
check fence-ipv6-chain "$([ "$(chain_of ip6tables)" = "$want" ] && echo 0 || echo 1)" "ip6tables was asked: $(chain_of ip6tables)"
inserted=$(grep -E '^ip6?tables -I ' "$calls" || true)
check fence-matches-the-cgroup "$([ "$inserted" = "$(printf '%s\n' 'iptables -I OUTPUT 1 -m cgroup --path legion-test -j LEGION-TEST' \
  'ip6tables -I OUTPUT 1 -m cgroup --path legion-test -j LEGION-TEST')" ] && echo 0 || echo 1)" "OUTPUT was given: $inserted"
named=$(grep -E '^ip6?tables ' "$calls" | grep -E -- ' -[ds] ' || true)
check fence-names-no-address "$([ -z "$named" ] && echo 0 || echo 1)" "a rule names an address: $named"
status=0
said=$(bash "$script" fence --network host 2>&1) || status=$?
check fence-refuses-a-network-that-is-no-bridge "$([ "$status" = 1 ] && [[ $said == *"the docker network host is a host network"* ]] && echo 0 || echo 1)" \
  "it exited $status: $said"

# --- run ---------------------------------------------------------------------------------------
: >"$calls"
printf '%s\n' 'echo "pid=$$ user=$(id -un) GOPROXY=$GOPROXY GOTOOLCHAIN=$GOTOOLCHAIN STEP_VALUE=$STEP_VALUE"' 'exit 3' >"$work/step.sh"
status=0
ran=$(STEP_VALUE=kept "$LEGION_TEST_SHELL" "$work/step.sh" 2>&1) || status=$?
pid=$(sed -n 's/^pid=\([0-9]*\) .*/\1/p' <<<"$ran")
check run-moves-into-the-cgroup "$([ -n "$pid" ] && [ "$(cat "$LEGION_TEST_CGROUP_ROOT/legion-test/cgroup.procs")" = "$pid" ] &&
  [ "$(has "$(cat "$calls")" "sudo -n :: tee $LEGION_TEST_CGROUP_ROOT/legion-test/cgroup.procs")" = 0 ] && echo 0 || echo 1)" \
  "the step ran as pid ${pid:-none}, the cgroup holds $(cat "$LEGION_TEST_CGROUP_ROOT/legion-test/cgroup.procs" 2>/dev/null), and sudo was asked $(cat "$calls")"
check run-as-the-runner "$([[ $ran == *" user=$(id -un) "* ]] && ! grep -q -- '-u ' "$calls" && echo 0 || echo 1)" \
  "the step printed $ran, and sudo was asked $(cat "$calls")"
check run-go-settings-and-the-step-environment "$([[ $ran == *" GOPROXY=off GOTOOLCHAIN=local STEP_VALUE=kept" ]] && echo 0 || echo 1)" "the step printed $ran"
check run-exit-status "$([ "$status" = 3 ] && echo 0 || echo 1)" "the step exited 3 and the shell $status"
status=0
said=$(FAKE_SUDO_LOSES_THE_MOVE=1 "$LEGION_TEST_SHELL" "$work/step.sh" 2>&1) || status=$?
check run-fails-outside-the-cgroup "$([ "$status" = 1 ] && [[ $said == *"is not in the cgroup legion-test"* ]] && [[ $said != *"pid="* ]] && echo 0 || echo 1)" \
  "it exited $status: $said"
status=0
said=$(RUNNER_TEMP=$work/other bash "$script" run "$work/step.sh" 2>&1) || status=$?
check run-refused-before-fence "$([ "$status" = 1 ] && [[ $said == *"no fence has run"* ]] && echo 0 || echo 1)" "it exited $status: $said"

# --- report ------------------------------------------------------------------------------------
# The listings in iptables' own layouts (`-S`, and `-L -v -x -n`), each family's its own, which
# report prints as they come and reads nothing of but the match row's text.
for family in iptables ip6tables; do
  if [ "$family" = iptables ]; then any=0.0.0.0/0 icmp=icmp opt=--; else any=::/0 icmp=icmp6 opt="  "; fi
  printf '%s\n' "-P OUTPUT ACCEPT" "-A OUTPUT -m cgroup --path legion-test -j LEGION-TEST" >"$out/$family-SOUTPUT"
  printf '%s\n' "-N LEGION-TEST" "-A LEGION-TEST -o lo -j RETURN" "-A LEGION-TEST -j REJECT --reject-with $icmp-port-unreachable" >"$out/$family-SLEGION-TEST"
  printf '%s\n' "Chain OUTPUT (policy ACCEPT 0 packets, 0 bytes)" \
    "    pkts      bytes target     prot opt in     out     source               destination" \
    "    4113   348114 LEGION-TEST  all  $opt  *      *       $any            $any            cgroup legion-test" >"$out/$family-LOUTPUT"
  printf '%s\n' "Chain LEGION-TEST (1 references)" \
    "    3901   330211 RETURN     all  $opt  *      lo      $any            $any" >"$out/$family-LLEGION-TEST"
done
echo "1764000000 tag sha256:aa legion-worker-stand-ins:rig" >"$work/events"
status=0
said=$(bash "$script" report --docker-events 2>&1) || status=$?
printed=0
for family in iptables ip6tables; do
  for listing in SOUTPUT SLEGION-TEST LOUTPUT LLEGION-TEST; do
    # Of OUTPUT's rules, report prints the rows that match the cgroup.
    while IFS= read -r line; do
      [ "$listing" != SOUTPUT ] || [[ $line == *legion-test* ]] || continue
      [[ $said == *"  $line"* ]] || printed=1
    done <"$out/$family-$listing"
  done
done
check report-prints-both-families "$printed" "report left out a line of a family's rule, match row or counters: $said"
check report-passes "$([ "$status" = 0 ] && [[ $said == *"CHECK test-fence-match: PASS"* ]] && [[ $said == *"CHECK test-fence-refusals: PASS"* ]] &&
  [[ $said == *"CHECK test-fence-image-pulls: PASS"* ]] && echo 0 || echo 1)" "it exited $status: $said"
printf '%s\n' "-P OUTPUT ACCEPT" >"$out/ip6tables-SOUTPUT"
status=0
said=$(bash "$script" report 2>&1) || status=$?
check report-fails-without-a-match "$([ "$status" = 1 ] && [[ $said == *"CHECK test-fence-match: FAIL: no OUTPUT row of ip6tables matches the cgroup legion-test"* ]] && echo 0 || echo 1)" \
  "it exited $status: $said"
printf '%s\n' "-P OUTPUT ACCEPT" "-A OUTPUT -m cgroup --path legion-test -j LEGION-TEST" >"$out/ip6tables-SOUTPUT"
echo "[  42.1] legion-test refused: IN= OUT=eth0 SRC=10.1.0.4 DST=140.82.112.6 LEN=60 PROTO=TCP SPT=40312 DPT=443" >"$work/dmesg"
status=0
said=$(bash "$script" report 2>&1) || status=$?
check report-fails-on-a-refusal "$([ "$status" = 1 ] && [[ $said == *"DST=140.82.112.6"* ]] && [[ $said == *"CHECK test-fence-refusals: FAIL"* ]] && echo 0 || echo 1)" "it exited $status: $said"
: >"$work/dmesg"
echo "1764000001 pull ghcr.io/example/image:1 ghcr.io/example/image" >"$work/events"
status=0
said=$(bash "$script" report --docker-events 2>&1) || status=$?
check report-fails-on-a-pull "$([ "$status" = 1 ] && [[ $said == *"CHECK test-fence-image-pulls: FAIL"* ]] && echo 0 || echo 1)" "it exited $status: $said"

status=0
bash "$script" fence --nothing >/dev/null 2>&1 || status=$?
check usage-error "$([ "$status" = 2 ] && echo 0 || echo 1)" "an unknown argument exited $status"

exit "$failed"
