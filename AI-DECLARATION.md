---
version: "0.1.2"
level: auto
processes:
  design: pair
  implementation: copilot
  testing: auto
  documentation: pair
  review: assist
  deployment: auto
---

This format is based on [AI-DECLARATION.md](https://ai-declaration.md/en/0.1.2/).

Long-form context — approach, human accountability, provenance, and the rules for
AI-assisted contributions — is in [AI_DISCLOSURE.md](AI_DISCLOSURE.md).

## Notes

- `implementation` is declared `copilot` because the interactive sessions that produce most
  changes prompt the maintainer for permission and clarification throughout. The `draft-tile`
  workflow is the exception and is genuinely `auto`: an app request that clears triage is
  handed to an agent that researches the upstream, drafts a digest-pinned tile, runs the same
  gates a human would, and opens a pull request with no human in the loop. It proposes; the
  gates and the maintainer dispose, and the maintainer performs every merge.
