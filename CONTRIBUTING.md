# Contributing to Builder

Thank you for your interest in contributing to Builder! This document provides guidelines and information for contributors.

## Code of Conduct

By participating in this project, you agree to maintain a respectful and inclusive environment for everyone.

## How to Contribute

### Reporting Bugs

Before submitting a bug report:

1. Check existing [issues](https://github.com/MobAI-App/ios-builder/issues) to avoid duplicates
2. Use the bug report template when creating a new issue
3. Include:
   - Builder version (`builder --version`)
   - Operating system and version
   - Steps to reproduce
   - Expected vs actual behavior
   - Relevant logs (with sensitive info redacted)

### Suggesting Features

Feature requests are welcome! Please:

1. Check existing issues for similar suggestions
2. Use the feature request template
3. Describe the use case and expected behavior
4. Explain why this would benefit other users

### Pull Requests

1. **Fork** the repository
2. **Create a branch** from `main`:
   ```bash
   git checkout -b feature/your-feature-name
   ```
3. **Make your changes** following the coding standards below
4. **Add tests** for new functionality
5. **Run tests** to ensure nothing is broken:
   ```bash
   go test ./...
   ```
6. **Commit** with clear, descriptive messages
7. **Push** to your fork and create a Pull Request

## Development Setup

### Prerequisites

- Go 1.22 or later
- Git

### Building

```bash
git clone https://github.com/MobAI-App/ios-builder.git
cd ios-builder
go build -o builder ./cmd/builder
```

### Running Tests

```bash
# Run all tests
go test ./...

# Run tests with verbose output
go test -v ./...

# Run specific package tests
go test ./internal/config/...

# Run with race detection
go test -race ./...
```

### Linting

```bash
go fmt ./...
go vet ./...

# If you have golangci-lint installed
golangci-lint run
```

## Coding Standards

### Go Style

- Follow [Effective Go](https://go.dev/doc/effective_go)
- Use `gofmt` for formatting
- Keep functions focused and small
- Add comments for exported functions and types

### Project Structure

```
cmd/builder/         # CLI entrypoint
internal/
  auth/              # GitHub OAuth authentication
  build/             # Build coordination and progress
  config/            # Configuration management
  dev/               # Development session (Flutter hot reload)
  github/            # GitHub API client
  mobai/             # MobAI API client
  workflow/          # GitHub Actions templates
```

### Error Handling

- Wrap errors with context using `fmt.Errorf("context: %w", err)`
- Use sentinel errors for expected conditions (e.g., `ErrConfigNotFound`)
- Provide clear error messages for users

### Testing

- Write table-driven tests where applicable
- Test both success and error paths
- Use `t.TempDir()` for file system tests
- Mock external services (GitHub, MobAI)

### Commits

- Use clear, descriptive commit messages
- Reference issues where applicable: `Fix #123`
- Keep commits focused on a single change

## Architecture Decisions

### Security First

- Never persist source code longer than necessary
- Use short-lived signed URLs (max 10 minutes)
- Verify checksums on all file transfers
- Store credentials in OS keychain only

### Cross-Platform

- Use build tags for platform-specific code
- Test on Windows, macOS, and Linux when possible
- Avoid shell-specific assumptions

### User Experience

- Provide clear progress feedback
- Use colors and emojis appropriately
- Give actionable error messages with hints

## Getting Help

- Open a [Discussion](https://github.com/MobAI-App/ios-builder/discussions) for questions
- Join the community chat (if available)
- Check the README and existing issues

## License

By contributing, you agree that your contributions will be licensed under the MIT License.
