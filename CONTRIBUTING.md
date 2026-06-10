# 🤝 Contributing to dansal

Thank you for your interest in contributing to dansal! We welcome contributions from everyone.

## 📋 Table of Contents

- [Code of Conduct](#-code-of-conduct)
- [How Can I Contribute?](#-how-can-i-contribute)
- [Getting Started](#-getting-started)
- [Development Workflow](#-development-workflow)
- [Submitting Changes](#-submitting-changes)
- [Review Process](#-review-process)
- [Community Guidelines](#-community-guidelines)
- [Recognition](#-recognition)

## 🤝 Code of Conduct

By participating in this project, you agree to abide by our [Code of Conduct](CODE_OF_CONDUCT.md). Please read it to understand the expected behavior.

## 💡 How Can I Contribute?

### 🐛 Bug Reports

Found a bug? Please [open an issue](https://github.com/ademant/dansal/issues) with:
- Clear title and description
- Steps to reproduce
- Expected vs actual behavior
- Screenshots if applicable
- Environment details (OS, browser, dansal version)

### 🚀 Feature Requests

Have an idea? [Open a discussion](https://github.com/ademant/dansal/discussions) first to:
- Explain the use case
- Describe the proposed solution
- Get community feedback
- Avoid duplicate work

### 📝 Documentation

Help improve our docs by:
- Fixing typos and errors
- Adding missing information
- Improving examples
- Translating to other languages
- Creating tutorials

### 💻 Code Contributions

We welcome code contributions for:
- Bug fixes
- New features
- Performance improvements
- Test coverage
- Refactoring

### 🌍 Translations

Help make dansal accessible worldwide by:
- Adding new language support
- Improving existing translations
- Reviewing translation quality

### 🎨 Design & UX

Contribute to user experience by:
- Improving UI components
- Creating wireframes and mockups
- Conducting user research
- Improving accessibility

## 🚀 Getting Started

### Prerequisites

- [Go 1.22+](https://go.dev/dl/)
- [Git](https://git-scm.com/)
- [Node.js 18+](https://nodejs.org/) (for web frontend)
- Basic understanding of Git and GitHub

### Setup

```bash
# Fork the repository on GitHub
# Clone your fork
git clone https://github.com/your-username/dansal.git
cd dansal

# Set up upstream remote
git remote add upstream https://github.com/ademant/dansal.git

# Install dependencies
make setup

# Build and run
make dev
```

### Development Environment

- **API Server**: `http://localhost:8000`
- **Web Frontend**: `http://localhost:3000`
- **Admin Interface**: `http://localhost:3001`

## 🔧 Development Workflow

### 1. Find an Issue

- Look for [good first issues](https://github.com/ademant/dansal/labels/good%20first%20issue)
- Check [help wanted](https://github.com/ademant/dansal/labels/help%20wanted) labels
- Ask in discussions if you need ideas

### 2. Create a Branch

```bash
# Update your fork
git fetch upstream
git checkout main
git merge upstream/main

# Create feature branch
git checkout -b feat/your-feature-name
# or for bugs:
git checkout -b fix/bug-description
```

**Branch naming conventions:**
- `feat/*` - New features
- `fix/*` - Bug fixes  
- `docs/*` - Documentation
- `refactor/*` - Code refactoring
- `test/*` - Testing improvements
- `chore/*` - Maintenance tasks

### 3. Make Changes

- Follow our [coding guidelines](DEVELOPER_GUIDE.md#coding-guidelines)
- Write tests for new functionality
- Update documentation if needed
- Keep changes focused and minimal

### 4. Commit Changes

```bash
# Stage changes
git add .

# Commit with clear message
git commit -m "feat: add new event filter option"
```

Follow [Conventional Commits](https://www.conventionalcommits.org/):
- `feat: ` - New features
- `fix: ` - Bug fixes
- `docs: ` - Documentation changes
- `style: ` - Code style changes
- `refactor: ` - Code refactoring
- `test: ` - Adding or updating tests
- `chore: ` - Maintenance tasks

### 5. Push Changes

```bash
git push origin your-branch-name
```

## 📤 Submitting Changes

### Creating a Pull Request

1. Go to [GitHub Pull Requests](https://github.com/ademant/dansal/pulls)
2. Click "New Pull Request"
3. Select your branch
4. Fill out the PR template
5. Submit for review

### Pull Request Template

```markdown
## Description

Clear description of changes and purpose.

## Related Issues

Fixes #123

## Changes Made

- Added new endpoint `/api/v1/events/filter`
- Updated event service to support filtering
- Added unit tests for new functionality

## Testing

- Manual testing performed
- Unit tests added
- Integration tests pass

## Checklist

- [x] Code follows project style guidelines
- [x] Tests added/updated
- [x] Documentation updated
- [x] Changes are minimal and focused
- [x] No breaking changes
```

### PR Requirements

- Clear title and description
- Reference related issues
- Follow coding standards
- Include tests
- Update documentation
- Pass CI checks

## 🔍 Review Process

### What to Expect

1. **Automated Checks**: CI runs tests, linting, and builds
2. **Initial Review**: Maintainer triage (1-2 days)
3. **Community Review**: Feedback from contributors
4. **Iteration**: Address feedback and update PR
5. **Approval**: At least one maintainer approval
6. **Merge**: Your code becomes part of dansal! 🎉

### Review Guidelines

- Be responsive to feedback
- Address all comments
- Update PR description if scope changes
- Be patient - reviewers are volunteers
- Thank reviewers for their time

### Common Review Feedback

- **Style Issues**: Not following coding guidelines
- **Test Coverage**: Missing or inadequate tests
- **Documentation**: Missing or incomplete docs
- **Scope Creep**: PR doing too much
- **Performance**: Inefficient implementations
- **Security**: Potential vulnerabilities

## 🤗 Community Guidelines

### Communication

- Be respectful and professional
- Use inclusive language
- Be patient with newcomers
- Give constructive feedback
- Say "thank you" often

### Getting Help

- **GitHub Discussions**: General questions
- **GitHub Issues**: Bug reports and feature requests
- **Slack/Discord**: Real-time chat (link in README)
- **Weekly Meetings**: Community sync (check calendar)

### Mentorship

We offer mentorship for new contributors:
- Pair programming sessions
- Code review guidance
- Architecture explanations
- Career advice

Ask in discussions if you'd like a mentor!

## 🏆 Recognition

### Contributor Benefits

- **GitHub Contributor Badge**: On your profile
- **Release Notes**: Your name in release announcements
- **Swag**: Stickers, t-shirts for significant contributions
- **Leadership**: Opportunity to become maintainer
- **Networking**: Connect with dance tech community

### Contributor Levels

| Level | Requirements | Benefits |
|---|---|---|
| **New Contributor** | 1 merged PR | GitHub badge, thanks in chat |
| **Regular Contributor** | 5 merged PRs | Release notes mention, swag |
| **Core Contributor** | 10+ merged PRs | Maintainer meetings invite |
| **Maintainer** | Consistent leadership | Commit access, decision making |

## 📚 Additional Resources

- **[Developer Guide](DEVELOPER_GUIDE.md)** - Technical details
- **[API Documentation](API.md)** - API reference
- **[Architecture](DEVELOPER_GUIDE.md#architecture)** - System design
- **[Coding Guidelines](DEVELOPER_GUIDE.md#coding-guidelines)** - Style rules

## 🤔 Still Have Questions?

- **GitHub Discussions**: [Ask the community](https://github.com/ademant/dansal/discussions)
- **Email**: contributors@dansal.example.com
- **Weekly Office Hours**: Check our calendar

---

**Thank you for contributing!** 🎉

Your work helps dance communities around the world connect and thrive. Every contribution, no matter how small, makes a difference.

"Alone we can do so little; together we can do so much." — Helen Keller