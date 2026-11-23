#!/usr/bin/env bash
# .github/scripts/test-user.test.sh
#
# Tests .github/scripts/test-user.sh with no root, no user made and no rule applied: fakes of
# sudo, iptables, ip6tables, useradd, usermod, id, setfacl, visudo, docker, dmesg and git on PATH
# record what they were asked, and sudo runs what it was given as this user. It checks, one
# CHECK line each: that fence puts every outbound packet of the user's uid through a chain that
# passes established connections, loopback and each subnet of the named networks (and of a named
# container's), of the chain's own family alone, then logs with the prefix and the uid and
# rejects, naming no host; that --docker adds the user to the docker group and --signal-as writes
# a sudoers entry of kill and environ alone; that a grant reaches the path, its directories'
# defaults and the directories above it; that a step run through the installed shell runs as the
# user with the Go settings and the step's exit status, and is refused before any fence; that
# report passes on no refusal and no pull, and fails on a refused packet and on a pull; and that
# a usage error exits 2.
#
# Run from anywhere: .github/scripts/test-user.test.sh
set -euo pipefail

script="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/test-user.sh"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
bin=$work/bin
mkdir -p "$bin"
calls=$work/calls

failed=0
check() { # <name> <0 for a pass> <what failed>
  if [ "$2" = 0 ]; then echo "CHECK $1: PASS"; else echo "CHECK $1: FAIL: $3"; failed=1; fi
}
has() { grep -qxF -- "$2" <<<"$1" && echo 0 || echo 1; }

for tool in iptables ip6tables useradd usermod setfacl visudo git apt-get; do
  printf '#!/usr/bin/env bash\necho "%s $*" >>"%s"\n' "$tool" "$calls" >"$bin/$tool"
done
# sudo records its options and the command, then runs the command as this user.
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
exec "\$@"
EOF
cat >"$bin/id" <<'EOF'
#!/usr/bin/env bash
if [ "$1" = -u ]; then echo 4321; else exit 0; fi
EOF
cat >"$bin/docker" <<EOF
#!/usr/bin/env bash
case "\$1 \$2 \${3:-}" in
  "network inspect kind") printf '172.18.0.0/16\nfc00:f853:ccd:e793::/64\n\n' ;;
  "network inspect github_network_1") printf '172.19.0.0/16\n' ;;
  "inspect postgres-1 --format") printf 'github_network_1\n' ;;
  events*) cat "$work/events" ;;
  *) echo "docker: unexpected \$*" >&2; exit 1 ;;
esac
EOF
printf '#!/usr/bin/env bash\ncat "%s"\n' "$work/dmesg" >"$bin/dmesg"
chmod +x "$bin"/*
: >"$work/events"
: >"$work/dmesg"

export PATH="$bin:$PATH" LEGION_TEST_SHELL="$work/legion-test-shell" LEGION_TEST_SUDOERS="$work/sudoers"
export RUNNER_TEMP=$work/runner GITHUB_WORKSPACE=$work/checkout
mkdir -p "$RUNNER_TEMP" "$GITHUB_WORKSPACE/sub" "$work/cache/mod"

# --- fence -------------------------------------------------------------------------------------
out=$(bash "$script" fence --docker --signal-as runner --network kind --container postgres-1 \
  --grant "$GITHUB_WORKSPACE" --read "$work/cache" 2>&1) || { echo "$out"; check fence-runs 1 "fence exited non-zero"; }
v4=$(grep '^iptables ' "$calls" | sed 's/^iptables //')
v6=$(grep '^ip6tables ' "$calls" | sed 's/^ip6tables //')
want_v4="-N LEGION-TEST
-A LEGION-TEST -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN
-A LEGION-TEST -o lo -j RETURN
-A LEGION-TEST -d 172.18.0.0/16 -j RETURN
-A LEGION-TEST -d 172.19.0.0/16 -j RETURN
-A LEGION-TEST -j LOG --log-prefix legion-test refused:  --log-uid
-A LEGION-TEST -j REJECT
-I OUTPUT 1 -m owner --uid-owner 4321 -j LEGION-TEST
-S LEGION-TEST"
want_v6="-N LEGION-TEST
-A LEGION-TEST -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN
-A LEGION-TEST -o lo -j RETURN
-A LEGION-TEST -d fc00:f853:ccd:e793::/64 -j RETURN
-A LEGION-TEST -j LOG --log-prefix legion-test refused:  --log-uid
-A LEGION-TEST -j REJECT
-I OUTPUT 1 -m owner --uid-owner 4321 -j LEGION-TEST
-S LEGION-TEST"
check fence-ipv4-chain "$([ "$v4" = "$want_v4" ] && echo 0 || echo 1)" "iptables was asked: $v4"
check fence-ipv6-chain "$([ "$v6" = "$want_v6" ] && echo 0 || echo 1)" "ip6tables was asked: $v6"
named=$(grep -E '^ip6?tables ' "$calls" | grep -oE -- '-d [^ ]+' | grep -vE -- '^-d [0-9a-f:./]+$' || true)
check fence-names-no-host "$([ -z "$named" ] && echo 0 || echo 1)" "a rule names $named"
check fence-docker-group "$(has "$(cat "$calls")" "usermod -aG docker legion-test")" "no usermod -aG docker legion-test"
check fence-signal-as "$([ "$(cat "$work/sudoers")" = 'legion-test ALL=(runner) NOPASSWD: /usr/bin/kill, /usr/bin/cat /proc/[0-9]*/environ' ] && echo 0 || echo 1)" \
  "the sudoers entry is $(cat "$work/sudoers")"
