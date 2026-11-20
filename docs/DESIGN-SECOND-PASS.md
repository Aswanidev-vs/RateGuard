# Design second pass — make it unmistakably a rate-limiting product

Applied the `frontend-design` skill again after user feedback that the visuals looked fine but did not read as "rate limiting as a service". Context7 was used to confirm HTMX indicator and `HX-Trigger` patterns before touching the dashboard.

## Audit finding

The surfaces were well-composed but generic: stat cards and charts could belong to any SaaS. They did not expose the product's actual vocabulary — token buckets, keys, `429`, `Retry-After`, request decisions, quota headers, and spend baselines.

## Direction (unchanged, sharpened)

- **Product:** RateGuard is an HTTP proxy that enforces per-tenant token buckets and reports AWS spend anomalies.
- **Character:** precise, instrumented, operator-grade.
- **World:** a network operations desk where the primary artifacts are HTTP headers, request decisions, and quota meters.
- **Moments:** the decode of a `429` response and the live bucket state.
- **Rule:** every visual must encode a real value the system already produces. No decorative charts.

## Landing page (`docs/`)

- Hero copy now states the mechanism: token bucket per tenant, HTTP headers, no SDK.
- Capability chips are technical: token bucket, `429 + Retry-After`, per-tenant keys, AWS spend alerts.
- The console mockup gained a **live decisions** strip with `200`/`429` rows and a real-looking `429` header block (`Retry-After`, `X-RateLimit-Limit`, `X-RateLimit-Remaining`, `X-RateLimit-Reset`).
- Feature cards were rewritten around actual endpoints and algorithms: `POST /api/v1/tenants`, `429 · Retry-After: 12`, `current > baseline + 2σ`, `GET /proxy/quote`.
- Added an endpoint list to "How it works" and a copyable curl command in the CTA.
- JS now handles the copy button; all other motion remains CSS-only.

## Dashboard (`web/`)

- Demo panel now shows a **token-bucket gauge** driven by the real `X-RateLimit-Remaining` / `X-RateLimit-Limit` headers returned by `/proxy/quote`, plus a latest-response header readout and a rolling decision feed. All updates use `textContent`, never `innerHTML`.
- Overview gained a "Allowed vs blocked" decision panel with a static `429` header example, so the main page reads as a proxy console rather than a generic analytics dashboard.
- Anomaly records now compare baseline, threshold, and current value as three bars — the actual anomaly rule made visible.
- Help page gained the response-header contract table.
- Tenant creation uses `hx-indicator` (Context7-confirmed pattern) so the in-flight state is explicit.

## Third pass — brand legibility and honest navigation

- Added `logo-dark.png`, a dark-surface variant where the navy wordmark and gauge needle are rendered in off-white while the teal/orange brand colors stay untouched. It is used in the header, console mockup, and footer; the original `logo.png` stays on light plates and in the README.
- Removed all `width`/`height` attributes from logo `<img>` tags. Sizing is now width-driven only, with `aspect-ratio: 1007 / 166` and `object-fit: contain` locked in CSS, so the crop can never be distorted. Widths were reduced (160px nav, 100px console, 108px flow, 124px footer) to avoid a horizontally stretched appearance.
- Removed the fake `Product` / `Solutions` dropdown carets and the nonsensical `Company` link. Navigation now says exactly what it opens: Features → `#product`, How it works → `#how`, Quick start → Docker guide, Docs → repository README.

## Verification

- `go test ./...` passes, including template rendering.
- `node --check docs/script.js` passes; CSS braces balanced; local asset references resolve.
- No browser screenshot was available, so a final visual pass at 375px and desktop width remains manual.
