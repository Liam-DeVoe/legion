# Notes on `.github/workflows/ci-caches.yaml`

## Why typecheck's darwin vet is left out of go-daemon

typecheck's darwin vet is the one build of the daemon module that the go-daemon entry leaves out.
Its darwin objects, about 1.65 GB of build cache on a devbox, would ride in an entry that every job restoring go-daemon downloads, and the step builds them in about two minutes (run 43990864191).
