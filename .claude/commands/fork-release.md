---
name: fork-release
description: Create a fork release by merging feature branches, tagging, pushing, and triggering the GitHub Actions release workflow. Use when asked to "release the fork", "tag a release", or "cut a fork release".
---

# Fork Release

Create and push a release tag that triggers the fork-release GitHub Actions workflow.

## Important constraints

- NEVER modify or delete the `feat/*` branches — they are the base for upstream contributions
- NEVER merge into `master` — it tracks upstream `cue-lang/cue`
- The `release/*` branch is a disposable merge target that combines both features for the binary build

## Prerequisites

- Both feature branches (`feat/koala-xml-encoder`, `feat/struct-filter-by-attr`) must be up to date
- The GitHub Actions workflow `.github/workflows/fork-release.yaml` must exist on the target branch
- The `gh` CLI must be authenticated with push access to the fork remote

## Steps

### 1. Determine the release version

Ask the user for the tag if not provided as an argument. The convention is:

```
v<upstream-version>-fork.<increment>
```

For example: `v0.16.0-fork.1`. The upstream version should match the merge base on master that both feature branches share.

If the user doesn't specify, determine it automatically:
- Find the merge base: `git merge-base feat/koala-xml-encoder master`
- Get the latest upstream tag at or before that commit: `git describe --tags --abbrev=0 <merge-base>`
- Check existing fork tags: `git tag -l 'v*-fork*' | sort -V | tail -1`
- Increment accordingly

### 2. Create the release branch

```bash
git checkout -b release/<tag> feat/koala-xml-encoder
git merge feat/struct-filter-by-attr --no-edit
```

If conflicts arise, stop and ask the user to resolve them before continuing.

### 3. Validate the build

```bash
go build ./cmd/cue
```

Run the fork-specific tests:

```bash
go test ./pkg/struct/... ./encoding/xml/...
```

If either fails, stop and report the error.

### 4. Tag and push

```bash
git tag <tag>
git push origin release/<tag> <tag>
```

### 5. Monitor the workflow

After pushing, check that the GitHub Actions workflow was triggered:

```bash
gh run list --workflow=fork-release.yaml --limit=1
```

Report the run URL to the user so they can monitor it.

### 6. Summary

Report:
- The tag that was created
- The release branch name
- The GitHub Actions run URL
- Remind: the release will appear as a prerelease on the GitHub Releases page once the workflow completes
