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