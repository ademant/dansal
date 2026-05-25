# nginx Reverse Proxy Configuration with HTTP/3 Support

This directory contains nginx configuration templates for deploying dansal behind a reverse proxy with HTTP/3 (QUIC) support.

## Features

- **HTTP/3 (QUIC) support** - Modern protocol for faster connections
- **HTTP/2 fallback** - Compatibility with older clients
- **Automatic HTTP → HTTPS redirect** - Security by default
- **Certbot integration** - Works with Let's Encrypt certificates
- **Dual upstream support** - Proxy both API and web frontend
- **Security headers** - HSTS, XSS protection, etc.
- **Rate limiting** - Protect API endpoints from brute force and DDoS attacks

## Requirements

### For HTTP/3 Support

- **nginx 1.25.0+** with `--with-http_v3_module` compiled in
- **OpenSSL 1.1.1+** (for QUIC support)
- **Linux Kernel 5.6+** (recommended for UDP GRO support)

### Standard Requirements

- Certbot for TLS certificate management
- dansal API service running on `127.0.0.1:8000`
- dansal-web frontend running on `127.0.0.1:8080`

## Installation

### 1. Install nginx with HTTP/3 support

#### On Ubuntu/Debian:
```bash
sudo apt install nginx
# Verify HTTP/3 module is available:
nginx -V 2>&1 | grep -o http_v3_module
```

#### If you need to compile from source:
```bash
sudo apt build-dep nginx
wget https://nginx.org/download/nginx-1.25.3.tar.gz
tar -xzvf nginx-1.25.3.tar.gz
cd nginx-1.25.3
./configure --with-http_v3_module --with-http_ssl_module
make
sudo make install
```

### 2. Install certbot for TLS certificates

#### Install certbot with nginx plugin:
```bash
sudo apt install certbot python3-certbot-nginx
```

#### Recommended: Get certificates before deploying nginx config
```bash
sudo certbot certonly --nginx -d events.example.com
```

This creates certificates in `/etc/letsencrypt/live/events.example.com/` which our template expects.

### 2. Configure dansal.conf

1. Copy the template:
```bash
sudo cp dansal.conf /etc/nginx/sites-available/dansal
```

2. Edit the file and replace:
   - `events.example.com` with your actual domain
   - Certificate paths if you use a different location than `/etc/letsencrypt/live/`

3. Enable the site:
```bash
sudo ln -s /etc/nginx/sites-available/dansal /etc/nginx/sites-enabled/
```

### 3. Obtain TLS certificates with certbot

```bash
sudo apt install certbot python3-certbot-nginx
sudo certbot certonly --nginx -d events.example.com
```

### 4. Test and reload nginx

```bash
sudo nginx -t  # Test configuration
sudo systemctl reload nginx
```

## Testing HTTP/3

### Using curl (7.66.0+)
```bash
curl --http3-only https://events.example.com
```

### Using Chrome
1. Open `chrome://flags/`
2. Enable "Experimental QUIC protocol"
3. Visit your site and check DevTools → Network → Protocol column

### Using [http3check.net](https://http3check.net/)
```bash
curl https://http3check.net/api/v2/check?host=events.example.com
```

## Testing Rate Limiting

### Test general API rate limiting
```bash
# Send 25 requests quickly (should trigger rate limiting)
for i in {1..25}; do curl -s -o /dev/null -w "%{http_code}\n" https://events.example.com/api/v1/info; done
# Expected: Mostly 200 responses, then some 429 responses
```

### Test authentication rate limiting
```bash
# Send 15 login requests quickly (should trigger auth rate limiting)
for i in {1..15}; do curl -s -o /dev/null -w "%{http_code}\n" -X POST https://events.example.com/api/v1/login -H "Content-Type: application/json" -d '{"username":"test","password":"test"}'; done
# Expected: Mostly 401 responses (invalid creds), then some 429 responses
```

### Check rate limiting headers
```bash
curl -I https://events.example.com/api/v1/info
# Look for RateLimit headers if you've enabled them
```

### Monitor rate limiting in real-time
```bash
# Watch nginx error logs for rate limiting messages
sudo tail -f /var/log/nginx/error.log | grep limit
```

## Configuration Details

### Dual Server Setup

The configuration uses two server blocks:

1. **HTTP/3 server** (`listen 443 quic`):
   - Handles modern clients that support HTTP/3
   - Requires TLS 1.3
   - Uses `reuseport` for better performance
   - Advertises HTTP/3 via `Alt-Svc` header

