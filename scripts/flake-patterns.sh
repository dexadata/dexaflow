# shellcheck shell=bash
# FLAKE_RE: the CI failures that are safe to rerun, sourced by cut-release.sh
# (its waits on the prepare PR, the merge commit and the release run) and by
# branch-red-watch.sh (the rerun before a red main opens an issue). One list,
# so a pattern added for one is trusted by both.
#
# These are transient failures, never a code signal. The list matches the
# classes seen in practice: registry rate-limits and 5xx, Go module-proxy stream
# resets, the shallow-fetch merge-base gate, the lite cold-start /readyz timing
# flake, a package index answering 5xx mid-resolve, and a k3d cluster that fails
# to come up. Every alternative is covered by the table in cut-release.sh's
# self_test(); add a pattern there first, with the line that motivated
# it, or it is not coverage.
#
# Deliberately absent: anything that also matches a failure of OUR OWN code. A
# "returned error: 5xx" alternative looks like a registry pattern but is what
# curl prints for a 5xx out of our control plane, which this harness queries
# from 60 sites, so it would rerun past the commonest real regression there is.
# shellcheck disable=SC2034 # read by the scripts that source this file
FLAKE_RE='toomanyrequests|Rate exceeded|TLS handshake|i/o timeout|no space left|Connection reset|context deadline|Client\.Timeout|INTERNAL_ERROR|proxy\.golang\.org|stream ID [0-9]|readyz never responded|go mod download|failed to solve|returned error: 404|reserve cache|no merge base|exit code 128|HTTP Error 5[0-9][0-9]|Gateway Time-out|Cluster creation FAILED|failed Cluster Creation'
