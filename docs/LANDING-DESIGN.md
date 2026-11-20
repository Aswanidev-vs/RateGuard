# Landing page design notes

Built with the `frontend-design` skill in **Build** mode, using the supplied screenshot as the compositional reference. The page is self-contained under `docs/`: `index.html`, `styles.css`, `script.js`, and a local copy of the brand mark.

## Direction

- **Product:** RateGuard protects multi-tenant APIs from abuse and alerts on AWS spend spikes, with a dashboard operators can self-host.
- **Character:** precise, wound-up, dusk-lit.
- **World:** a network operations console overlooking a mountain dusk — field instruments, signal lights, ledger strips, cold air. This is why the hero has a CSS/SVG dusk scene instead of a generic gradient.
- **Moments:** one rich moment only — the dashboard console mockup in the hero, including its request-traffic and spend charts. Everything below it is quiet.
- **Signature:** the brand gauge mark reused as the small tag markers and in the "how it works" flow.
- **Dial:** Medium. A SaaS marketing page gets one distinctive identity and one memorable visual, but conventional navigation and a familiar section order so visitors can scan quickly.

## Plan applied

- **Color:** navy ink (`#0b1723`) for the hero, teal signal (`#12a389`) for primary actions and the highlighted headline line, orange (`#ef7a36`) for blocked/spend warnings, warm off-white for light sections. Chart colors each carry meaning; surfaces stay neutral.
- **Type:** system sans with tight display tracking; monospace only for metrics, ranges, and small technical labels. No external font load.
- **Layout:** centered 1240px shell; asymmetric hero split (copy ~45%, console ~55%); four-up feature grid, a flow strip, then a dark CTA. The console keeps its slight perspective tilt on desktop and flattens on mobile.
- **Principles:** one bold bet (the console). No emoji, no glassmorphism, no glow effects outside the single dusk window light, no scroll hijacking, no looping animation.

## Passes

1. **Spacing** — 4px rhythm, equal card padding, 24–32px section gutters, 44px+ touch targets.
2. **Color** — brand-derived palette; accent carries action and one headline line only; warning orange reserved for blocked requests and cost anomaly.
3. **Icons** — one inline SVG stroke set for feature and flow icons; brand mark used as a quiet marker.
4. **Copy** — product-specific and concrete: "Protect your APIs. Control usage. Keep costs in check.", real feature descriptions, no invented proof or hype words.
5. **Type** — size and color create hierarchy; numbers use tabular monospace; no gradient text.
6. **Layout** — reference composition preserved: nav, dusk hero + console, feature cards, flow, CTA, footer.
7. **Details** — nested radii, hairline borders, visible focus, chart grid with muted labels, realistic mock data.
8. **Visuals** — the dusk scene and both charts are built in CSS/SVG, no stock or placeholder imagery.
9. **Motion** — hover color changes only; `prefers-reduced-motion` respected; JS handles only the mobile menu and footer year.

## Verification and limits

- Local asset references checked; CSS braces balanced; `node --check` passes for `script.js`.
- No browser screenshot was available in this environment, so visual confirmation at 375px and desktop width is still a manual step.
- The highlighted third headline line and the dusk scene are intentional deviations from the skill's "no accent on one headline word" and "no decorative gradients" defaults — they come directly from the supplied reference.