2. **HTTP/2 server** (`listen 443 ssl http2`):
   - Fallback for older clients
   - Supports TLS 1.2 and 1.3
   - Same functionality as HTTP/3 server

### Certbot Integration Best Practices

Our nginx template is designed to work seamlessly with certbot. Here are best practices for integration:

#### Certificate Paths
The template uses standard Let's Encrypt paths:
```nginx
ssl_certificate     /etc/letsencrypt/live/events.example.com/fullchain.pem;
ssl_certificate_key /etc/letsencrypt/live/events.example.com/privkey.pem;
```

#### Recommended Workflow

**Option 1: Certbot First (Recommended)**
```bash
# 1. Get certificate before deploying nginx config
sudo certbot certonly --nginx -d your-domain.com

# 2. Deploy dansal-web
sudo make install-web

# 3. Deploy nginx configuration (replaces domain automatically)
sudo make deploy-nginx

# 4. Test and reload
sudo nginx -t && sudo systemctl reload nginx
```

**Option 2: Certbot After Deployment**
```bash
# 1. Deploy nginx config (uses dummy certs initially)
sudo make deploy-nginx

# 2. Get certificate - certbot will modify the config
sudo certbot --nginx -d your-domain.com

# 3. Test
sudo nginx -t && sudo systemctl reload nginx
```

#### Certificate Renewal

Certbot automatically sets up renewal. Test it with:
```bash
# Dry run (test renewal without making changes)
sudo certbot renew --dry-run

# Check renewal timer
sudo systemctl list-timers | grep certbot
```

#### Multi-Domain Setup

If you need multiple domains, edit the deployed config:
```bash
sudo nano /etc/nginx/sites-available/dansal
```

Add additional server names:
```nginx
server_name your-domain.com www.your-domain.com;
```

Then update certificates:
```bash
sudo certbot --expand -d your-domain.com,www.your-domain.com
```

#### Troubleshooting Certbot Issues

1. **Certificate not found**:
   ```bash
   # Check certbot certificates
   sudo certbot certificates
   
   # Verify paths match in nginx config
   ls -la /etc/letsencrypt/live/your-domain.com/
   ```

2. **Permission issues**:
   ```bash
   sudo chown -R root:root /etc/letsencrypt/
   sudo chmod -R 755 /etc/letsencrypt/
   sudo systemctl restart nginx
   ```

3. **Certbot renewal failures**:
   ```bash
   # Check renewal logs
   sudo journalctl -u certbot -f
   
   # Manual renewal test
   sudo certbot renew --force-renewal --dry-run
   ```

#### Advanced: Custom Certificate Paths

If you use custom certificate paths, modify the template before deployment:
```bash
sed -i 's|/etc/letsencrypt/live/events.example.com|/your/custom/path|g' deploy/nginx/dansal.conf
```

Then deploy normally:
```bash
sudo make deploy-nginx
```

### Rate Limiting

The configuration includes two rate limiting zones:

1. **General API rate limiting** (`api_limit`):
   - 10 requests/second per IP address
   - Burst capacity: 20 requests
   - Applies to all `/api/` endpoints

2. **Authentication rate limiting** (`auth_limit`):
   - 5 requests/second per IP address  
   - Burst capacity: 10 requests
   - Applies to sensitive endpoints: `/api/v1/login`, `/api/v1/register`, `/api/v1/verify`, `/api/v1/invites`

**How it works:**
- Uses nginx's `limit_req` module with shared memory zones
- Tracks requests by client IP address (`$binary_remote_addr`)
- Returns HTTP 429 (Too Many Requests) when limit exceeded
- `nodelay` flag ensures immediate rate limiting without delays

**Adjusting limits:**
Edit these lines in `dansal.conf`:
```nginx
limit_req_zone $binary_remote_addr zone=api_limit:10m rate=10r/s;
limit_req_zone $binary_remote_addr zone=auth_limit:10m rate=5r/s;
```

Change `10r/s` and `5r/s` to your desired rates, and adjust burst values in the `location` blocks.

### Security

- **HSTS**: Enforced with 2-year max-age
- **TLS**: TLS 1.2+ (TLS 1.3 required for HTTP/3)
- **Headers**: XSS protection, frame options, referrer policy
- **Proxy headers**: Proper forwarding of client IP and protocol

### Proxy Configuration

- **API endpoint**: `/api/` → `http://dansal_api` (127.0.0.1:8000)
- **Web frontend**: `/` → `http://dansal_web` (127.0.0.1:8080)
- **Client max body size**: 10MB for file uploads
- **Keepalive**: 16 connections per upstream

