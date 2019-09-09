# Design Proposal #006: Build Improvements

## Current State

At the start of the project we've picked CircleCI as it appears to be a default choice at Weaveworks.

The original solution involved:
- running `docker build` to create a build image
- use build image to run tests produce final image

Publishing build and final images hasn't been automated, but some users requested it.

When we switched [to modules](https://github.com/weaveworks/eksctl/pull/917), we have increased the
compexity of how build image worked. It's hard for a new person to understand it, e.g. for a new
contributor.

We currently build images in CI and run tests as part of that, we should only run tests as a primary
task.

Downside of current setup:

- CircleCI requires delegation of access to GitHub
  - for releases we use a token
  - we don't have a way to automate workflows that require pushing to the repo
- CircleCI cache 
  - it is not very fast, it turns out pulling image with cached dependencies is as fast
  - it is very specifig to CircleCI, not portable
  - we do not have a way to controll it
- the task of building images should not be mixed with the task of running test
- images are not being automatically pushed to a registry
- update of build image is manual

## Proposed Improvements

This proposal [#1200](https://github.com/weaveworks/eksctl/pull/1200) aims to streamline the following
aspects:

- automate build image versioning through deterministic git object hashes (see `Makefile.docker`)
- initial cleanup of build scripts
    - separate makefiles
    - single static `Dockerfile`
- stop using CircleCI cache and pull build image instead
- switch to Docker executor using our build image in CircleCI
    - stop having to dowload Go toolchain
    - clear separation of concerns - creating build image vs running tests
- enable GitHub Actions as an experiment, so we can evaluate it against CircleCI
- based on initial tests (in GitHub Actions) - 40%-50% speed-up in on-commit execution (lint+test+build)

## Further Improvements

- stop using `docker run` and `docker commit`, use another `Dockerfile` instead of `eksctl-image-builder.sh`
- push images to a registry (GitHub offering could be a good fit, otherwise condider ECR)
- automate other workflows with bots and GitHub Actions (e.g. cherry-picking [#1284](https://github.com/weaveworks/eksctl/issues/1284), AMI updates [#314](https://github.com/weaveworks/eksctl/issues/314))
- automate intrgration tests
- allow running integration tests on PRs from contributors upon PR approval
