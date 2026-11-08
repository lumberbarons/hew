# orchestrator

Coordinates the fleet of ingest workers and exposes a control API.

## Quick start

```bash
npm run setup
npm run dev
```

See [the deployment guide](docs/DEPLOY.md) for production.

## Services

The orchestrator supervises two services:

- `worker` — pulls jobs from the queue
- `scheduler` — enqueues jobs on a cron

## Docker

Build and run the container:

```bash
docker build -t orchestrator .
docker run -p 3000:3000 orchestrator
```

## Requirements

- Node 22+
- Redis 7+ (`brew install redis`, or `apt install redis-server`)
- A `.env` file — copy `.env.example` and set `CONTROL_TOKEN` to any random string

## Troubleshooting

- `ECONNREFUSED 127.0.0.1:6379` — Redis isn't running. Start it with `redis-server`.
