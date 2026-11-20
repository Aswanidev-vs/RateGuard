# Dashboard UI/UX audit and retrofit

Applied the `frontend-design` skill's Retrofit workflow to the embedded dashboard (`web/templates/`, `web/static/`). Product, features, section order, and all routes stayed the same; only craft changed.

## Direction

- **Product:** An operator console for issuing tenant API keys, watching rate-limit decisions, and catching AWS spend anomalies.
- **Character:** watchful, exact, industrial.
- **World:** a night-shift network operations desk — instrument panels, metering marks, shift logs, ledger strips. The palette is derived from the RateGuard brand mark in `logo.png`: navy neutrals, teal signal, orange warning.
- **Moments:** the one-time key reveal, and the live quote/flood console with its counters.
- **Signature:** the three-bar bucket-level mark in the brand, used consistently as the only decorative glyph.
- **Dial:** low. This is a tool used under pressure; familiar patterns and clear hierarchy come first. Personality lives in type, color, spacing, empty states, and small details.

## Audit — tells found

| Sign | Where |
|---|---|
| Near-black surface with a single acid-green accent used on nearly every eyebrow, link, nav item, badge, dot, and progress fill | `web/static/app.css` (original `--mint`, `.eyebrow`, `.text-link`, `.badge--ok`, `.status-dot`) |
| Colored glows and a decorative full-page grid backdrop | original `body::before`, `.status-dot` box-shadow, `.input:focus` box-shadow |
| Tracked uppercase labels and decorative numbered nav markers | `.eyebrow`, `.nav-label`, `.field label`, `.stat-label`, `.badge`, `layout.html` `01–04`, `anomalies.html` `01–03` |
| Mini progress bars on stat cards | `overview.html` block-rate card |
| Heavy heading weights and negative tracking on small figures | `h1/h2/h3` at 620–650, `.num` |
| Hype copy and middle-dot/wordmark labels | `login.html` "Unlock console", "Node 01 · v…", "RATEGUARD // ADMIN", `partials.html` "API key — shown once" |
| Under-44px touch targets and inconsistent radii | `.btn`, `.btn--small`, 3px radius on both cards and controls |

## Nine passes

1. **Spacing** — normalized page edges, panel/card padding, table cells, and form controls to a 4px rhythm; equal rows and equal card padding; removed leftover one-off values.
2. **Color** — after `logo.png` appeared in the repo root, the palette was derived from the real brand: navy-tinted neutrals, teal signal (`#12a389`), orange warning (`#ef7a36`), and a muted coral for blocked states. The accent was moved out of the chrome: eyebrows and labels are muted, links are quiet text, and the primary button is off-white with dark text. Removed the page grid, status-dot glow, focus glow, and translucent blur. Hairline borders stay quiet.
3. **Icons** — no emoji existed; kept the CSS-only three-bar bucket mark and status dots. Removed decorative numbered markers from navigation and the anomaly legend.
4. **Copy** — "Operator sign in" / "Sign in" replace the greeting-style login; removed "Unlock", node numbering, `//` wordmarks, em-dash labels, and the decorative middle-dot meta strings. No invented claims were added.
5. **Type** — dropped the unavailable Inter reflex stack in favor of the system sans + mono figures; reduced heading weights to 500, removed negative tracking on small numbers, and converted tracked uppercase labels to sentence case at normal tracking.
6. **Layout** — kept the app shell and sidebar proportions; varied card rhythm by keeping the wide hero stat and removing the competing mini bar from the block-rate card. The one-time key panel and demo console remain the two rich moments.
7. **Details** — nested radii (8px panels, 4px controls), a single aligned avatar/number column, 32px avatars, visible focus outline, and 44px primary / 36px compact button heights.
8. **Visuals** — no generated imagery. The signature mark, progress meter, and quote console are built from CSS and real data, as appropriate for an operator tool.
9. **Motion** — unchanged except removal of the glow/focus effects; transitions already name specific properties and `prefers-reduced-motion` is respected. No looping or layout-property animation.

## Brand asset note

`logo.png` appeared in the repository root during this pass. It was used only to derive the palette and to confirm that the existing three-bar CSS mark echoes the logo's speed lines. It was **not** embedded in the dashboard: the PNG has a white background that would fight the dark console, and the mark is already built in CSS. The file remains untracked and uncommitted — include it in `web/static/` only if you want the full wordmark shown somewhere.

## Verification

- `go build ./...`, `go test ./...`, and `go test -race ./...` pass — these render every template through the real renderer in `internal/server/server_test.go`.
- Browser screenshots were not available in this environment, so visual checks were source-level plus template execution; a real browser pass at 375px and desktop is the remaining manual confirmation.

## What to push on next

- If the console still reads too dark, the next deliberate step is lighter instrument surfaces, not another accent color.
- The brand mark can be extended into a usage-level indicator in the sidebar if you want the signature to appear in more than one place.
- Table action buttons are 36px for density; raise them to 44px if touch use matters more than row compactness.
