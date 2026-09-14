---
name: <project>-sdlc
description: Build and iterate on <project> using Gitflow, automated testing and CI/CD. Use this skill whenever working on the <project> codebase — writing code, setting up tests, configuring CI, or deploying. This skill covers the complete SDLC from feature branch to production readiness.
---

# <Project> Development Workflow

**Every rule in this document is stated exactly once.** If you are looking for a
rule and find only a link, follow the link — the target is authoritative and the
place you are standing is not a second copy that can drift out of agreement with
it. Source files (`Makefile`, `.github/workflows/*.yml`, and so on) are likewise
never reproduced here, only described and linked.

---

## Filling this template in

Replace every `<ANGLE_BRACKET>` placeholder. The checklist:

| Placeholder | What it is | Example from the project this came from |
| --- | --- | --- |
| `<project>` | Repository name | `kado-operator` |
| `<INTEGRATION_BRANCH>` | Where feature work merges | `develop` |
| `<RELEASE_BRANCH>` | The default branch; what gets promoted to | `main` |
| `<SHARED_RESOURCE>` | The live thing only post-merge work may touch | a Kubernetes cluster |
| `<VALIDATION_TARGET>` | Where post-merge validation runs | `picard`, a k3d cluster on a laptop |
| `<TEST_CMD>` / `<LINT_CMD>` | The cluster-free checks | `make test` / `make lint` |
| `<GENERATE_CMD>` | Regeneration that must leave no diff | `make manifests generate helm-crds` |
| `<ARTIFACT>` | What a merge publishes | a multi-arch image on Docker Hub |
| `<CORE_PATH>` | The two or three things this project must get right | provision → report honestly → tear down |

Sections marked **[PROJECT-SPECIFIC]** are stubs. Everything else is the process
and transfers unchanged.

---

## Core Principle: <ONE SENTENCE THAT CONSTRAINS EVERY DECISION>

**[PROJECT-SPECIFIC]** — every project needs one, and it is worth arguing about
before writing any of it down. It is the rule that settles arguments later.

State it, then list what it means in practice — three to six concrete
prohibitions a reader can check a diff against. A principle nobody can test a
change against is decoration.

Finish with a check that makes it falsifiable. The project this came from used:
*"if a step would break by pointing `KUBECONFIG` at a remote cluster, it is too
tightly coupled."*

---

## The Loop: Branch → Done → Review → Merge → Validate

Work proceeds in **loops**. One loop is one unit of change: a feature, a fix, a
refactor. Every loop follows the same five gates, in order, with no step skipped
and no step reordered. These are requirements, not suggestions.

```
  1. BRANCH    cut feature/<name> from <INTEGRATION_BRANCH>
       ↓
  2. DONE      every cluster-free test layer passes; merge request opened;
               every CI check green
       ↓
  3. REVIEW    evaluated by a DIFFERENT agent than the author
       ↓
  4. MERGE     only on approval → CI tags the version → publishes
       ↓
  5. VALIDATE  AUTOMATIC, after the merge, against <VALIDATION_TARGET>
```

### Nothing before the merge touches <SHARED_RESOURCE>

**A feature branch must not touch <SHARED_RESOURCE>** — not the shared one, not
a colleague's, not a throwaway one inside a CI runner.

What a merge request runs is therefore exactly the self-contained set:
`<LINT_CMD>`, code generation, unit tests, and whatever local-binary test
harness the project has.

Everything needing the real thing happens **after** the merge, automatically, in
Gate 5.

**Know what this costs, because it is a deliberate trade and not a free one.** A
change that breaks the core behaviour now merges before anything catches it. Be
explicit about which post-merge failures are contained and which are not — the
ones that fail *before* publishing cost a bad commit, and the ones that fail
*after* publishing have already shipped an artifact that cannot be unshipped.

Write that distinction down for this project, because someone will read the
first case and assume it covers both.

An on-demand integration command against a cluster of your own is a **tool, not
a gate** — reach for it deliberately when changing risky behaviour and you want
the feedback early.

### Where work comes from

The backlog is **GitHub Issues**, not a file in this repository.

```bash
gh issue list                    # what is waiting
gh issue view <n>                # why, and what was already decided
gh issue create --label enhancement --title "..." --body "..."
```

