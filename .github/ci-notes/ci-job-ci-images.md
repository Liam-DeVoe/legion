# Notes for the ci-images job's steps

The derivations of `.github/workflows/ci-job-ci-images.yaml`, which points into this file. Each
`# why:` line of that workflow names the section below that holds the derivation its value rests
on. A value's own arithmetic stays beside the value.

## The job's limit, held to the crane calls of the image copy

`.github/scripts/mirror-ci-images.sh` runs every crane call through
`.github/scripts/network-install.sh`'s bound for crane: three attempts of 60 s, each killed 15 s
after its SIGTERM, with two 10 s pauses, 245 s before it fails by name. The job's limit must end
after the last of them can fail by name: up to it, the bounds of every step before it and the
calls themselves. The job has no service and no container, so the runner pulls nothing.

- 496 s: `.github/actions/install-crane`, at the worst case `.github/ci-notes/ci.md`'s "The worst
  case of the tool installs that read .github/scripts/ci-tools.sh" derives.
- The copy step, by the rows of `.github/ci-images.yaml` (15 copied rows and 1 built row today).
  A built row makes no call. A copied row the package holds makes one, `crane digest` of its tag;
  one the package lacks makes three, that read, `crane copy` and `crane digest` of what it copied.
  The library/golang row makes one more, `crane config` of its Go. Which rows the package lacks is
  the package's state and not this tree's, so the limit counts the worst the tree allows, every
  copied row copied: 15 × 3 × 245 s + 245 s = 11,270 s. The common run, which copies nothing, makes
  16 calls, 3,920 s at their worst.

496 s + 11,270 s = 11,766 s, which 197 minutes (11,820 s) hold. The 25 minutes the job had end
before even the common run's 3,920 s, so a run whose calls each waited out most of their attempts
could be ended by GitHub's cancellation, which names nothing, before a later call failed by name.

The steps after the copy are no part of the sum, since no install follows them: the package mode
of `.github/scripts/check-ci-images.sh` bounds each read it makes (30 s for each of its HTTP
requests, 120 s for its `crane config`), and `.github/scripts/mirror-ci-tools.sh` reads under the
rows' own `max_time`.
