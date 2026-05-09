---
name: develop
description: Use for any development work — implementing a feature, fixing a bug, refactoring, or any production code change. Enforces senior-engineer discipline with strict TDD (one test at a time, red → green → refactor), minimum-viable implementation, clean code in production and test code, and coverage-driven gap detection. Activates for tasks like "implement X", "build Y", "add functionality for Z", "fix bug in W", "refactor V".
---

# Develop

How you write code. Every feature, every bug fix, every change.

You write production code only in response to a failing test. You write **one** test at a time, watch it fail for the right reason, write the minimum code to pass, then refactor — both production code and test code. Repeat until the behavior is fully specified. Then use coverage to find gaps.

This is the loop you actually run. Not aspirational. Not "TDD-ish."

## Step 0 — Brainstorm a test list (plan, do not implement)

Before writing any code, list the behaviors the feature/bug fix needs to specify. Bullet list, plain English, one line each. Examples:

- empty cart total is zero
- single item cart total equals item price
- two-item cart sums prices
- discount code reduces total
- expired discount code is rejected

Do **not** write tests yet. Do **not** order them by implementation complexity. Just enumerate behaviors. Pick the simplest one and enter the cycle.

Add to the list as you discover behaviors. Cross items off as you specify them.

## The cycle

Run these four steps in order, every cycle. Do not skip ahead.

### 1. Write ONE test

Pick the smallest unchecked behavior from the list.

**Before writing a new test, read the existing test files for the unit/module under change.** If your scenario is genuinely a different behavior, write a new test. But if it's best understood as another assertion or input-variant of a behavior an existing test already covers, augment that test (or parameterize it) instead of duplicating setup. Decide with these two questions:

- Would the new test's Arrange section be substantially identical to an existing test's? (Smell — consider augmenting or parameterizing.)
- Would the existing test's name still accurately describe its behavior after adding this assertion? (Yes → augment. No, the name would become "does X **and** Y" → split into a new test.)

Default to a new test only when the answer to the second question is "no."

Then write a single test (or single assertion added to an existing test):

- Test name describes the behavior in plain English ("returns empty list when input is empty"), not the implementation ("calls map then filter").
- AAA structure (Arrange / Act / Assert). One concept per test — multiple assertions are fine if they describe one behavior.
- Assert on observable outcomes, not implementation details. Don't assert "method X was called with Y" unless the call itself is the contract.

Then **stop**. Do not write a second test, even if the next one is obvious. Do not write production code.

If you catch yourself starting a second test, delete it and add a TODO line to the test list instead.

### 2. Run the test — verify it fails for the right reason

Run the test suite. The new test must fail. Read the failure message:

- **Fails with the expected reason** (function not defined, AssertionError comparing expected vs actual): proceed to step 3.
- **Fails for any other reason** (import error, syntax error, fixture missing, wrong setup): the test is broken. Fix the test — not the production code. Stay in step 2 until the failure is clean.
- **Passes**: the test isn't testing what you think. Either the behavior already exists (move to next item on the list), the assertion is wrong (fix it), or the arrange step accidentally produced the asserted state (fix it).

This step is non-negotiable. The compiler is not a test. Passing-by-coincidence is real and frequent.

### 3. Write the MINIMUM code to pass

Whatever it takes to make this one test green. Three legitimate strategies, in order of preference:

1. **Obvious implementation** — if the right code is small and clear, write it directly.
2. **Fake it** — return a hard-coded value matching the test's expectation. Use this when the real implementation is unclear; the next test will force you to generalize.
3. **Triangulate** — if you faked it, the next cycle's test must use a different input that the fake cannot satisfy. That's how you generalize honestly.

Do **not**:

- Add code not required by the current failing test.
- Add defensive checks for inputs no test covers.
- Add error handling for branches no test reaches.
- Anticipate the next test by pre-implementing it.
- Add "while I'm here" features, logging, or refactors. Refactor comes in step 4.

Run the **full** suite. All tests must pass. If a previously-green test is now red, the new code broke something — fix or revert before continuing.

### 4. Refactor — production code AND test code

With the suite green, look for cleanup. Both sides:

**Production code:**
- Names: do they describe intent? Rename if not.
- Duplication: extract when the same idea appears in 2+ places (rule of three for new abstractions; rule of two for clear duplication).
- Cohesion: should this method/class be split? merged?
- Dead code: remove anything not exercised.

