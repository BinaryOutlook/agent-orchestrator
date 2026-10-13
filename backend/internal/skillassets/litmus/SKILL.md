---
name: litmus
description: "Test one pull request by driving its build, comparing it with main, and leaving evidence."
trigger: "You were asked to test a PR with `ao testing`."
---

# Litmus PR testing

You test one pull request. Litmus builds the PR's commit and the main commit it branched from, each in a private app window. Find out whether the PR does what it says and broke nothing nearby. You have 25 minutes from when the builds are ready; after that every verb is refused.

You have a normal shell. Put scratch files in `scratch/`; never edit the files Litmus writes in the run folder. Never open another run's folder; it is not evidence. Build, open and drive the two app windows only through `ao testing`, leave the checkouts alone, and never push or post to GitHub (also not `ao testing start|post|prune` or `gh auth`).

The loop:

1. `ao testing intake <pr-url>`, then read `context-pack.md`: the PR text, the diff by area and suggested checks.
2. Decide what to check: the PR's headline claim first, in the author's words, then the things most likely to have broken around it. Plan pairs: at most 4 for a PR over 30 files, 6 otherwise (Litmus accepts at most 6 checks; a main twin is free). One observable thing per check; split a check that mixes two things: `ao testing check add <name> --expect "<what a user would see>"`.
3. `ao testing prepare` (waits for both builds), then `ao testing launch head` (the PR build) and `ao testing launch base`.
4. Run each check on the PR build, then at once on main, before the next check; it is done only when it has a result on both. `check start <id>`, then `snapshot` to see, `click <ref>`, `fill <ref> <text>`, `type`, `press <key>`, `wait` to act. Finish with `check end <id> --result pass|fail|partial|absent|skipped --note "<what you saw>"`. On main, re-add the check by name with `--leg base` and pass `--leg base` on every verb.
5. A check that passes on the PR build and fails on main is what makes a verdict. If main does not have the feature at all, end the main side as `absent` with a screenshot of where it would be; `fail` means the feature is there and broken. If main will not build or launch, or time is short, `submit --skip-base --reason "<why>"` records each unrepeated check as not run on main.
6. With time left, add another pair (next suggested check or changed area), never a check on the PR build alone. Stop early only when nothing is left.
7. `ao testing submit` writes the verdict, posts the comment and ends your session.

Explore. Use the feature as a user would, changing any Settings needed to reach it. Only an account, login, key or build-time flag is out of reach: skip the check and say which. When a feature does not show, read the context pack's gate notes and the code first.

Never import, export, sign in to or read the owner's real browser profiles, cookies, passwords, keychain or accounts; check such a feature only with data created in the test window, or end it as `skipped` with that reason.

Agent chats run on the owner's account: unless the PR is about an agent or model, use Haiku 5.5 or Codex's smallest model and a one-line prompt.

Judge visual expectations by Reading the after-shot the verb named; `compare <id>` diffs a check's main and PR-build shots when the difference is subtle.

PRs that touch renderer or UI files: do the UI checks in the pack's suggested order; package tests come last, with at least 5 minutes left, at most one test check.

PRs with nothing to click (no renderer or UI files in the diff): run each touched package's tests on both builds (`ao testing test --leg head --dir backend -- go test ./internal/cli/ -count=1`, then `--leg base`) and record one check per package with the counts in the note. For a bug-fix PR that adds tests, also run the `--leg base` command with `--with-tests-from head`: it runs the PR's new or changed test files on main's code. A test that ran and failed there is `fail` on main (summary as the note); tests that do not compile there are `skipped` with the note "needs code main does not have". Prove a feature PR with a screenshot, not by running its new tests on main. Name the package in words in the expectation.

Notes. The PR author reads them; write what you saw in plain words: buttons present, what happened after the click, what was missing. No check ids, build names or remarks about what you compared; Litmus adds those.

Results. `pass`: you saw the expectation hold. `fail`: you saw it not hold. `partial`: you saw part of it and could not reach the rest; say which part. `skipped`: you could not reach it at all; say why. A pass or fail needs a receipt, so act on the check's build first.

When stuck. Never ask; nobody answers. End a check you cannot get working as `skipped` with a one-line reason. If intake fails on the network or GitHub, retry once after a minute. If a verb fails and you cannot fix it, or intake fails again, `ao testing submit --blocked --reason "<what failed>"` and stop. `status` shows the run, deadline and checks.
