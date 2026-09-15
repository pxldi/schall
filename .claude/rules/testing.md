---
paths:
  - "**/*_test.go"
  - "web/**/*.test.ts"
---

# Testing rules

- One behaviour per test. If the name needs "and", split it.
- Test names state the behaviour, not the method: `rejects_expired_token`, not
  `test_validate`.
- Assert on observable behaviour, never on internal call counts, unless the call
  itself is the contract.
- Mock at the process boundary only — network, clock, filesystem, external APIs.
  Do not mock schall's own modules to make a test easier to write.
- New branch, new test. A bug fix lands with the test that would have caught it.
- Never weaken an assertion, add a skip, or widen a tolerance to get green.
  If a test is genuinely wrong, say so and explain why before changing it.
