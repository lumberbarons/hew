# Price Drop Watchlist

**Slug**: `009-price-drop-watchlist`
**Created**: 2026-09-05
**Status**: Draft

## Description

Shoppers who care about a specific bottle currently have to re-run the same
search every few days to notice a price change. A watchlist lets a signed-in
user mark products they care about and see, on one page, what each one cost when
they added it and what it costs now. The scrapers already record every price
they observe, so the data exists — nothing surfaces it per-user. This is worth
doing now because price movement is the main reason people return to the site,
and the current experience gives them no reason to.

## Prior Decisions

- [ADR-0004 Store prices as integer cents](docs/adr/0004-store-prices-as-integer-cents.md) — the drop shown on the page is computed in cents and formatted once at the edge, never as a float
- [ADR-0009 API keys identify callers, sessions identify people](docs/adr/0009-api-keys-and-sessions.md) — the watchlist is keyed to a session user, so it is unreachable from an API-key caller and the schema must not imply otherwise

## Research

- [RES-0003 How often shoppers revisit the same bottle](docs/research/0003-bottle-revisit-rate.md) — F1 (High): most repeat visits are to a product seen in the last two weeks, so the page shows the current price beside the price at add time and does not need history

## Out of Scope

- Email or push notification when a price drops; the watchlist is pull-only for now
- Watching a whole producer, region, or search query rather than a single product
- Price history charts; the page shows two numbers, not a series

## Assumptions

- A user watches at most a few hundred products, so the page loads the whole list without pagination
- "Price when added" is captured at add time and never recomputed, even if a scrape later corrects it
- Anonymous visitors cannot watch products; the button prompts sign-in instead

## User Stories

### US1 — Watch a product from search results (P1)

As a signed-in shopper, I want to add a bottle to my watchlist from wherever I
see it so that I do not have to hunt for it again later

The watch control sits on the product card in search results and on the product
detail page, and reflects current state without a page reload. Adding the same
product twice is a no-op rather than an error, since the control can be clicked
from two places before the first result lands.

### US2 — Review what I am watching and what changed (P2)

As a signed-in shopper, I want one page listing everything I watch with its
price then and now so that I can see at a glance what has moved

The list sorts by largest drop first, since that is the reason to open the page
at all. A product that has gone out of stock still appears, marked unavailable,
rather than vanishing — disappearing silently would read as a bug.

## Tasks

### US1

- [ ] T001 Add `watchlist_entries` table with a unique constraint on (user, product) — `api/internal/db/migrations`
- [ ] T002 Add `watchProduct` and `unwatchProduct` mutations to the schema — `api/graph/schema.graphqls`
- [ ] T003 Implement the mutation resolvers, treating a duplicate add as success — `api/internal/graph`, `api/internal/repository`
- [ ] T004 Add the watch toggle to the product card and detail view — `webui/src/components`

### US2

- [ ] T005 Add a `watchlist` query returning entries with added-at price and current price — `api/graph/schema.graphqls`, `api/internal/graph`
- [ ] T006 Build the watchlist page, sorted by largest drop, with an unavailable badge — `webui/src/components`, `webui/src/App.tsx`

### Cross-cutting

- [ ] T007 Restrict all watchlist operations to the requesting user's own rows — `api/internal/graph`
- [ ] T008 Add the watchlist link to the signed-in navigation — `webui/src/components`

## Done When

- [ ] A signed-in user can watch and unwatch a product from both search results and the detail page, and the control survives a reload
- [ ] Clicking watch twice on the same product leaves exactly one row and reports success both times
- [ ] The watchlist page lists every watched product with its added-at price and current price, largest drop first
- [ ] A watched product that goes out of stock still appears, marked unavailable
- [ ] A request for another user's watchlist returns that user nothing, verified by an integration test
- [ ] An anonymous visitor sees a sign-in prompt in place of the watch control
