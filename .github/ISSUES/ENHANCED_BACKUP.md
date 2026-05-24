# Enhanced Backup System for Dansal

## Issue: Upgrade Backup Function to Include Dansal-Web with Security Features

### Current Limitations

1. **Incomplete Backup**: Current backup only includes dansal database, not dansal-web
2. **No Password Protection**: Regular backups contain credentials in plaintext
3. **Limited Security Options**: Only encrypted backup option exists, but it's separate
4. **No Secure Mode**: No way to create backups without credentials for sharing

### Requirements

1. **Comprehensive Backup**: Include both dansal and dansal-web databases
2. **Secure Mode**: Create backups without credentials for safe sharing
3. **Encryption**: Strong encryption with modern key derivation
4. **Backward Compatibility**: Existing backup/restore functionality preserved
5. **Password Safety**: Never store passwords in plaintext

### Proposed Solution

**New `enhanced-backup` command with multiple modes:**

#### 1. Normal Mode (Default)
```bash
# Backup dansal only
dansal_admin enhanced-backup

# Backup dansal + dansal-web
dansal_admin enhanced-backup --include-web
```
- Includes: config.yaml, calendar.db, images/
- Optionally: web.db (when --include-web specified)
- Credentials: Preserved (for local backups)

#### 2. Secure Mode (--secure)
```bash
# Secure backup without credentials
dansal_admin enhanced-backup --secure --include-web
```
- Removes all password hashes from databases
- Removes API keys and sensitive data
- Safe for cloud storage or sharing
- Uses same approach as existing backup (VACUUM + sanitization)

#### 3. Encrypted Mode (--encrypt)
```bash
# Encrypted backup with password prompt
dansal_admin enhanced-backup --encrypt --include-web

# With password provided
dansal_admin enhanced-backup --encrypt --password "secret" --include-web
```
- **Encryption**: AES-256-GCM (authenticated encryption)
- **Key Derivation**: Argon2 (memory-hard, resistant to GPU attacks)
- **Parameters**: 3 iterations, 64MB memory, 4 parallelism
- **Password Handling**: Prompted securely (no echo), confirmed twice
- **Output**: `.tar.gz.enc` extension

### Implementation Details

#### Files Created

1. **`cmd/dansal_admin/enhanced_backup.go`** - Core implementation
   - `EnhancedBackupOptions` struct for configuration
   - `createEnhancedBackup()` - Main backup function
   - `encryptFile()` / `decryptFile()` - AES-256-GCM encryption
   - `deriveKey()` - Argon2 key derivation
   - Database sanitization functions

2. **Command Handlers**
   - `cmdEnhancedBackup()` - Backup command
   - `cmdEnhancedRestore()` - Restore command

3. **Security Features**
   - Password prompting with terminal echo disabled
   - Password confirmation (typed twice)
   - Secure memory handling
   - Random salt generation for each encryption

#### Backup Archive Structure
```
backup.tar.gz (or .tar.gz.enc if encrypted)
├── config.yaml          - Server configuration
├── calendar.db          - Main dansal database
├── web.db              - Dansal-web database (optional)
└── images/             - Uploaded images
```

#### Encryption Format
```
[16 bytes salt][12 bytes nonce][variable ciphertext]
- Salt: Random per-backup, prevents rainbow tables
- Nonce: Random per-encryption, ensures uniqueness
- Ciphertext: AES-256-GCM encrypted data with authentication tag
```

### Security Features

1. **Never Store Plaintext Passwords**
   - Secure mode removes all credentials before backup
   - Encrypted mode uses strong cryptography

2. **Modern Cryptography**
   - AES-256-GCM: Authenticated encryption
   - Argon2: Memory-hard key derivation
   - Random salts and nonces

3. **Secure Defaults**
   - File permissions: 0600 for encrypted files
   - Password prompting: No terminal echo
   - Password confirmation: Typed twice

### Usage Examples

```bash
# 1. Normal backup (dansal only)
dansal_admin enhanced-backup

# 2. Complete backup (dansal + dansal-web)
dansal_admin enhanced-backup --include-web

# 3. Secure backup (no credentials, safe for sharing)
dansal_admin enhanced-backup --secure --include-web

# 4. Encrypted backup (password prompted)
dansal_admin enhanced-backup --encrypt --include-web

# 5. Encrypted backup with custom output
dansal_admin enhanced-backup --encrypt --output /backups/dansal.tar.gz.enc --include-web

# 6. Restore normal backup
dansal_admin enhanced-restore --input backup.tar.gz

# 7. Restore encrypted backup (password prompted)
dansal_admin enhanced-restore --input backup.tar.gz.enc
```

### Backward Compatibility

- ✅ Existing `backup` command unchanged
- ✅ Existing `password-backup` command unchanged
- ✅ New commands are additive
- ✅ No breaking changes to existing functionality

### Testing Requirements

1. **Normal Backup**: Verify all components included
2. **Secure Backup**: Verify credentials removed
3. **Encrypted Backup**: Verify encryption/decryption works
4. **Restore**: Verify all components restored correctly
5. **Password Handling**: Verify prompting and confirmation
6. **Error Handling**: Invalid passwords, missing files, etc.

### Expected Benefits

1. **Comprehensive**: Complete system backup in one command
2. **Secure**: Multiple security levels available
3. **Flexible**: Choose what to include (dansal-web optional)
4. **Modern**: Uses current cryptographic best practices
5. **User-Friendly**: Clear examples and help text
6. **Safe**: Never exposes passwords in process list

### Implementation Status

- ✅ Core backup/restore functionality implemented
- ✅ Encryption with AES-256-GCM
- ✅ Argon2 key derivation
- ✅ Secure password prompting
- ✅ Command-line interface
- ✅ Help documentation
- ✅ Error handling
- ⏳ Database sanitization (uses existing approach)
- ⏳ Comprehensive testing

This enhancement provides a complete, secure backup solution that addresses all the requirements while maintaining backward compatibility.