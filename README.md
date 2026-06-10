# dansal - Dance Event Management System

**Open-source calendar and event platform for dance communities**

[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.22%2B-blue)](https://go.dev/)
[![SQLite](https://img.shields.io/badge/database-SQLite-blue)](https://sqlite.org/)

## 🎯 Quick Overview

dansal helps dance communities organize, publish, and discover events - from folk dances and tangos to salsa festivals and workshops.

**Key Features:**
- ✅ Multi-format event import (iCal, JSON, API)
- ✅ Automatic ActivityPub publishing to the fediverse
- ✅ Interactive map with geospatial event discovery
- ✅ Multi-language support (8 languages)
- ✅ Role-based access control (admin, publisher, user, viewer)
- ✅ Community bulletin board for ride-sharing and ticket exchange

## 📖 Documentation

### For Visitors & Dance Enthusiasts
Learn how to find events, use the interactive map, and connect with the community:
→ **[Visitor Guide](VISITOR_GUIDE.md)**

### For Event Organizers & Users
Create and manage events, venues, and organizations:
→ **[User Guide](USER_GUIDE.md)**

### For System Administrators
Installation, configuration, maintenance, and troubleshooting:
→ **[Admin Guide](ADMIN_GUIDE.md)**

### For Developers
API reference, architecture, and contribution guidelines:
→ **[Developer Guide](DEVELOPER_GUIDE.md)**

## 🚀 Quick Start

```bash
# Run from source
go run .

# Or build and run
go build -o dansal
./dansal
```

The server starts on port 8000 (configurable). On first run, an admin account is created with credentials printed to console.

## 🔧 Deployment Options

- **Single binary**: Simple deployment with SQLite backend
- **Docker**: Containerized deployment with `docker-compose`
- **Systemd**: Service files included for production use

See **[Admin Guide](ADMIN_GUIDE.md)** for detailed deployment instructions.

## 📞 Support & Community

- **Issues & Bug Reports**: [GitHub Issues](https://github.com/ademant/dansal/issues)
- **Feature Requests**: [GitHub Discussions](https://github.com/ademant/dansal/discussions)
- **Documentation**: This repository's Markdown files

## 🤝 Contributing

We welcome contributions! Please see **[CONTRIBUTING.md](CONTRIBUTING.md)** for guidelines on how to contribute code, documentation, or translations.

## 📜 License

[MIT License](LICENSE) - Copyright © 2024 dansal contributors

---

**Star this repository** if you find dansal useful! ⭐
Your support helps grow the dance community ecosystem.