That is a deliberate choice about cost. Every merge goes through the five gates
— a branch, an independent review, CI, a version bump and a deploy. Correct for
changing the software, absurd for writing down an idea: a checked-in backlog
would cut a version every time someone had a thought, and would rot the moment
one entry went stale.

Issues cost nothing to open and carry the reasoning next to the item.

**When a decision defers something, open an issue before the context is lost,**
and say in the merge request that you did. A deferral recorded only in a commit
message is a deferral nobody will find.

Write the issue so the next person starts from an answer rather than a hunt: what
it is, how to reproduce it, what was already tried, and **why it was left**.

### Closing issues

**Close an issue by hand as soon as its fix merges to `<INTEGRATION_BRANCH>`.**

Not at the next promotion, and not after post-merge validation. An issue that is
fixed but still open makes `gh issue list` useless — it stops showing what is
actually outstanding, which is the only thing it is for. If a later check fails,
reopen it; that is rarer than the confusion caused by holding it open.

```bash
gh pr merge <n> --merge --delete-branch
gh issue close <n> -c "Fixed in #<pr>, merged to \`<INTEGRATION_BRANCH>\`."
```

GitHub will not do this for you. A closing keyword acts only on the **default
branch**, so `Closes #14` in a merge request body or a commit message leaves the
issue open through the integration merge and fires only at the next promotion —
too late to be useful.

Keep writing `Closes #14` in the **commit message** anyway: it records the link,
and it survives because merge requests here are merged rather than squashed. The
keyword firing again at promotion is a harmless no-op on an already-closed issue.

### Gate 1 — One branch per loop

**Every change starts on a feature branch cut from `<INTEGRATION_BRANCH>`.** No
exceptions, including one-line fixes, documentation edits and changes made by an
AI assistant. `<INTEGRATION_BRANCH>` and `<RELEASE_BRANCH>` are never committed
to directly.

```bash
git checkout <INTEGRATION_BRANCH>
git pull --ff-only
git checkout -b feature/<short-description>
```

Branch names describe the change, not the author or the tool.

Why this is a hard rule rather than a preference: every push to
`<INTEGRATION_BRANCH>` cuts a version and publishes `<ARTIFACT>`. A direct
commit is therefore a release *and* a deployment, made without review, that
cannot be undone without burning a version number.

Feature branches are short-lived and single-purpose: one loop, one branch, one
merge request. Do not continue a finished loop's branch into the next, and do not
accumulate unrelated changes on one — **a reviewer cannot meaningfully approve a
branch that does three things.**

Both branches are protected and reject direct pushes, so the rule is enforced by
the forge rather than by discipline alone.

### Gate 2 — Definition of Done

A loop is **done** when the self-contained checks pass and CI agrees. All of
these MUST be true before the change is offered for review:

| Check | Command | Proves |
| --- | --- | --- |
| Tests | `<TEST_CMD>` | **[PROJECT-SPECIFIC]** what the logic does |
| Lint | `<LINT_CMD>` | No new correctness or style violations |
| Generated code current | `<GENERATE_CMD>` leaves no diff | Committed artefacts match source |
| CI green | On the open merge request | It passes somewhere other than your machine |

**"Tests pass" means you ran them and read the output.** A loop is not done
because the change looks right, because it compiled, or because the tests were
passing before you started. Green locally but red in CI is **not done**.

If a check cannot pass for a defensible reason, say so explicitly in the merge
request. Do not open one claiming done when it is not.

### Gate 3 — Independent agent review

Every merge MUST go through a merge request. There are no direct merges and no
exceptions for small changes.

**The merge request MUST be evaluated by a different agent than the one that
wrote the change.** This is the central rule of this gate.

An agent reviewing its own work is not review. It re-derives the same
assumptions that produced the code, so the failure modes it missed while writing
are exactly the ones it will miss while reading. Worse, it already believes the
change is correct — it wrote it — so it reads to confirm rather than to falsify.

The reviewing agent MUST:

- Start from the **diff and the merge request description**, not the authoring
  conversation. A fresh context is the point.
- **Independently verify the Definition of Done** rather than trusting the
  description. Run the tests. A merge request asserting "tests pass" is a claim
  to check, not evidence.
- Look for what the author could not see: unhandled error paths, missing
  coverage for the change, assumptions that hold only on the author's machine,
  breaking changes to a published interface.
