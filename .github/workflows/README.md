# Workflow rules

This repo's CI reads things strangers wrote. Issue bodies, upstream READMEs, registry
metadata, and eventually community pull requests. Four rules follow, and `zizmor` enforces
them as a required check rather than trusting anyone to remember.

## 1. Untrusted text reaches a script as data, never as source

```yaml
# NO — this is remote code execution
run: echo "${{ github.event.issue.body }}"

# YES
env:
  BODY: ${{ github.event.issue.body }}
run: echo "$BODY"
```

A `${{ }}` expression is pasted into the script *before* the shell runs, so a body
containing `$(...)` or a backtick executes on the runner. This applies to anything a
stranger influences: issue bodies and titles, PR titles, comment text, **and branch names**
— `github.base_ref` looks innocent and is not.

This is not hypothetical here. The first `zizmor` run found exactly this in
`lint.yml`, in the same repo whose intake workflow carries three paragraphs explaining why
never to do it. Discipline in comments does not hold. A gate does.

## 2. Permissions start empty

`permissions: {}` at workflow level, then grant per job, narrowly. A job that comments gets
`issues: write` and nothing else — not `contents: write`, not `id-token`, not `packages`.

Ask what a total compromise of the job yields. If the answer is worse than "an unwanted
comment" or "an unwanted pull request", the grant is too wide.

## 3. Actions and tools are pinned to a hash

```yaml
uses: actions/checkout@11d5960a326750d5838078e36cf38b85af677262 # v4
```

A tag is mutable: whoever controls the action can repoint `v4` at anything, and CI runs it
with whatever permissions the job holds. The trailing comment is what keeps it readable, and
Renovate updates these automatically — so pinning costs nothing ongoing.

Pin the **tool** too, not just the action that installs it — and pin everything the install
runs. Trivy is downloaded from its GitHub release asset and checked against a sha256 held in
the workflow, so a scan result cannot depend on the day it ran: the supply-chain gate compares
two scans expecting the same engine on both sides. We used to install it with
`aquasecurity/setup-trivy`, pinned by hash, but that action fetches `contrib/install.sh` from
Trivy's default branch at run time. When upstream pointed the script at a download host the
runners cannot resolve, every PR failed — a hash-pinned action is not pinned if it runs code
it fetched unpinned. Renovate does not track this pin; bump the version and the sha together,
taking the sha from the release's `trivy_<version>_checksums.txt`.

This is the same rule the catalog applies to container images. We digest-pin every tile;
pinning actions by mutable tag while doing so was an inconsistency, not a considered
exception.

## 4. Checkout does not leave the token behind

`persist-credentials: false` unless the job genuinely needs to push. Otherwise the token
stays in `.git/config` where any later step — including one an agent was talked into
running — can read it.

## Running the audit locally

```
pip install zizmor==1.29.0
zizmor --format plain .github/workflows/
```

## What the rules do not cover

They constrain the *runner*. They do nothing about an agent being **persuaded** by what it
reads — that is handled by keeping the agent's output to a pull request, gating that PR on
deterministic checks the agent cannot edit, and never putting signing material anywhere it
can reach. See `draft-tile.yml`.
