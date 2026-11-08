# pipeline

Batch ETL pipeline for analytics events.

## Configuration

The pipeline reads the following environment variables:

| Variable | Purpose |
|----------|---------|
| `DATABASE_URL` | Postgres connection string |
| `S3_BUCKET` | Bucket for raw event dumps |
| `BATCH_SIZE` | Rows per batch |

## Commands

- `npm run ingest` — pull new events
- `npm run transform` — normalise and dedupe
- `npm run load` — write to the warehouse

## Requirements

- Node 20+
