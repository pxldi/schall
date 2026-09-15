# 0005 — Review-queue rejections never feed the recommendation model

**Status:** accepted, 2026-07-29

## Context

The review queue produces a stream of explicit negative signals ("none of
these", per-(file, target) rejections). Recommendation systems are hungry for
exactly such signals, and wiring them in looks like free training data.

## Decision

Review-queue decisions and recommendation signals live in **separate stores**
and never flow into each other. Recommendation suppression draws only on its
own inputs: ownership, open requests, followed-artist routing, explicit
dismissals, and time-limited impression fatigue.

## Why

A review rejection means "this *file* is not that recording". It is a
statement about audio identity, made about music the user **explicitly asked
for**. It carries zero information about taste — if anything, the opposite:
the user wanted the recording enough to review candidates for it.

## What breaks if reversed

The recommendation model learns to suppress exactly the music the user most
wants — every target that was hard to acquire accumulates "negative" signals
proportional to how hard the user tried. The system silently stops surfacing
the user's most-wanted music, and nothing in either subsystem looks broken.
