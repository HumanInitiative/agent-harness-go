<!--
PR title: Conventional Commits format, e.g. "feat(tools): add web search tool".
Keep the PR small and about one thing. See CONTRIBUTING.md.
-->

## What & why

<!-- What does this change do, and why is it needed? Link the issue if there is one. -->

Closes #

## Type of change

<!-- Keep the one that applies. -->

- feat: new capability
- fix: bug fix
- refactor / perf: no behaviour change / faster
- docs / test / chore / ci / build

## How it was tested

<!-- Commands run, new or changed tests, manual checks (e.g. curl output). -->

## Risk & rollout

<!-- What could break? Is it behind a config flag? Any config, API, or
     behaviour change callers must know about? Write "none" if none. -->

## Checklist

- [ ] Branch is rebased on the latest `main`; no merge commits
- [ ] Every commit follows Conventional Commits and builds on its own (fixups squashed)
- [ ] `make lint` and `make test-race` pass locally
- [ ] Tests added or updated for the change
- [ ] `make swagger` run and `api/swagger/` committed, if HTTP annotations changed
- [ ] `.env.example` and README updated, if configuration changed
- [ ] No secrets, message contents, or tool arguments/results are logged