**Test code (equally important — tests are first-class code):**
- Keep test files short and scannable. A reader should grasp what's tested in roughly one screen. When a file gets long, the cause is usually duplicated setup or assertions that should have been parameterized.
- Extract setup helpers when arrangements repeat across tests. Keep helpers **in the same test file** by default (`build_user(...)`, `given_an_empty_cart()`, `assert_invoice_matches(...)` — named for what they do, not how). Only promote to a shared test utility module when 2+ test files actually need the helper; do not pre-share.
- Use parameterized tests when many cases differ only by input/output.
- Test names still describe behavior after each addition?
- Remove dead arrangements and unused fixtures.
- No logic in tests (no `if`, no loops over assertions). If a test has logic, it needs its own test — which is a smell. Split into multiple tests instead.

**Constraints:**
- Refactoring changes structure, not behavior. Suite stays green throughout. Run tests after each refactoring step, not just at the end.
- If a refactor needs a behavior change, stop. That's a new red-green-refactor cycle, not a refactor.
- Don't add abstractions for "future flexibility." Three concrete cases beats one premature abstraction.

**Commit at green. Commit again after refactor.** Two commits per cycle is the norm — one for the new behavior, one for the cleanup. Keeps history readable as a series of behavior additions.

Then return to the test list and start the next cycle.

## After the feature — coverage as a gap detector

When you believe the feature is implemented (test list is exhausted), run coverage with **branch coverage** enabled:

- Python: `pytest --cov=<pkg> --cov-branch --cov-report=term-missing`
- JS/TS: `jest --coverage` (configure `coverageReporters` to include `text` for line-by-line)
- Go: `go test -cover -coverprofile=cov.out && go tool cover -func=cov.out`
- Rust: `cargo tarpaulin` or `cargo llvm-cov`

For each uncovered line or branch, ask: **what behavior does this line implement?**

- If the answer is a real behavior, add it to the test list and enter the cycle. The test you write **must fail** before you change the line — that proves the line is load-bearing.
- If you cannot articulate a behavior the line provides, the line is dead code. Delete it and re-run the suite.

Coverage is a signal, not a goal. 100% coverage of nonsense is worthless; 80% coverage of every meaningful behavior is excellent. Use uncovered-lines as a checklist of "behaviors I forgot to specify," not a number to maximize.

## Hard rules — do not violate

- No production code without a failing test that requires it.
- No second test while another test is failing (other than the one you just wrote).
- No batching: never write two tests in a row without green-and-refactor between them.
- No "I'll add a test for that later." Add it now or delete the code now.
- No skipping the run-it-fail step.
- No coverage-chasing tests that exercise lines without asserting behavior.
- No refactoring with red tests. Get green first.

## Anti-patterns to recognize and stop

- **"I'll write the feature, then add tests."** Not TDD. Tests written after codify accidents instead of intentions and miss the design feedback TDD exists to provide.
- **"I'll write all the tests up front, then implement."** Same problem in reverse — you're guessing at the design instead of letting tests pull it out.
- **Tests that took 30 seconds to write but the implementation took an hour.** Smell: the test is probably testing implementation details, not behavior. Rewrite the test against the public contract.
- **Mocking what you own.** Mock at architectural seams (network, clock, filesystem when not the unit under test, third-party SDKs). Don't mock your own pure functions — call them.
- **Asserting on internals.** Tests against private methods or internal state break under harmless refactors. Test through the public interface.
- **Shared mutable test fixtures.** Tests must be independent. If reordering tests changes pass/fail, the fixtures are wrong.

## Picking the next test when stuck

Order to try:
1. **Simplest happy path** — one example with no edge conditions.
2. **Degenerate input** — empty, zero, null, single element.
3. **Boundary** — first valid value, last valid value, just outside.
4. **Representative real case** — closer to production data.
5. **Known edge cases** — duplicates, ordering, unicode, large inputs.
6. **Error conditions** — invalid input, dependency failures, timeouts.

Stop adding tests when a new test no longer changes any production code. That's the signal the behavior is fully specified.

## Commit cadence

- **Green commit**: subject = the behavior that just passed. Example: `cart: empty cart totals to zero`.
- **Refactor commit**: subject starts with `refactor:` and describes the structural change with no behavior change. Example: `refactor: extract LineItem.total`.

Two-commit-per-cycle keeps `git log` legible as a feature's behavioral history and makes refactors trivially revertable if they prove wrong.
