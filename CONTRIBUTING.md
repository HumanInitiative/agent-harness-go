# Contributing

How we work on agent-harness-go. Most of these rules are enforced by
tooling (git hooks, CI, and GitHub repository settings); the sections below
say which, so nothing here is only a convention you have to remember.

- [One-time setup](#one-time-setup)
- [Workflow: trunk-based development](#workflow-trunk-based-development)
- [Branch names](#branch-names)
- [Commit messages](#commit-messages)
- [Pull requests](#pull-requests)
- [Code review](#code-review)
- [Releases](#releases)
- [Repository settings](#repository-settings)

## One-time setup

```bash
make setup   # installs the git hooks and the pinned swag CLI
```

The hooks check commit messages (`commit-msg`) and, before every push, the
branch name and `make lint` (`pre-push`). CI runs the same checks, so
skipping the hooks with `--no-verify` only moves the failure to your PR.

## Workflow: trunk-based development

`main` is the trunk. It is always green and always deployable. Everyone
integrates into it continuously through small, short-lived branches — there
are no `develop`, `release`, or other long-lived branches.

```
1. branch off      main     A───B
                                 \
                   feat/x         C1───C2          (someone merges D meanwhile)

2. rebase          main     A───B───D
                   feat/x   A───B───D───C1'───C2'  (git rebase origin/main)

3. rebase & merge  main     A───B───D───C1'───C2'  (linear: no merge commit)
```

1. **Start from the latest `main`.**
   ```bash
   git switch main && git pull --rebase
   git switch -c feat/search-tool
   ```
2. **Keep the branch short-lived**: merged within 1–2 days. If a feature is
   bigger than that, split it into several PRs that each leave `main`
   working.
3. **Hide unfinished work behind a config flag** instead of keeping a branch
   open. Add an environment variable in `internal/platform/config` that
   defaults to *off*, merge the incomplete work behind it, and remove the
   flag once the feature is finished.
4. **Stay current by rebasing, never by merging `main` in.**
   ```bash
   git fetch origin && git rebase origin/main
   git push --force-with-lease   # never plain --force
   ```
5. **Open a pull request early** (as a draft if it is not ready), get it
   reviewed, and **rebase and merge** it.
6. **If `main` breaks, revert first, fix second.** `git revert <sha>` in a
   PR restores a working trunk immediately; the real fix follows in its own
   PR.

## Branch names

`<type>/<short-kebab-description>`, optionally with the issue number:

| Type       | Use it for                                          | Example                              |
|------------|-----------------------------------------------------|--------------------------------------|
| `feat`     | A new capability                                    | `feat/web-search-tool`               |
| `fix`      | A bug fix (urgent production fixes too)             | `fix/42-timeout-on-long-replies`     |
| `refactor` | Restructuring with no behaviour change              | `refactor/split-chat-handler`        |
| `perf`     | Performance improvement                             | `perf/reuse-genkit-tools`            |
| `docs`     | Documentation only                                  | `docs/deployment-guide`              |
| `test`     | Adding or fixing tests only                         | `test/memory-store-eviction`         |
| `chore`    | Maintenance, dependency bumps                       | `chore/upgrade-genkit`               |
| `ci`       | CI/CD pipelines and repository automation           | `ci/cache-go-modules`                |
| `build`    | Build system, Dockerfile, Makefile                  | `build/smaller-image`                |
| `revert`   | Reverting an earlier change                         | `revert/web-search-tool`             |

Lowercase letters, digits and single hyphens only; at most 60 characters.
There is no `hotfix/` type: with trunk-based development an urgent fix is a
normal `fix/` branch that gets reviewed first. Enforced by the `pre-push`
hook and the `conventions` CI check (`scripts/check-branch-name.sh`).

## Commit messages

We follow [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(<optional-scope>): <description>

<optional body: what changed and why, wrapped at 72 characters>

<optional footer: Refs: #42, BREAKING CHANGE: ...>
```

- **type**: the same list as for branch names.
- **scope** (optional) — the area touched, usually a package: `agent`,
  `httpapi`, `genkit`, `memory`, `tools`, `config`, `logger`, `deps`,
  `docker`.
- **description**: imperative mood ("add", not "added"), no capital first
  letter, no trailing period, ideally ≤ 72 characters (hard limit 100).
- **breaking change**: add `!` after the type/scope (`feat(httpapi)!: ...`)
  and explain it in a `BREAKING CHANGE:` footer.
- The body explains **why**; the diff already shows what.

```
feat(tools): add web search tool
fix(httpapi): return 504 instead of 500 when the request times out
refactor(agent): move history windowing into its own function
chore(deps): bump github.com/firebase/genkit/go to 1.14.0
```

**Why every commit matters here:** we merge with *rebase and merge*, so every
commit on your branch lands on `main` exactly as it is. Each commit must
therefore follow the convention, build, and pass the tests on its own. While
a PR is in review, address feedback with fixup commits, then fold them in
before merging:

```bash
git commit --fixup <sha-of-the-commit-being-fixed>
git rebase -i --autosquash origin/main
git push --force-with-lease
```

Enforced by the `commit-msg` hook and the `conventions` CI check
(`scripts/check-commits.sh`), which also rejects merge commits and any
leftover `fixup!` commits.

## Pull requests

- **Title**: Conventional Commits format, describing the whole change.
- **Description**: the template is filled in automatically when you open the
  PR (`.github/pull_request_template.md`). Complete every section.
- **Size**: one concern per PR, ideally under ~400 changed lines excluding
  generated files. Small PRs get reviewed faster and more carefully.
- **Before requesting review**: `make ci` passes locally, and the branch is
  rebased on the latest `main`.

A PR can be merged only when (enforced by GitHub):

- all CI checks pass: `conventions`, `lint`, `test`, `swagger`, `build`;
- it has the required approval from someone other than the author, and no
  new commits were pushed after that approval;
- every review conversation is resolved;
- the branch is up to date with `main`.

**Merging:** use **Rebase and merge**, the only method enabled. Merge
commits and squash merging are disabled to keep `main` linear. The author
merges once the PR is approved; the branch is deleted automatically.

## Code review

- Reviewers respond within one working day.
- Review for correctness, security (secrets, personal data in logs, untrusted
  tool input), tests, and whether the layer boundaries in the README's
  *Architecture* section still hold.
- Prefix comments so their weight is clear: `blocking:` must be addressed
  before merge, `suggestion:` is optional, `nit:` is cosmetic, `question:`
  asks for clarification.
- Approve once everything `blocking:` is resolved; don't hold a PR for nits.

## Releases

Releases are annotated tags on `main` using semantic versioning. Because
`main` is always deployable, releasing is only tagging:

```bash
git switch main && git pull --rebase
git tag -a v1.2.0 -m "v1.2.0"
git push origin v1.2.0
```

Bump MAJOR for breaking API changes (commits with `!`), MINOR for `feat`,
PATCH for `fix`.

## Repository settings

The merge rules above live in GitHub settings, versioned as a script so they
can be reviewed and re-applied:

```bash
scripts/github-setup.sh                        # default: 1 required approval
REQUIRED_APPROVALS=0 scripts/github-setup.sh   # while there is a single maintainer
```

It configures:

- **Merge methods**: rebase and merge only; head branches deleted after
  merge; the "Update branch" button enabled.
- **Ruleset `protect-main`** on the default branch:
  - changes only through pull requests, with the required approvals;
  - stale approvals dismissed on new pushes;
  - all review threads resolved before merge;
  - required CI checks, with the branch up to date with `main`;
  - linear history;
  - no force pushes and no branch deletion;
  - no bypass, administrators included.

Dependency updates arrive as weekly Dependabot PRs
(`.github/dependabot.yml`) and go through the same checks as any other PR.
The one exception: Dependabot always writes "Bump" with a capital letter, so
its commits are exempt from the lowercase-description rule (and only that
rule).
