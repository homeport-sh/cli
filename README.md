# homeport CLI

The command-line tool for [homeport](https://homeport.sh), the hosted
platform for single-binary and static apps.

```sh
curl -fsSL https://homeport.sh/install.sh | sh
homeport help
```

`homeport login`, `deploy`, `logs` and `env` arrive here as the platform's
API opens to the CLI. Today it carries `build-plan`, which reads a
repository - with no configuration file - and says how to build it: the
same detection the platform's builders use. [docs/detection.md](docs/detection.md)
says what it detects - Go, PHP, static sites, and JavaScript apps on Node
or Bun (Next.js, Nuxt, SvelteKit, Astro, React Router, Remix, NestJS,
Elysia, Hono, Fastify, Express) - and how each runs.

The Go packages here (`buildplan`, `envfile`, `artifact`, `cidr`) are shared
with the platform.

## Licence

[Functional Source License, Version 1.1, ALv2 Future License](LICENSE.md):
use, modify and self-host it freely, except to offer a competing service;
each release becomes Apache 2.0 two years after it ships.
