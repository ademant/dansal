# 📚 dansal Documentation

Complete documentation for the dansal dance event management system.

## 🗂️ Documentation Structure

```
📁 dansal/
├── README.md                    # Main entry point - quick overview
├── VISITOR_GUIDE.md             # For dance enthusiasts finding events
├── USER_GUIDE.md                # For event organizers and users
├── ADMIN_GUIDE.md               # For system administrators
├── DEVELOPER_GUIDE.md           # For developers and contributors
├── API.md                      # Comprehensive API reference
├── CONTRIBUTING.md             # Contribution guidelines
├── CODE_OF_CONDUCT.md          # Community behavior standards
├── DOCKER.md                   # Docker deployment guide
└── dansal_admin.md             # CLI administration tool reference
```

## 🎯 Quick Start Guide

### 1. **Choose Your Role**

- **🎭 Visitor**: Just want to find dance events? → **[Visitor Guide](VISITOR_GUIDE.md)**
- **👥 Organizer**: Need to create/manage events? → **[User Guide](USER_GUIDE.md)**
- **🛠️ Admin**: Setting up/maintaining a dansal instance? → **[Admin Guide](ADMIN_GUIDE.md)**
- **👨‍💻 Developer**: Building integrations or contributing? → **[Developer Guide](DEVELOPER_GUIDE.md)**

### 2. **Get Running Quickly**

```bash
# Quick local setup
git clone https://github.com/ademant/dansal.git
cd dansal
go run .
```

Then open `http://localhost:8000` in your browser.

### 3. **Explore Features**

- **Interactive Map**: Browse events geographically
- **Event Creation**: Add your dance events
- **Venue Management**: Maintain venue profiles
- **Community Board**: Connect with other dancers

## 📖 Documentation Overview

### For Visitors & Dance Enthusiasts

**[VISITOR_GUIDE.md](VISITOR_GUIDE.md)** covers:
- Browsing events on the interactive map
- Understanding event details and status
- Using the community bulletin board
- Multi-language support
- Mobile experience and gestures
- Saving favorite events

### For Event Organizers & Users

**[USER_GUIDE.md](USER_GUIDE.md)** covers:
- Account management and roles
- Creating and managing events
- Venue and organization management
- Musician profiles and MusicBrainz integration
- Advanced features (templates, bulk operations)
- User roles and permissions

### For System Administrators

**[ADMIN_GUIDE.md](ADMIN_GUIDE.md)** covers:
- System requirements and installation
- Configuration and environment variables
- Deployment options (binary, Docker, systemd)
- User management and security
- System maintenance and monitoring
- Backup, recovery, and troubleshooting
- Scaling and performance tuning

### For Developers

**[DEVELOPER_GUIDE.md](DEVELOPER_GUIDE.md)** covers:
- Project structure and architecture
- API reference and authentication
- Data models and database schema
- Development setup and workflow
- Coding guidelines and conventions
- Testing strategies
- Building, packaging, and releasing
- Contribution process

**[API.md](API.md)** provides:
- Complete REST API endpoint reference
- Authentication methods
- Request/response examples
- Error codes and handling
- WebSocket endpoints

### Contribution & Community

**[CONTRIBUTING.md](CONTRIBUTING.md)** explains:
- How to contribute (code, docs, translations, etc.)
- Development workflow
- Pull request process
- Community guidelines
- Recognition and benefits

**[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)** outlines:
- Expected behavior standards
- Unacceptable behavior
- Reporting procedures
- Enforcement policies

## 🔧 Technical Reference

### CLI Administration

**[dansal_admin.md](dansal_admin.md)** documents:
- Command-line administration tool
- User management commands
- Database operations
- Configuration management
- Backup and restore procedures

### Docker Deployment

**[DOCKER.md](DOCKER.md)** provides:
- Containerized deployment instructions
- Docker Compose configuration
- Environment variables
- Volume and network setup
- Production considerations

## 📈 Learning Path

### New Users
1. Start with **[README.md](README.md)** for overview
2. Read **[VISITOR_GUIDE.md](VISITOR_GUIDE.md)** to find events
3. Explore **[USER_GUIDE.md](USER_GUIDE.md)** if you want to organize events

### System Administrators
1. **[ADMIN_GUIDE.md](ADMIN_GUIDE.md)** for installation and setup
2. **[DOCKER.md](DOCKER.md)** for container deployment
3. **[dansal_admin.md](dansal_admin.md)** for CLI administration

### Developers
1. **[DEVELOPER_GUIDE.md](DEVELOPER_GUIDE.md)** for architecture and setup
2. **[API.md](API.md)** for API integration
3. **[CONTRIBUTING.md](CONTRIBUTING.md)** to start contributing

## 🤝 Getting Help

- **Bug Reports**: [GitHub Issues](https://github.com/ademant/dansal/issues)
- **Feature Requests**: [GitHub Discussions](https://github.com/ademant/dansal/discussions)
- **General Questions**: [GitHub Discussions](https://github.com/ademant/dansal/discussions)
- **Security Issues**: security@dansal.example.com

## 📋 Documentation Conventions

### Formatting

- **Markdown**: Standard GitHub Flavored Markdown
- **Code Blocks**: Syntax highlighting for Go, JSON, YAML, bash
- **Tables**: For comparative information
- **Lists**: For step-by-step instructions
- **Emoji**: 🎉 For visual emphasis (sparingly)

### Structure

Each guide follows this structure:
1. **Title with emoji**
2. **Table of Contents**
3. **Introduction**
4. **Main Content** (organized by topic)
5. **Additional Resources**
6. **Getting Help** section

### Cross-Referencing

Documents link to each other where relevant:
- `→ **[Guide Name](FILE.md)**` for navigation
- Relative links for GitHub rendering
- Contextual links within content

## 🌍 Translations

Help wanted! We welcome translations of our documentation. See **[CONTRIBUTING.md](CONTRIBUTING.md)** for how to contribute translations.

## 📝 Documentation Roadmap

### Planned Documentation

- **Database Schema Reference**: Detailed SQLite schema
- **Integration Guide**: iCal, ActivityPub, Telegram deep dives
- **Performance Tuning**: Advanced optimization techniques
- **Security Guide**: Hardening and best practices
- **Migration Guide**: Upgrading between major versions

### How You Can Help

- Fix typos and errors
- Add missing sections
- Improve examples
- Create tutorials
- Translate to other languages

See **[CONTRIBUTING.md](CONTRIBUTING.md)** for contribution guidelines.

---

**Found a documentation issue?** [Open an issue](https://github.com/ademant/dansal/issues) or [suggest an improvement](https://github.com/ademant/dansal/discussions)!

**Want to contribute?** Check out **[CONTRIBUTING.md](CONTRIBUTING.md)** - we'd love your help! 🎉