- Return an explicit verdict — **APPROVE**, or **REQUEST CHANGES** with
  specific, actionable findings. "Looks good" is not a verdict.

The authoring agent MUST NOT approve its own merge request, and MUST NOT merge on
the strength of its own assessment.

```bash
/code-review <pr-number>           # a different agent evaluates it
```

Blocking findings are fixed **on the same feature branch** and re-reviewed. A
finding is not resolved by arguing it away in a comment: either change the code,
or explain in the merge request why it does not apply and let the reviewer decide.

#### Knowing when to stop reviewing

A review loop does not converge on its own, and a long one is a signal rather
than a virtue. Two things to watch for:

- **A fix that breaks the previous fix.** When a round's findings are about
  code the *last* round introduced, the loop is generating roughly as many
  problems as it removes. Stop fixing and start filing.
- **Findings that need equipment this project does not have.** A fault requiring
  infrastructure nobody runs cannot be reproduced, cannot be verified fixed, and
  is not worth blocking a release on.

The useful question is not *is this technically a defect* — that has no natural
stopping point — but **does this break `<CORE_PATH>`**. Fix what does. File what
does not, with the reproduction and the reason it was left.

Say plainly in the merge request which findings were fixed and which were filed.

### Gate 4 — Merge, tag, publish

Only an approved merge request may be merged. The merge is what authorises the
version.

**Merge; never squash.** The closing keywords and the reasoning live in
individual commit messages, and a squash replaces them with one synthetic
commit. Use `--merge`, and if a branch has fallen behind, rebase and force-push
with `--force-with-lease` rather than squashing to resolve it.

Nothing is tagged or released that did not pass an independent review.

### Gate 5 — Automatic post-merge validation

**[PROJECT-SPECIFIC]** — describe what runs automatically after the merge,
against `<VALIDATION_TARGET>`, and in what order relative to publishing. Be
precise about which steps run before the artifact is published and which run
after, because that is what decides whether a failure is contained.

State plainly that this gate **cannot block a merge** — it runs after one. It
catches what escaped, it does not prevent escape.

### Working unattended

When working without someone to ask, make the call and **write down why**.

Every autonomous decision goes in a comment on the issue before it is closed:
the option taken, the options rejected, and what would change the answer. The
reasoning has to outlive the session it happened in, and an issue closed with
`Fixed in #12` teaches nobody anything.

State the judgement calls in the merge request too, especially the ones a
reviewer would otherwise have to reverse-engineer from the diff.

### Documentation-only changes

A change touching only documentation may skip the build, test and release
pipeline. Documentation cannot alter what is built, and running the suite on one
only delays the merge.

**[PROJECT-SPECIFIC]** — define what counts, e.g. `docs/**`, `*.md`, `LICENSE`.
Everything else, including CI configuration and packaging, is code.

**Two traps, if you implement this.** Both come from the same fact: a job
*skipped* by an `if:` condition **satisfies** a required status check.

- Do not use `paths-ignore`. A workflow that never runs leaves required checks
  pending forever and the merge request permanently unmergeable, with no
  override if admin enforcement is on.
- Gate on a **definite** answer only. A condition like `== 'true'` is implicitly
  ANDed with `success()`, so a *failed* classification job skips every gated job
  — and skipped satisfies the requirement, letting a code change merge with
  nothing having run. Use `!cancelled() && ... != 'false'` so only an explicit
  negative skips anything.

The classifier itself must fail open: anything ambiguous runs the full pipeline.
The cost of being wrong is one wasted run, never an unchecked merge.

### What never to do

This is the complete list.

- **Never commit directly to `<INTEGRATION_BRANCH>` or `<RELEASE_BRANCH>`,**
  including fast-forward merges.
- **Never open a merge request before the Definition of Done is met.**
- **Never make `<SHARED_RESOURCE>` part of a merge request's checks,** and never
  point a feature branch at anything anyone else uses.
- **Never keep anything you care about on `<VALIDATION_TARGET>`.** It tracks
  `<INTEGRATION_BRANCH>` automatically and its test data is deleted on every run.
- **Never review your own change.** If you wrote it, a different agent reviews it.
- **Never merge an unreviewed or change-requested merge request.**
- **Never squash a merge request.**
- **Never create a version tag by hand.** CI derives the next version from the
  highest existing tag; a hand-made tag silently reassigns everything after it.
