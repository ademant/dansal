# 👨‍💻 Developer Guide

Guide for developers working with dansal's codebase, API, or contributing to the project.

## 📋 Table of Contents

- [Project Structure](#-project-structure)
- [API Reference](#-api-reference)
- [Authentication](#-authentication)
- [Data Models](#-data-models)
- [Development Setup](#-development-setup)
- [Coding Guidelines](#-coding-guidelines)
- [Testing](#-testing)
- [Building & Packaging](#-building--packaging)
- [Contributing](#-contributing)
- [Architecture](#-architecture)

## 🗂️ Project Structure

```
dansal/
├── cmd/                  # Command-line applications
│   ├── dansal/           # Main API server
│   ├── dansal_admin/     # CLI administration tool
│   ├── dansal_web/       # Web frontend
│   └── dansal_webmin/    # Admin web interface
├── internal/             # Core libraries
│   ├── api/              # API handlers and routes
│   ├── db/               # Database models and operations
│   ├── auth/             # Authentication and authorization
│   ├── config/           # Configuration management
│   ├── services/         # Business logic services
│   └── util/             # Utilities and helpers
├── web/                  # Web assets (compiled into binaries)
│   ├── frontend/         # React/Vue frontend code
│   └── admin/            # Admin interface code
├── migrations/           # Database migration scripts
├── scripts/              # Build and deployment scripts
├── config.yaml            # Configuration file
└── go.mod                # Go module definition
```

## 📡 API Reference

**Complete API documentation**: See **[API.md](API.md)**

### API Basics

- **Base URL**: `/api/v1/`
- **Content Type**: `application/json`
- **Authentication**: Bearer tokens or API keys
- **Pagination**: `?page=1&limit=50`
- **Filtering**: `?filter[key]=value`
- **Sorting**: `?sort=field` or `?sort=-field` (descending)

### Authentication

```bash
# Login to get token
POST /api/v1/login
Content-Type: application/json

{
  "username": "your_username",
  "password": "your_password"
}

# Use token in subsequent requests
Authorization: Bearer <token>
```

### Common Endpoints

| Endpoint | Method | Description |
|---|---|---|
| `/events` | GET | List events |
| `/events` | POST | Create event |
| `/events/{id}` | GET | Get single event |
| `/events/{id}` | PUT | Update event |
| `/locations` | GET | List locations |
| `/organizations` | GET | List organizations |
| `/musicians` | GET | List musicians |
| `/users/me` | GET | Get current user profile |

### WebSocket Endpoints

- `/ws/notifications` - Real-time notifications
- `/ws/updates` - Live event updates

## 🔐 Authentication

### Session Tokens
- Issued on login or magic link usage
- Expire after configurable duration (default: 30 days)
- Can be revoked individually
- Support multiple concurrent sessions

### API Keys
- Prefixed with `ak_`
- Created via `/api/v1/apikeys`
- Can be restricted to specific endpoints
- No expiration (must be manually revoked)

### WebAuthn
- Passwordless authentication
- Hardware security key support
- Multiple credentials per user

### OAuth2 (Future)
- Planned for future versions
- Will support GitHub, Google, etc.

## 📊 Data Models

### Core Models

#### Event
```json
{
  "id": "string",
  "title": "string",
  "description": "string",
  "start_time": "RFC3339",
  "end_time": "RFC3339",
  "location_id": "string",
  "organization_id": "string",
  "type": "ball|workshop|festival|combination",
  "difficulty": "beginner|advanced|pro",
  "status": "draft|published|cancelled",
  "is_public": "boolean",
  "created_at": "RFC3339",
  "updated_at": "RFC3339",
  "created_by": "user_id",
  "updated_by": "user_id"
}
```

#### Location
```json
{
  "id": "string",
  "name": "string",
  "short_name": "string",
  "address": "string",
  "postcode": "string",
  "town": "string",
  "country": "string",
  "latitude": "float64",
  "longitude": "float64",
  "website": "string",
  "organization_id": "string",
  "accessibility": {
    "wheelchair": "boolean",
    "parking": "string",
    "floor_surface": "string",
    "seating": "boolean",
    "hearing_loop": "boolean"
  }
}
```

#### Organization
```json
{
  "id": "string",
  "name": "string",
  "description": "string",
  "website": "string",
  "mastodon": "string",
  "instagram": "string",
  "facebook": "string",
  "email": "string",
  "logo_url": "string"
}
```

#### User
```json
{
  "id": "string",
  "username": "string",
  "email": "string",
  "role": "admin|publisher|user|viewer",
  "disabled": "boolean",
  "created_at": "RFC3339",
  "last_login": "RFC3339",
  "telegram_handle": "string",
  "telegram_verified": "boolean",
  "telegram_chat_id": "string"
}
```

## 🛠️ Development Setup

### Prerequisites

- Go 1.22+
- SQLite 3.35+
- Node.js 18+ (for web frontend)
- Make
- Git

### Setup

```bash
# Clone repository
git clone https://github.com/ademant/dansal.git
cd dansal

# Install Go dependencies
go mod download

# Install JavaScript dependencies
cd web/frontend
npm install
cd ../..

# Build everything
make build

# Run development server
make dev
```

### Development Workflow

```bash
# Run API server
go run ./cmd/dansal

# Run web frontend (in another terminal)
cd web/frontend
npm run dev

# Run admin interface (in another terminal)
cd web/admin
npm run dev
```

### Hot Reloading

- Go server: Uses `air` for live reload
- Web frontend: Vite hot module replacement
- Admin interface: Vite hot module replacement

## 📝 Coding Guidelines

### Go Code Style

- Follow [Effective Go](https://go.dev/doc/effective_go)
- Use `gofmt` for formatting
- Use `golangci-lint` for linting
- Keep functions short and focused
- Use meaningful names
- Write comprehensive docstrings

### JavaScript/TypeScript Style

- Use ESLint with our configuration
- Follow React best practices
- Use functional components with hooks
- TypeScript for new code
- Consistent naming (camelCase for variables, PascalCase for components)

### Commit Messages

Follow [Conventional Commits](https://www.conventionalcommits.org/):

```
feat: add new event creation endpoint
fix: correct timezone handling in event display
docs: update API documentation for musicians
chore: update dependencies
refactor: improve database query performance
style: format code with gofmt
test: add unit tests for authentication
```

### Branch Naming

- `feat/*` - New features
- `fix/*` - Bug fixes
- `docs/*` - Documentation changes
- `refactor/*` - Code refactoring
- `test/*` - Testing improvements
- `chore/*` - Maintenance tasks

## 🧪 Testing

### Running Tests

```bash
# Unit tests
make test

# Integration tests
make test-integration

# All tests
make test-all

# Test coverage
make test-coverage
```

### Test Structure

```
internal/
└── api/
    └── events_test.go      # Unit tests for events API
internal/
└── db/
    └── events_test.go     # Database tests
internal/
└── services/
    └── events_test.go     # Service layer tests
```

### Writing Tests

```go
func TestCreateEvent(t *testing.T) {
    // Setup
    db := testdb.New()
    service := events.NewService(db)
    
    // Test data
    eventData := events.CreateParams{
        Title: "Test Event",
        StartTime: time.Now(),
        // ... other fields
    }
    
    // Execute
    created, err := service.Create(context.Background(), eventData)
    
    // Assert
    assert.NoError(t, err)
    assert.NotEmpty(t, created.ID)
    assert.Equal(t, "Test Event", created.Title)
    
    // Cleanup
    cleanup(db)
}
```

## 📦 Building & Packaging

### Build Targets

```bash
# Build all binaries
make build

# Build specific binary
make build-dansal
make build-dansal-web
make build-dansal-admin
make build-dansal-webmin

# Cross-compile
make build-linux
make build-windows
make build-macos
```

### Docker Builds

```bash
# Build Docker images
make docker-build

# Build and push
make docker-push
```

### Creating Releases

```bash
# Bump version
make bump-version PATCH=1

# Create release
make release

# Build release assets
make release-builds
```

## 🤝 Contributing

### Contribution Process

1. **Fork** the repository
2. **Clone** your fork
3. **Create** a feature branch
4. **Develop** your changes
5. **Test** thoroughly
6. **Document** your changes
7. **Commit** with clear messages
8. **Push** to your fork
9. **Open** a pull request

### Pull Request Guidelines

- Reference related issues (e.g., "Fixes #123")
- Include screenshots for UI changes
- Update documentation if needed
- Keep PRs focused on single features/bugs
- Be responsive to feedback
- Follow the code review process

### Code Review Process

1. **Automated Checks**: CI runs tests and linting
2. **Peer Review**: At least one approval required
3. **Maintainer Review**: Final check by project maintainer
4. **Merge**: After all checks pass

## 🏗️ Architecture

### System Overview

```
┌───────────────────────────────────────────────────────┐
│                     Client Applications               │
├─────────────┬─────────────┬─────────────┬─────────────┤
│  Web Frontend  │  Mobile App    │  CLI Tools    │  3rd Party  │
└─────────────┴─────────────┴─────────────┴─────────────┘
                            │
                            ▼
┌───────────────────────────────────────────────────────┐
│                     dansal API Server                 │
├─────────────┬─────────────┬─────────────┬─────────────┤
│  REST API     │  WebSocket    │  Auth Service │  Cache      │
└─────────────┴─────────────┴─────────────┴─────────────┘
                            │
                            ▼
┌───────────────────────────────────────────────────────┐
│                     SQLite Database                   │
├─────────────┬─────────────┬─────────────┬─────────────┤
│  Events        │  Locations    │  Users        │  Org.       │
└─────────────┴─────────────┴─────────────┴─────────────┘
                            │
                            ▼
┌───────────────────────────────────────────────────────┐
│                     External Services                 │
├─────────────┬─────────────┬─────────────┬─────────────┤
│  ActivityPub  │  Telegram     │  MusicBrainz  │  SMTP       │
└─────────────┴─────────────┴─────────────┴─────────────┘
```

### Key Components

1. **API Layer**: RESTful endpoints with JSON payloads
2. **Service Layer**: Business logic and validation
3. **Database Layer**: SQLite with Go migrations
4. **Auth Layer**: JWT tokens, API keys, WebAuthn
5. **Integration Layer**: ActivityPub, Telegram, etc.

### Data Flow

```
Request → Middleware (Auth, Logging) → Handler → Service → Repository → Database
```

### Error Handling

- Structured error responses
- Error codes for programmatic handling
- Detailed error messages in development
- Generic messages in production

## 🔧 Development Tools

### Recommended Tools

- **Editor**: VS Code with Go extension
- **Go Tools**: `gofmt`, `golangci-lint`, `delve`
- **Database**: `sqlite3`, `sqlitebrowser`
- **API Testing**: Postman, Insomnia, or `curl`
- **Monitoring**: Prometheus, Grafana

### VS Code Extensions

- Go (Golang)
- ESLint
- Prettier
- SQLite
- REST Client
- GitLens

## 📚 Learning Resources

### dansal-Specific

- **[API Documentation](API.md)** - Complete endpoint reference
- **[Admin Guide](ADMIN_GUIDE.md)** - System administration
- **[User Guide](USER_GUIDE.md)** - Using the platform

### General Development

- [Go Documentation](https://go.dev/doc/)
- [React Documentation](https://react.dev/)
- [SQLite Documentation](https://www.sqlite.org/docs.html)
- [REST API Design Guide](https://restfulapi.net/)

## 🤔 Need Help?

- **Development Questions**: [GitHub Discussions](https://github.com/ademant/dansal/discussions)
- **Bug Reports**: [GitHub Issues](https://github.com/ademant/dansal/issues)
- **Security Issues**: security@dansal.example.com
- **General Help**: Check our other guides

---

**Happy coding!** 🎉
Your contributions help grow the dance community ecosystem.