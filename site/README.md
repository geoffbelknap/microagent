# microagent-kit docs site

Astro Starlight site for [microagent-kit](https://github.com/geoffbelknap/microagent-kit).

## Develop

```bash
cd site
npm ci
npm run dev   # http://localhost:4321
```

## Build

```bash
npm run build   # outputs site/dist
npm run preview
```

## Deploy

The site is published by Cloudflare Pages from the `main` branch.

Cloudflare Pages settings:

- **Repo**: `geoffbelknap/microagent-kit`
- **Production branch**: `main`
- **Build command**: `cd site && npm ci && npm run build`
- **Build output**: `site/dist`
- **Root directory**: repo root

PR previews are enabled by default. CI also runs `npm run build` on pull
requests via `.github/workflows/docs.yml` so a broken build blocks merge.
