# Performance Optimization

## Issue
Improve backend and frontend performance to handle increased mobile traffic and provide faster response times.

## Current State
- No caching layer implemented
- Database queries could be optimized
- Image handling is basic
- No API response compression
- Limited frontend performance optimizations

## Proposed Improvements

### 1. Caching Layer
- **Redis integration**: Cache frequent queries and API responses
- **Two-level caching**: Memory cache + Redis fallback
- **Cache invalidation**: Smart invalidation on data changes
- **Rate limiting**: Per-IP and per-user rate limits

### 2. Database Optimization
- **Additional indexes**: For common event queries
- **Query optimization**: Analyze and optimize slow queries
- **Connection pooling**: Better database connection management
- **Read replicas**: For scaling read operations

### 3. Image Handling
- **Automatic resizing**: Create multiple sizes on upload
- **Smart compression**: Optimize JPEG/PNG quality
- **Lazy loading**: Only load visible images
- **CDN integration**: For static asset delivery

### 4. API Performance
- **Response compression**: Gzip/Brotli compression
- **Pagination**: Consistent pagination for all list endpoints
- **Field selection**: Allow clients to request specific fields
- **Batching**: Support for batch requests

### 5. Frontend Optimization
- **Code splitting**: Load only necessary JavaScript
- **Tree shaking**: Remove unused code
- **Bundle optimization**: Reduce bundle sizes
- **Critical CSS**: Inline critical styles

## Technical Implementation

### Backend Changes
```go
// Add caching middleware
func (s *Server) withCaching(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // Check cache
        cacheKey := r.URL.String()
        if cached, found := cache.Get(cacheKey); found {
            w.Write(cached.([]byte))
            return
        }
        
        // Cache miss - proceed and cache response
        rec := httptest.NewRecorder()
        next.ServeHTTP(rec, r)
        
        if rec.StatusCode == http.StatusOK {
            cache.Set(cacheKey, rec.Body.Bytes(), cache.DefaultExpiration)
        }
        
        for k, v := range rec.Header() {
            w.Header()[k] = v
        }
        w.WriteHeader(rec.Code)
        w.Write(rec.Body.Bytes())
    })
}

// Add database indexes
func migrate() {
    // Add indexes for common queries
    db.Exec(`CREATE INDEX IF NOT EXISTS idx_events_start ON events(start_time)`)
    db.Exec(`CREATE INDEX IF NOT EXISTS idx_events_location ON events(location_id)`)
    db.Exec(`CREATE INDEX IF NOT EXISTS idx_events_organization ON events(organization_id)`)
}
```

### Configuration Additions
```yaml
server:
  # Cache settings
  cache_enabled: true
  cache_ttl_minutes: 30
  redis_url: "redis://localhost:6379/0"
  
  # Performance settings
  gzip_enabled: true
  max_body_bytes: 2097152  # 2 MiB
  
  # Rate limiting
  rate_limit: 100
  rate_limit_window: 60
  
web:
  # Frontend optimization
  bundle_analyzer: true
  minify_assets: true
  critical_css: true
```

## Acceptance Criteria
- [ ] Redis caching implemented and working
- [ ] Database indexes added for common queries
- [ ] API response times improved by ≥40%
- [ ] Image optimization reduces file sizes by ≥50% without quality loss
- [ ] Frontend bundle sizes reduced by ≥30%
- [ ] Rate limiting prevents abuse while allowing legitimate use
- [ ] All changes maintain data integrity

## Priority
Medium-High - Performance directly impacts user experience

## Dependencies
- Redis server for caching (optional but recommended)

## Estimated Effort
Medium (4-6 days)

## Related Issues
- None currently

## Benchmarking
Before implementing, establish baseline metrics:
- API response times for key endpoints
- Database query execution times
- Frontend bundle sizes
- Page load times on mobile networks

After implementation, verify improvements against these baselines.

## Notes
- Ensure cache invalidation works correctly to prevent stale data
- Monitor Redis memory usage and set appropriate limits
- Consider using a managed Redis service for production