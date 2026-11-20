# Notes for the pi-envoy job's steps

The derivations of `.github/workflows/ci-job-pi-envoy.yaml`, which points into this file. Each
`# why:` line of that workflow names the section below that holds the derivation its value rests
on. A value's own arithmetic stays beside the value.

## The job's limit, held to the installs it runs through network-install.sh

The slowest green run of this job in the 20 merge-group runs `.github/scripts/network-install.sh`'s
bounds were measured on (44633135919 to 44668680855) took 443 s, job 136032711558. The installs
it runs through that script can take 915 s before the last of them fails by name: the frozen
install's 425 s (three attempts of bun's 120 s, each killed 15 s after its SIGTERM, with two 10 s
pauses), and the two apt commands of `.github/scripts/runner-tmux.sh`, 245 s each, which run only
on a runner image without tmux. So the limit is 443 s + 915 s = 1,358 s, rounded up to 23
minutes. The ten minutes the job had would end a stalled install in GitHub's cancellation, which
names nothing, and rule 22 of `.github/scripts/check-ci-filters.sh` refuses a job limit that ends
before its wrapped installs can fail by name.
