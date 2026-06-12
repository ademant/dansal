# 🛠️ Admin Guide - System Administration

Complete guide for installing, configuring, and maintaining dansal instances.

## 📋 Table of Contents

- [System Requirements](#-system-requirements)
- [Installation](#-installation)
- [Configuration](#-configuration)
- [Deployment Options](#-deployment-options)
- [User Management](#-user-management)
- [System Maintenance](#-system-maintenance)
- [Monitoring & Logging](#-monitoring--logging)
- [Security](#-security)
- [Troubleshooting](#-troubleshooting)
- [Backup & Recovery](#-backup--recovery)

## 💻 System Requirements

### Minimum Requirements
- **OS**: Linux (recommended), macOS, or Windows (WSL)
- **CPU**: 2 cores
- **RAM**: 2GB
- **Storage**: 10GB (grows with event data and images)
- **Go**: 1.22+
- **SQLite**: 3.35+

### Recommended Production Setup
- **OS**: Ubuntu 22.04 LTS or Debian 11+
- **CPU**: 4 cores
- **RAM**: 4GB
- **Storage**: 50GB SSD
- **Reverse Proxy**: Nginx or Apache
- **SSL**: Let's Encrypt certificates

## 🚀 Installation

### From Source

```bash
# Clone repository
git clone https://github.com/ademant/dansal.git
cd dansal

# Build
go build -o dansal

# Run
./dansal
```

### Pre-built Binaries

Download latest releases from [GitHub Releases](https://github.com/ademant/dansal/releases):
- `dansal` - Main API server
- `dansal_web` - Web frontend
- `dansal_admin` - CLI administration tool
- `dansal_webmin` - Admin web interface

### Docker Installation

See **[DOCKER.md](DOCKER.md)** for containerized deployment.

### Branding Before Going Live

dansal ships with default logo, banner and favicon images built into the
`dansal_web` binary. Before announcing a new instance, drop your own
`logo`, `banner` and `favicon` files (`.svg`, `.avif`, `.jpg` or `.gif`)
into the instance's `images_dir` (configured in `web.yaml`, e.g.
`/var/lib/dansal-web/<instance>/`) — these are served in place of the
built-in defaults immediately, with no rebuild or restart required.

## ⚙️ Configuration

### Main Configuration File

`config.yaml` - Primary configuration file:

```yaml
# Server settings
server:
  port: 8000
  base_url: "https://your-domain.com"
  db_path: "/var/lib/dansal/calendar.db"
  admin_socket: "/var/run/dansal/admin.sock"

# Database settings
database:
  max_open_conns: 50
  max_idle_conns: 10
  conn_max_lifetime: "1h"

# Security settings
security:
  session_secret: "generate-a-strong-secret-here"
  session_expiry: "720h"  # 30 days
  login_max_failures: 5
  login_failure_window: "15m"

# Email settings (for notifications)
email:
  smtp_host: "smtp.example.com"
  smtp_port: 587
  smtp_username: "user@example.com"
  smtp_password: "password"
  from_address: "noreply@your-domain.com"

# Telegram bot (optional)
telegram:
  bot_token: "123456789:AABBccDDeeFFggHH"
  bot_name: "YourDansalBot"
```

### Environment Variables

Override configuration with environment variables:

```bash
export DANSAL_PORT=8080
export DANSAL_DB_PATH="/custom/path/database.db"
export DANSAL_SESSION_SECRET="your-secret-key"
```

### Configuration Reloading

Reload configuration without restart:

```bash
# Send SIGHUP to running process
kill -HUP $(pidof dansal)
```

**Note**: Some settings (port, db_path) require full restart.

## 🌐 Deployment Options

### Single Binary Deployment

```bash
# Build
GOOS=linux GOARCH=amd64 go build -o dansal

# Create systemd service
cp dansal.service /etc/systemd/system/
systemctl enable dansal
systemctl start dansal
```

### Systemd Service Files

Included service files:
- `dansal.service` - Main API service
- `dansal-web.service` - Web frontend
- `dansal-webmin.service` - Admin interface
- `dansal-*.timer` - Scheduled tasks (backups, etc.)

### Reverse Proxy Configuration

**Nginx example:**

```nginx
server {
    listen 80;
    server_name dansal.example.com;
    return 301 https://$host$request_uri;
}

server {
    listen 443 ssl;
    server_name dansal.example.com;

    ssl_certificate /etc/letsencrypt/live/dansal.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/dansal.example.com/privkey.pem;

    location / {
        proxy_pass http://localhost:8000;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    location /ws/ {
        proxy_pass http://localhost:8000;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
    }
}
```

### Docker Compose

See **[DOCKER.md](DOCKER.md)** for complete Docker setup.

## 👥 User Management

### First Admin Account

On first run, dansal creates an admin account:

```
Admin user created — username: admin  password: <generated-password>
```

**IMMEDIATELY** change this password after first login!

### Creating Users

#### Via Admin Interface
1. Login as admin
2. Go to **Admin → Users → Create User**
3. Fill in username, email, password, and role
4. Save

#### Via CLI

```bash
# Using dansal_admin tool
./dansal_admin create-user \
  --username "john_doe" \
  --email "john@example.com" \
  --password "secure-password" \
  --role "publisher"
```

### User Roles

| Role | Description |
|---|---|
| **admin** | Full system access, can manage everything |
| **publisher** | Can create/edit events, manage locations/musicians |
| **user** | Can create events for their own organization only |
| **viewer** | Read-only access, can see unpublished events |

### Invite Links

Safer alternative to direct account creation:

1. Go to **Admin → Invites → Create Invite**
2. Set role and optional organization
3. Set expiry time (default 48 hours)
4. Share the generated link
5. Recipient registers through the link with pre-set role

### Account Security

- **Password Policies**: Enforce minimum length and complexity
- **Failed Login Lockout**: Automatic after 5 failures in 15 minutes
- **Session Management**: View and revoke active sessions
- **2FA**: WebAuthn and TOTP support
- **Magic Links**: Passwordless login via email

## 🔧 System Maintenance

### Database Maintenance

```bash
# Vacuum database (optimize storage)
./dansal_admin vacuum

# Analyze database (update statistics)
./dansal_admin analyze

# Backup database
./dansal_admin backup --output backup.db
```

### Log Rotation

Configure logrotate for dansal logs:

```bash
# /etc/logrotate.d/dansal
/var/log/dansal/*.log {
    daily
    missingok
    rotate 30
    compress
    delaycompress
    notifempty
    create 640 dansal dansal
}
```

### Regular Maintenance Tasks

1. **Daily**: Database backup
2. **Weekly**: Log rotation, temporary file cleanup
3. **Monthly**: Database vacuum and analyze
4. **Quarterly**: Review user accounts and permissions

## 📊 Monitoring & Logging

### Logging Configuration

```yaml
# In config.yaml
logging:
  level: "info"  # debug, info, warn, error
  file: "/var/log/dansal/api.log"
  max_size: 100  # MB
  max_backups: 7
  max_age: 30  # days
```

### Log Levels

- **DEBUG**: Detailed operational information
- **INFO**: Normal operation messages
- **WARN**: Potential issues
- **ERROR**: Problems that need attention

### Monitoring Endpoints

```
GET /healthz        - Health check
GET /readyz         - Readiness check
GET /metrics        - Prometheus metrics
GET /status         - Detailed system status
```

### Alerting

Set up alerts for:
- Failed logins (potential brute force attacks)
- Database connection issues
- High error rates
- Storage capacity warnings

## 🔒 Security

### Security Best Practices

1. **Keep Updated**: Regularly update dansal and dependencies
2. **Use HTTPS**: Always use TLS encryption
3. **Strong Passwords**: Enforce password policies
4. **Principle of Least Privilege**: Only grant necessary permissions
5. **Regular Audits**: Review user accounts and access logs
6. **Backup Regularly**: Test restoration process

### Security Features

- **CSRF Protection**: Enabled by default
- **CORS**: Configurable origins
- **Rate Limiting**: Prevent abuse
- **SQL Injection Protection**: Prepared statements
- **XSS Protection**: Content security policies
- **Security Headers**: Strict transport security

### Telegram Integration Security

- Verification tokens expire after 24 hours
- Webhook validates all incoming requests
- No sensitive data stored in Telegram
- Users can revoke Telegram access anytime

## 🚨 Troubleshooting

### Common Issues

#### Database Connection Errors
- **Symptoms**: "Failed to connect to database"
- **Solutions**:
  - Check database file permissions
  - Verify db_path in config.yaml
  - Check disk space
  - Run `sqlite3 /path/to/db "PRAGMA integrity_check;"`

#### Performance Issues
- **Symptoms**: Slow response times
- **Solutions**:
  - Run `dansal_admin analyze`
  - Check for missing indexes
  - Review slow query logs
  - Increase database connection pool

#### Login Problems
- **Symptoms**: "Invalid credentials"
- **Solutions**:
  - Check failed login attempts
  - Verify account isn't locked
  - Reset password via magic link
  - Check session secret configuration

### Debugging

```bash
# Increase log level
./dansal_admin config set logging.level debug

# View real-time logs
tail -f /var/log/dansal/api.log

# Check running processes
ps aux | grep dansal

# Test API endpoints
curl -v http://localhost:8000/healthz
```

## 💾 Backup & Recovery

### Backup Strategy

```bash
# Full backup (database + config + uploads)
./dansal_admin backup --full --output /backups/dansal-$(date +%Y-%m-%d).tar.gz

# Database only backup
./dansal_admin backup --db-only --output /backups/db-$(date +%Y-%m-%d).db

# Automated backup (cron)
0 2 * * * /usr/local/bin/dansal_admin backup --full --output /backups/dansal-$(date +\%Y-\%m-\%d).tar.gz
```

### Recovery Process

```bash
# Stop dansal service
systemctl stop dansal

# Restore from backup
./dansal_admin restore --input /backups/dansal-2024-01-01.tar.gz

# Verify database integrity
sqlite3 /var/lib/dansal/calendar.db "PRAGMA integrity_check;"

# Start service
systemctl start dansal
```

### Disaster Recovery Plan

1. **Immediate**: Restore from latest backup
2. **Verify**: Check data integrity and consistency
3. **Communicate**: Notify users of any data loss
4. **Investigate**: Determine cause to prevent recurrence
5. **Document**: Update procedures based on lessons learned

## 📈 Scaling & Performance

### Vertical Scaling
- Increase CPU/RAM resources
- Optimize SQLite configuration
- Tune database connection pool

### Horizontal Scaling (Advanced)
- Use read replicas for reporting
- Implement caching layer (Redis)
- Consider PostgreSQL for very large deployments

### Performance Tuning

```yaml
# Database performance settings
database:
  max_open_conns: 100
  max_idle_conns: 20
  conn_max_lifetime: "30m"
  
  # SQLite-specific settings
  sqlite:
    busy_timeout: "5000"
    journal_mode: "WAL"
    synchronous: "NORMAL"
```

## 🔄 Upgrading dansal

### Upgrade Process

```bash
# Backup current installation
./dansal_admin backup --full --output upgrade-backup.tar.gz

# Stop services
systemctl stop dansal dansal-web dansal-webmin

# Replace binaries
cp new-binaries/* /usr/local/bin/

# Run migrations
./dansal_admin migrate

# Start services
systemctl start dansal dansal-web dansal-webmin

# Verify
./dansal_admin status
```

### Version Compatibility

- **Major versions** (1.x → 2.x): May require manual intervention
- **Minor versions** (1.2 → 1.3): Automatic migrations
- **Patch versions** (1.2.1 → 1.2.2): Drop-in replacements

## 📚 Additional Resources

- **[User Guide](USER_GUIDE.md)** - For event organizers
- **[Visitor Guide](VISITOR_GUIDE.md)** - For dance enthusiasts
- **[Developer Guide](DEVELOPER_GUIDE.md)** - API and development
- **[Docker Guide](DOCKER.md)** - Container deployment

---

**Need help?** Open an issue on [GitHub](https://github.com/ademant/dansal/issues) or check our [discussions](https://github.com/ademant/dansal/discussions).

**Security issues?** Please report responsibly to security@dansal.example.com.