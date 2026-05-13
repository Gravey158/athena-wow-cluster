# e2e/

End-to-end tests against a deployed `wow-cluster` namespace. Phase 5 deliverable.

Planned: a WoW-bot client (likely `mangosbot` adapted) running as a Kubernetes `Job`,
asserting on:

- Auth + character-list + realm enter
- Map transition between two worldserver pods without disconnect
- BG queue from two characters lands in the same cross-realm match
- AH operation latency p95 < 200 ms (Phase 3)

Skeleton only until Phase 5. Do **not** add tests here that depend on the cluster
state you happen to have right now — every test must be a self-contained `Job`
manifest with all preconditions reproducible from scratch.
