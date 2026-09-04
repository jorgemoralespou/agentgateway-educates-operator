# agentgateway-educates-operator

## Agent skills

### Issue tracker

Issues live as markdown files under `.scratch/<feature>/` in this repo. See `docs/agents/issue-tracker.md`.

### Triage labels

The five canonical triage roles, each label string equal to its name. See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: `CONTEXT.md` and `docs/adr/` at the repo root. See `docs/agents/domain.md`.

## Working norms

How I expect to collaborate:

- **Ask before destructive operations.** `rm`, `git push`, `git reset --hard`,
  deleting files outside `/tmp`, confirm first.
- **Don't push to remote.** Commits are fine when asked; pushes are mine.
- **Small, focused commits with clear messages.** Conventional Commits style
  preferred (`feat(operator): ...`, `fix(crd): ...`).
- **No `Co-Authored-By: Claude` trailers in commit messages.** Commits are
  authored by me; collaboration with you is a working detail, not a
  history artefact. Plain commit message body, no attribution footer.
- **Verify Helm chart values before suggesting them.** `helm show values
  <chart>` is the source of truth, not your training data.
- **Verify Kubernetes API semantics before suggesting them.** Especially
  finalizers, namespace deletion, controller-runtime cache behavior. When in
  doubt, ask me to verify, or read the source.
- **Prefer asking over assuming.** A clarifying question costs me 10 seconds.
  Wrong code costs me 30 minutes of debugging plus the time to undo it.
- **When uncertain about a design decision, stop and ask.** Don't pick a
  direction and run with it.
- **Always draft a plan or check-list when working with complex tasks.** If you
  envision the task is going to be long, either plan properly if needed, or at
  least create a task-list to track progress.
- **NEVER USE** emdashes on any documentation or comment.
- If there's new user facing functionality, **DOCUMENT IT**