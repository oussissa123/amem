# Contributing to AMem

Thank you for your interest in contributing to AMem, an agent-memory system designed to provide persistent, structured and searchable memory across AI agents and tools.

AMem welcomes bug reports, documentation improvements, tests, integrations, performance work and code contributions.

## 1. Before contributing

1. Read the `README.md` and the relevant architecture documentation.
2. Search existing issues and merge requests before opening a new one.
3. For significant changes, open an issue or discussion first so the proposed design can be reviewed.
4. Do not include credentials, API keys, personal data, confidential information or customer data in issues, commits, tests or examples.
5. Check that you have the right to submit every part of your contribution.

## 2. Contributor License Agreement

Before a contribution can be merged, each contributor must accept the project's `CLA.md`.

The CLA allows the contributor to retain copyright ownership while granting the AMem Project Owner the rights needed to distribute the contribution as part of AMem, including under separate commercial licenses.

If you are contributing on behalf of an employer or another organisation, obtain the necessary authorisation and follow the organisation-contribution section of the CLA.

A merge request may be reviewed before CLA acceptance, but it must not be merged until the required acceptance has been recorded.

## 3. Licensing of contributions

By submitting a contribution for inclusion in AMem, you agree that it will be distributed as part of the project under the project's applicable license and that the CLA applies.

The current project license is the **AMem Community License 1.0**. It is source-available and includes a restriction on selling AMem or providing paid services whose value derives entirely or substantially from AMem's functionality, unless separately authorised in writing by the copyright holder.

Do not add dependencies, copied code, datasets, fonts, images or other third-party materials unless their licenses are compatible with the project's distribution model. Identify all such materials and include required notices.

## 4. What you can contribute

Examples include:

- Bug fixes and regression tests.
- New memory storage, retrieval, indexing or consolidation capabilities.
- Agent, model, tool and protocol integrations.
- Documentation, examples and tutorials.
- Performance, reliability, observability and security improvements.
- Deployment examples and developer tooling.

For large changes, discuss the approach before investing significant implementation effort.

## 5. Development workflow

1. Fork the repository.
2. Create a focused branch, for example `feature/vector-retrieval` or `fix/memory-expiration`.
3. Keep changes small and logically separated.
4. Add or update tests for behaviour changes.
5. Run the relevant test suite, formatting and static checks.
6. Update documentation and examples where needed.
7. Open a GitLab merge request against the appropriate target branch.

## 6. Merge request checklist

In your merge request, explain:

- What problem is being solved?
- Why is this approach appropriate?
- What has changed?
- How was it tested?
- Are there migration, compatibility, performance or security implications?
- Are documentation or configuration updates required?
- Have you identified new third-party dependencies and their licenses?
- Have you accepted the CLA?

Do not mark a change as complete if tests have not been run; state clearly which checks were not run and why.

## 7. Code and documentation expectations

- Prefer clear, maintainable and well-tested code.
- Keep public APIs and configuration changes documented.
- Avoid unnecessary breaking changes.
- Do not commit generated files unless the project explicitly requires them.
- Use descriptive commit and merge request titles.
- Include migration notes for changes that affect stored memory, schemas or persisted data.

Project maintainers may request changes, decline a contribution or ask for a different implementation.

## 8. Security issues

Do not publish exploitable vulnerability details, secrets or proof-of-concept attacks in public issues.

Report security issues privately using the contact or reporting process described in `SECURITY.md` (to be added by the maintainers). Until that process is published, contact the repository owner through a private channel.

## 9. Community conduct

Be respectful, constructive and inclusive. Focus feedback on the work, not the person. Harassment, discrimination, threats, doxxing and abusive behaviour are not acceptable.

## 10. Questions

For design questions or proposed features, open a GitLab issue or discussion with enough context for maintainers and the community to evaluate the proposal.