- **Never delete or move a published tag.** The artifact is already published
  against it; the tag and the artifact would disagree.
- **Never tag or release a commit that did not come through an approved merge
  request.**

---

## Versioning: RELEASE.MAJOR.MINOR

Three places, each moving on a different event:

| Event | Effect | Example |
| --- | --- | --- |
| Merge into `<INTEGRATION_BRANCH>` | MINOR increments | `1.2.3` → `1.2.4` |
| Merge into `<RELEASE_BRANCH>` | MAJOR increments, MINOR resets | `1.2.4` → `1.3.0` |
| A release package is cut | RELEASE increments, the rest reset | `1.3.0` → `2.0.0` |

**This is not SemVer, and the middle place in particular does not mean what
SemVer means by MAJOR.** A promotion moves it whatever the change contained.
Communicate breaking changes in the release notes, because no place in the
version number signals them.

**RELEASE moves only when a release package is made**, which is a decision rather
than a side effect of a merge. That is the whole reason it is a separate place:
promoting readies work, and cutting a release ships it, and those are not the
same act.

### Who assigns versions

CI does, never a human. The version is derived from the highest existing tag, so
**the tags are the source of truth** — no VERSION file to drift, and no commit
written back to a branch.

Keep the arithmetic in **one script with its own tests**, not inline in a
workflow. Two workflows need the same rules, and two copies eventually disagree
about what "next" means.

---

## Gitflow procedures

```bash
# start a loop
git checkout <INTEGRATION_BRANCH> && git pull
git checkout -b feature/my-change

# finish it: Definition of Done first, then a reviewed merge request
gh pr create --base <INTEGRATION_BRANCH> --fill
/code-review <pr-number>          # a DIFFERENT agent
# merge only on APPROVE — the merge cuts the next MINOR
```

### Promoting <INTEGRATION_BRANCH> to <RELEASE_BRANCH>

A promotion **is a release; it gets *more* scrutiny than a feature, not less.**

```bash
gh pr create --base <RELEASE_BRANCH> --head <INTEGRATION_BRANCH> \
  --title "Release: promote <INTEGRATION_BRANCH> to <RELEASE_BRANCH>" --fill
/code-review <pr-number>
# merge only on APPROVE
```

A promotion promotes **reviewed work**; it does not introduce new work. Findings
raised against it are fixed on `<INTEGRATION_BRANCH>` through the normal loop,
and the promotion picks them up.

Post-merge validation usually runs on `<INTEGRATION_BRANCH>` only, so a promotion
gets no validation run of its own — **the gate is the validation run on the
tree being promoted.** Point at a specific run, and re-point it if the branch
moves. Do not restate that claim from memory after fixes land.

Then bring `<RELEASE_BRANCH>` back so the branches do not diverge:

```bash
git checkout -b chore/back-merge <INTEGRATION_BRANCH>
git merge --no-ff <RELEASE_BRANCH>
gh pr create --base <INTEGRATION_BRANCH> --title "Back-merge" --fill
```

`<INTEGRATION_BRANCH>` is protected and rejects direct pushes. It also matters
for a second reason: a hotfix reaches it **only** through this back-merge, and
pushing directly would carry hotfix code in with no review.

### Hotfixes

A production fix still goes through a branch, but branches from
`<RELEASE_BRANCH>`, and reaches `<INTEGRATION_BRANCH>` through the back-merge
above — never by cherry-pick, which leaves the two branches claiming different
histories for the same fix.

---

## [PROJECT-SPECIFIC] Everything below

The sections above are the process and transfer unchanged. What follows is
particular to each project, and each needs writing from scratch:

- **Local development setup** — prerequisites, how to get a working environment.
- **Architecture and code patterns** — the shapes contributors should follow,
  and the ones they should not.
- **Testing strategy** — the layers, what each proves, and crucially **which
  need `<SHARED_RESOURCE>` and which do not**. That boundary is what Gate 2 and
  Gate 5 are built on, so it has to be explicit.
- **Build tooling** — the commands, and which act on shared state.
- **CI/CD pipeline** — the jobs, their order, and the decisions in them that
  look wrong until you know why.

On that last point: record the reasoning for anything non-obvious *next to it*.
The most valuable comments in a pipeline are the ones explaining why the obvious
approach was rejected — they are what stops the next person reintroducing a bug
that was already fixed once.
