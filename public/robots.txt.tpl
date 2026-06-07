# Dansal Robots.txt - Optimized for SEO and Crawl Efficiency
# Generated: {{DATE}}
# Contact: webmaster@balfolk-jetzt

# Global Rules - Allow important public content
User-agent: *
Allow: /$
Allow: /events/
Allow: /locations/
Allow: /about
Allow: /contact
Allow: /help
Allow: /faq

# Disallow private/admin areas
Disallow: /admin/
Disallow: /api/

# Disallow search results and dynamic pages
Disallow: /search?
Disallow: /results?
Disallow: /filter?
Disallow: /sort?

# Disallow unnecessary file types
Disallow: /*.pdf$
Disallow: /*.zip$
Disallow: /*.doc$
Disallow: /*.xls$
Disallow: /*.ppt$
Disallow: /*.docx$
Disallow: /*.xlsx$
Disallow: /*.pptx$

# Sitemap Reference
Sitemap: {{BASE_URL}}/sitemap.xml

# Search Engine Specific Rules
User-agent: Googlebot
Allow: /events/
Allow: /locations/
Disallow: /admin/

User-agent: Bingbot
Allow: /events/
Allow: /locations/
Disallow: /admin/

User-agent: DuckDuckBot
Allow: /events/
Allow: /locations/
Disallow: /admin/

# Crawl Control for Aggressive Bots
User-agent: *
Crawl-delay: 2
Request-rate: 1/2

# Special rules for known aggressive bots
User-agent: AhrefsBot
Crawl-delay: 5

User-agent: SemrushBot
Crawl-delay: 5

User-agent: PetalBot
Crawl-delay: 5

# Allow health checks and status pages
User-agent: *
Allow: /health
Allow: /status
Allow: /heartbeat

# Block known spam bots
User-agent: spamBot
Disallow: /

User-agent: emailBot
Disallow: /

User-agent: scraperBot
Disallow: /

# End of robots.txt
