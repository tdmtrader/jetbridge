---
name: tidy-review
description: Adversarially review one tidy branch against its category card. Usage: /tidy-review <branch>
disable-model-invocation: true
---

BRANCH is `$ARGUMENTS`; CARD is `.claude/skills/tidy/categories/<category>.md`
where `<category>` is the middle segment of the branch name. Follow
`../tidy/REVIEW.md` exactly and print its verdict line first.
