# Database Migrations for dansal-web

This directory contains SQL migration scripts to update the database schema.

## Current Issue

The WebFinger implementation on `dev.balfolk.jetzt` is failing with:
```
{"error":"no such column: public_key_ed25519_pem"}
```

This indicates that the database schema is outdated and missing columns required by the current code.

## Applying Migrations

### Method 1: Using the migration tool (recommended)

```bash
# Build the migration tool
cd cmd/dansal_web/migrations
go build apply_migrations.go

# Apply migrations to your database
./apply_migrations /path/to/your/web.db ../../cmd/dansal_web/migrations
```

### Method 2: Manual SQL execution

If you don't want to use the Go tool, you can apply the SQL directly:

```bash
# For SQLite
sqlite3 /path/to/your/web.db < migrations/001_add_ed25519_keys.sql
```

## Migration Files

- `001_add_ed25519_keys.sql` - Adds Ed25519 key columns required for modern ActivityPub support

## Troubleshooting

If you encounter issues:

1. **Backup your database first**: `cp web.db web.db.backup`
2. **Check current schema**: `sqlite3 web.db ".schema actors"`
3. **Verify migration was applied**: `sqlite3 web.db "SELECT * FROM schema_migrations;"`

## For dev.balfolk.jetzt

To fix the WebFinger issue on dev.balfolk.jetzt:

1. Locate the database file (typically `/var/lib/dansal-web/web.db`)
2. Apply the migration:
   ```bash
   sqlite3 /var/lib/dansal-web/web.db < /path/to/dansal/cmd/dansal_web/migrations/001_add_ed25519_keys.sql
   ```
3. Restart the dansal-web service
4. Test WebFinger: `curl "https://dev.balfolk.jetzt/.well-known/webfinger?resource=acct:admin@dev.balfolk.jetzt"`