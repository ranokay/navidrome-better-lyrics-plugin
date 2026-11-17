# PR-Agent commands

PR-Agent is an optional reviewer. On an open, non-draft pull request, a user
with current repository write, maintain, or admin access can post:

- `/review` to update the persistent review.
- `/improve` to publish a suggestion summary and applicable inline suggestions.
- `/ask QUESTION` to ask about the proposed change.

Post one command per comment. Wait for its workflow to finish before requesting
another command. The authorizer requests cancellation of lower-numbered active
or queued runs of this workflow for the same PR. Run numbers order workflow
creation, not event timestamps; delayed event delivery can change that order.
Before starting model work, the authorizer confirms that selected older runs
have completed. If they remain active after its bounded confirmation checks,
the new review fails without starting inference. Cancellation selections and
observed run states appear in the authorization job logs.
Command flags and runtime configuration overrides are rejected. Unrelated
comments, bot requests, drafts, and closed PRs do not invoke the model.

The same write, maintain, or admin permission requirement applies to commands
on fork PRs. The workflow reads PR content without executing its code.
Automatic lifecycle reviews cover same-repository PRs only.

Inspect responses and the workflow logs before claiming coverage. Missing keys
skip, and provider errors can leave a failed optional review. Apply and test
suggestions yourself; PR-Agent cannot push, approve, merge, or resolve human
threads. Local reviews and repository checks remain required.
