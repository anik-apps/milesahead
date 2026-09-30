---
title: Miles Ahead
---

{{< hextra/hero-badge >}}
  <span>Self-Hosted</span>
  {{< icon name="arrow-circle-right" attributes="height=14" >}}
{{< /hextra/hero-badge >}}

<div class="hx-mt-6 hx-mb-6">
{{< hextra/hero-headline >}}
  AI-Powered Miles &amp; Points News
{{< /hextra/hero-headline >}}
</div>

<div class="hx-mb-12">
{{< hextra/hero-subtitle >}}
  A self-hosted content platform that scans miles-and-points blogs, clusters related stories, and uses AI to generate verified articles with structured metadata, reference pages, images, and a daily newsletter.
{{< /hextra/hero-subtitle >}}
</div>

<div class="hx-mb-6">
{{< hextra/hero-button text="Get Started" link="docs/getting-started" >}}
{{< hextra/hero-button text="GitHub" link="https://github.com/anik-apps/milesahead" style="alt" >}}
</div>

<div class="hx-mt-6"></div>

{{< hextra/feature-grid >}}
  {{< hextra/feature-card
    title="Automated Scanning"
    subtitle="RSS and scrape-based scanner monitors miles-and-points blogs on configurable intervals. Content-hash deduplication prevents reprocessing."
    icon="rss"
  >}}
  {{< hextra/feature-card
    title="AI Pipeline"
    subtitle="Generate-critique-revise loop using Nemotron for generation and a separate critic chain for verification. Critic scoring with hallucination caps ensures factual accuracy."
    icon="sparkles"
  >}}
  {{< hextra/feature-card
    title="Reference Pages"
    subtitle="52 curated credit card pages across 6 categories with structured sections, Scout's Verdict, and automatic cross-linking from articles."
    icon="book-open"
  >}}
  {{< hextra/feature-card
    title="Admin Dashboard"
    subtitle="Review, approve, reject, or request revisions on generated articles. Manage sources, reference pages, and newsletter subscribers."
    icon="adjustments"
  >}}
  {{< hextra/feature-card
    title="Image Pipeline"
    subtitle="Searches 5 free image APIs in parallel, then scores candidates with a text pre-filter and Llama Vision model for relevance."
    icon="photograph"
  >}}
  {{< hextra/feature-card
    title="Zero Cost"
    subtitle="Runs on a free-tier OCI ARM instance with SQLite, free NVIDIA API endpoints, and free image APIs. No cloud databases or paid services required."
    icon="currency-dollar"
  >}}
{{< /hextra/feature-grid >}}