## Troubleshooting

### HTTP/3 not working

1. Check nginx version:
```bash
nginx -v
```

2. Verify HTTP/3 module:
```bash
nginx -V 2>&1 | grep http_v3_module
```

3. Check for errors:
```bash
sudo journalctl -u nginx -f
```

4. Test UDP connectivity:
```bash
sudo tcpdump -i any udp port 443 -n
```

### Rate limiting issues

1. **Rate limiting too aggressive**:
   - Increase the rate limits in `limit_req_zone` directives
   - Increase burst values in `location` blocks
   - Example: Change `rate=10r/s` to `rate=20r/s`

2. **Rate limiting not working**:
   - Check that `limit_req` directives are inside `location` blocks
   - Verify nginx was compiled with `--with-http_limit_req_module`
   - Check shared memory allocation (10m should be sufficient for most sites)

3. **Check rate limiting statistics**:
```bash
# Install nginx module for monitoring (if available)
sudo apt install nginx-module-vts
# Then check: http://localhost/nginx-status
```

4. **Temporarily disable rate limiting** (for testing):
```nginx
# Comment out these lines in the location block:
# limit_req zone=api_limit burst=20 nodelay;
# limit_req_status 429;
```

### Certificate issues

1. Check certbot renewal:
```bash
sudo certbot renew --dry-run
```

2. Verify certificate paths in config match actual paths:
```bash
ls -la /etc/letsencrypt/live/events.example.com/
```

## Alternative: Split-Domain Setup

If you prefer separate hostnames for API and web frontend, see the commented section at the bottom of `dansal.conf`.

Example:
- `https://api.example.com` → REST API only
- `https://events.example.com` → Web frontend only

## Using Makefile with Certbot

The `deploy-nginx` Makefile target is designed to work seamlessly with certbot:

### Complete Deployment Workflow

```bash
# 1. Install dansal and dansal-web
sudo make install
sudo make install-web

# 2. Get TLS certificate with certbot
sudo certbot certonly --nginx -d your-domain.com

# 3. Deploy nginx configuration (auto-replaces domain)
sudo make deploy-nginx

# 4. Verify everything works
sudo nginx -t
sudo systemctl reload nginx
sudo certbot renew --dry-run
```

### Domain Replacement

The `deploy-nginx` target automatically:
1. Reads `domain:` from `/etc/dansal/web.yaml`
2. Replaces `events.example.com` with your actual domain
3. Deploys to `/etc/nginx/sites-available/dansal`

Example `/etc/dansal/web.yaml`:
```yaml
domain: "your-domain.com"
```

### Certbot Renewal Hooks

To ensure nginx reloads after certificate renewal, create a renewal hook:

```bash
sudo mkdir -p /etc/letsencrypt/renewal-hooks/deploy/
sudo nano /etc/letsencrypt/renewal-hooks/deploy/dansal-reload.sh
```

Add this content:
```bash
#!/bin/bash
systemctl reload nginx
```

Make it executable:
```bash
sudo chmod +x /etc/letsencrypt/renewal-hooks/deploy/dansal-reload.sh
```

### Verifying Certbot Integration

Check that certbot recognizes your nginx configuration:

```bash
# List certbot certificates
sudo certbot certificates

# Check renewal configuration
sudo certbot show-renewal

# Test renewal process
sudo certbot renew --dry-run
```

## Upgrading from HTTP/2-only

If you're upgrading from a previous HTTP/2-only configuration:

1. Backup your current config:
```bash
sudo cp /etc/nginx/sites-available/dansal /etc/nginx/sites-available/dansal.backup
```

2. Replace with the new template and customize

3. Reload nginx:
```bash
sudo systemctl reload nginx
```

4. Monitor for any issues:
```bash
sudo tail -f /var/log/nginx/error.log
```

## Performance Tuning

For high-traffic sites, consider adding these to your nginx config:

```nginx
# In main context (outside server blocks)
worker_processes auto;
worker_rlimit_nofile 100000;

# In http context
types_hash_max_size 4096;
server_names_hash_bucket_size 128;

# For HTTP/3 UDP performance
quic_host_key /etc/nginx/quic_key;
quic_gso on;
```

Create QUIC host key:
```bash
sudo openssl rand -hex 32 | sudo tee /etc/nginx/quic_key
sudo chmod 600 /etc/nginx/quic_key
```