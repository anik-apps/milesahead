# Miles Ahead — Roadmap

## Phase 1: Core Platform (Current)

- [x] Project scaffold and configuration
- [x] SQLite database with 11 tables
- [x] RSS scanner with HTML scraping fallback
- [x] Topic clustering (TF-IDF cosine similarity)
- [x] AI pipeline (Hugging Face: generator + critic)
- [x] Article manager with state machine
- [x] Admin dashboard (review queue, sources, subscribers, settings)
- [x] Public site (magazine grid, light/dark mode, RSS feed)
- [x] Vote system with threshold alerting
- [x] Email outbound (OCI SMTP) and inbound (Gmail IMAP)
- [x] Daily newsletter (newspaper-style digest)
- [x] Scheduler (cron-based background jobs)
- [x] CI/CD (GitHub Actions)
- [x] Deployment configs (systemd, nginx, backup)
- [x] Integration tests

## Phase 2: Polish & Reliability

- [ ] Add config validation on startup (fail fast on missing required values)
- [ ] Graceful shutdown with signal handling (SIGTERM/SIGINT)
- [ ] Structured logging (JSON logs for production)
- [ ] Health check endpoint (`/healthz`)
- [ ] Rate limiter cleanup (evict stale entries periodically)
- [ ] HTTP status code handling improvements in scanner
- [ ] Context propagation through scanner and AI pipeline
- [ ] Full-text search on articles
- [ ] Pagination on public homepage
- [ ] Mobile-responsive CSS improvements

## Phase 3: Enhanced AI

- [ ] Support additional model providers (Groq, Google Gemini, Cloudflare Workers AI)
- [ ] Model A/B testing (compare outputs from different models)
- [ ] Automatic article type detection (deal vs roundup vs summary)
- [ ] Improved topic clustering (content-aware, not just title-based)
- [ ] Article quality scoring based on admin approval/rejection patterns
- [ ] Automatic retry for draft articles with low critic scores

## Phase 4: Multi-User & Auth

- [ ] OAuth/SSO login (Google)
- [ ] Multiple admin users with roles
- [ ] Audit log for admin actions
- [ ] Per-user notification preferences

## Phase 5: Growth Features

- [ ] Full-content RSS feed option
- [ ] Email inbound via dedicated mail server (replace Gmail)
- [ ] Analytics dashboard (page views, popular articles, subscriber growth)
- [ ] API for third-party consumption
- [ ] Webhook integrations (Slack, Discord, Telegram notifications)
- [ ] Social media auto-posting (Twitter/X, Mastodon)
- [ ] SEO improvements (sitemap.xml, structured data)

## Phase 6: Monetization (Future)

- [ ] Affiliate link integration
- [ ] Premium newsletter tier
- [ ] Sponsored content management

---

*Phases are not strictly sequential — items can be picked up based on priority and need.*
