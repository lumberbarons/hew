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

### US1 — Watch a product from search results

As a signed-in shopper, I want to add a bottle to my watchlist from wherever I
see it so that I do not have to hunt for it again later

The watch control sits on the product card in search results and on the product
detail page, and reflects current state without a page reload. Adding the same
product twice is a no-op rather than an error, since the control can be clicked
from two places before the first result lands.

### US2 — Review what I am watching and what changed

As a signed-in shopper, I want one page listing everything I watch with its
price then and now so that I can see at a glance what has moved

The list sorts by largest drop first, since that is the reason to open the page
at all. A product that has gone out of stock still appears, marked unavailable,
rather than vanishing — disappearing silently would read as a bug.

## Work Items

### WI1 — Store watchlist entries (P1)

**Goal** Persist a shopper's saved products and the price at add time.

**Supports** US1, US2

**Where** `api/internal/db/migrations`

**Approach**
- Add a watchlist table keyed by user and product.
- Store the price at add time on each entry.

**Done when**
- The database rejects a second row for the same user and product.
- A saved row retains its added-at price when the product's current price changes.

### WI2 — Add watch and unwatch controls (P1)

**Goal** Let signed-in shoppers manage saved products from search and product detail.

**Supports** US1

**Blocked by** WI1

**Where** `api/graph/schema.graphqls`, `api/internal/graph`, `api/internal/repository`, `webui/src/components`

**Approach**
- Add watch and unwatch mutations scoped to the signed-in user's rows.
- Show a watch control on product cards and detail pages and update it without reloading.

**Done when**
- A shopper can watch and unwatch from both views, and the state survives a reload.
- Watching the same product twice reports success twice and leaves one row.
- A mutation cannot change another user's entry.
- An anonymous visitor sees a sign-in prompt in place of the watch control.

### WI3 — Add the watchlist page and navigation (P2)

**Goal** Give signed-in shoppers one place to see their saved products and price changes.

**Supports** US2

**Blocked by** WI1

**Where** `api/graph/schema.graphqls`, `api/internal/graph`, `webui/src/components`, `webui/src/App.tsx`

**Approach**
- Add a query scoped to the signed-in user's rows.
- List saved products by largest drop, show unavailable products, and link the page from signed-in navigation.

**Done when**
- The page shows added-at and current prices, largest drop first, and marks unavailable products.
- A request cannot read another user's watchlist, verified by an integration test.
- The signed-in navigation links to the watchlist.

## Done When

- A shopper can watch a product from search and find it on the watchlist via signed-in navigation.
- After the product's price changes, the watchlist shows the original and current prices.
