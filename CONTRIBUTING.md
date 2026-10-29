# Contributing

Thanks for helping improve jobs-dashboard. Contributions of bug reports,
documentation, tests, and code are welcome.

## Before you start

- Check existing issues and pull requests for related work.
- For a substantial change, open an issue first to discuss the approach.
- For security issues, follow [SECURITY.md](./SECURITY.md) instead of opening a
  public issue.

## Development setup

Install Go 1.24 or newer. This application uses Kubernetes in-cluster
configuration at runtime; unit tests use the Kubernetes fake client and do not
need a live cluster.

From the root of your checkout:

```sh
make check
```

## Making a change

1. Keep changes focused and follow existing Go and HTML conventions.
2. Add or update tests for behavior changes.
3. Update the README or other user-facing docs when configuration or behavior
   changes.
4. Run `make check` before submitting.
5. Include a clear summary and testing notes in the pull request.

Do not add credentials, tokens, cookie secrets, or other sensitive values to the
repository, examples, logs, or test fixtures.

## Pull requests

- Keep pull requests focused and reasonably sized.
- Explain the problem and the solution.
- Link related issues when applicable.
- Call out any deployment, configuration, or security implications.
- Make sure CI passes and respond constructively to review feedback.

By submitting a contribution, you agree that it may be distributed under the
project's [MIT License](./LICENSE).
