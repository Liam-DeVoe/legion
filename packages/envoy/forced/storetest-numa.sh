#!/usr/bin/env bash
# The forced reproduction of TestHoldingAThreadKeepsNUMABalancingOffItUntilItIsLetGo's failure:
# the storetest tests run under nomempolicy, a seccomp filter that answers EPERM to get_mempolicy,
# set_mempolicy and mbind, as a container runtime's default profile does to a container without
# CAP_SYS_NICE. On a machine NUMA balancing leaves alone that is the state where main's test fails
# at its first read of the thread's memory policy. FORCED_NUMA_BALANCING=1 makes the rig also read
# NUMA balancing as on (forced_numa_linux_test.go). The arguments go to go test before the package.
#
# usage: storetest-numa.sh [go test flags...]   (run from anywhere; RUN defaults to the one test)
set -euo pipefail
cd "$(dirname "$0")/.."
bin=${RUNNER_TEMP:-${TMPDIR:-/tmp}}/nomempolicy
go build -o "$bin" ./forced/nomempolicy
run=${RUN:-'^TestHoldingAThreadKeepsNUMABalancingOffItUntilItIsLetGo$'}
exec "$bin" go test -count=1 -v -run "$run" "$@" ./internal/dispatch/store/storetest