acls=$(grep '^setfacl ' "$calls")
grant_ok=0
for want in "setfacl -R -m u:legion-test:rwX $GITHUB_WORKSPACE" "setfacl -m u:legion-test:x $work" "setfacl -R -m u:legion-test:rX $work/cache" \
  "setfacl -m d:u:legion-test:rwX $GITHUB_WORKSPACE $GITHUB_WORKSPACE/sub" "setfacl -m d:u:legion-test:rX $work/cache $work/cache/mod"; do
  [ "$(has "$acls" "$want")" = 0 ] || grant_ok=1
done
check fence-grants "$grant_ok" "setfacl was asked: $acls"
check fence-safe-directory "$(has "$(cat "$calls")" "git config --global --add safe.directory $GITHUB_WORKSPACE")" "no safe.directory for the checkout"

# --- run ---------------------------------------------------------------------------------------
printf 'echo "GOPROXY=$GOPROXY GOTOOLCHAIN=$GOTOOLCHAIN HOME=$HOME"\nexit 3\n' >"$work/step.sh"
status=0
ran=$("$LEGION_TEST_SHELL" "$work/step.sh" 2>&1) || status=$?
as_user=$(grep -c -- '-u legion-test :: env ' "$calls" || true)
check run-as-the-user "$([ "$status" = 3 ] && [ "$ran" = "GOPROXY=off GOTOOLCHAIN=local HOME=/home/legion-test" ] && [ "$as_user" -ge 1 ] && echo 0 || echo 1)" \
  "the step exited $status and printed $ran, with $as_user sudo -u legion-test calls"
status=0
out=$(RUNNER_TEMP=$work/other bash "$script" run "$work/step.sh" 2>&1) || status=$?
check run-refused-before-fence "$([ "$status" = 1 ] && [[ $out == *"no fence has run"* ]] && echo 0 || echo 1)" "it exited $status: $out"

# --- report ------------------------------------------------------------------------------------
echo "1764000000 tag sha256:aa legion-worker-stand-ins:rig" >"$work/events"
status=0
out=$(bash "$script" report --docker-events 2>&1) || status=$?
check report-passes "$([ "$status" = 0 ] && [[ $out == *"CHECK test-user-refusals: PASS"* ]] && [[ $out == *"CHECK test-user-image-pulls: PASS"* ]] && echo 0 || echo 1)" "it exited $status: $out"
echo "[  42.1] legion-test refused: IN= OUT=eth0 SRC=10.1.0.4 DST=140.82.112.6 PROTO=TCP DPT=443 UID=4321" >"$work/dmesg"
status=0
out=$(bash "$script" report 2>&1) || status=$?
check report-fails-on-a-refusal "$([ "$status" = 1 ] && [[ $out == *"DST=140.82.112.6"* ]] && [[ $out == *"CHECK test-user-refusals: FAIL"* ]] && echo 0 || echo 1)" "it exited $status: $out"
: >"$work/dmesg"
echo "1764000001 pull ghcr.io/example/image:1 ghcr.io/example/image" >"$work/events"
status=0
out=$(bash "$script" report --docker-events 2>&1) || status=$?
check report-fails-on-a-pull "$([ "$status" = 1 ] && [[ $out == *"CHECK test-user-image-pulls: FAIL"* ]] && echo 0 || echo 1)" "it exited $status: $out"

status=0
bash "$script" fence --nothing >/dev/null 2>&1 || status=$?
check usage-error "$([ "$status" = 2 ] && echo 0 || echo 1)" "an unknown argument exited $status"

exit "$failed"
