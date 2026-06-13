# Changelog

## [1.1.0](https://github.com/voujr/voujr/compare/v1.0.0...v1.1.0) (2026-06-12)


### Features

* **cmd:** add the voujr CLI entrypoint and command wiring ([4bf14f5](https://github.com/voujr/voujr/commit/4bf14f57ee469bb6d750b6d6724ae789fad6f38e))


### Bug Fixes

* track cmd/voujr, the CLI entrypoint hidden by .gitignore ([4916ef5](https://github.com/voujr/voujr/commit/4916ef53defe986f70ec432db5988d5b2e962b80))

## 1.0.0 (2026-06-12)


### Features

* **ai:** real embeddings + end-to-end long-term memory + provider failover ([e6cfbac](https://github.com/voujr/voujr/commit/e6cfbac6a575f3a0e8a86d9fb567f181185431ac))
* **audit:** wire the audit engine into the CLI and agent, expand rule library ([83888c9](https://github.com/voujr/voujr/commit/83888c9d534b8794e6090e13f5e8efacc49984b5))
* **controller:** continuous-audit controller, Helm chart, Postgres store seam ([733cc44](https://github.com/voujr/voujr/commit/733cc44ba654c8376c56f180f0e8b797b0cda994))
* multi-cluster switching + AES-GCM encryption at rest ([5f99bce](https://github.com/voujr/voujr/commit/5f99bce08bd9f3aa642870a5f5260d86e503e256))
* **observability:** emit Prometheus metrics + wire Slack/PagerDuty alerts ([727c1b5](https://github.com/voujr/voujr/commit/727c1b5d23c07a4f59e5e638aea2fff40205aa24))
* scaffold voujr, a terminal-native Kubernetes AI agent ([2d6aa67](https://github.com/voujr/voujr/commit/2d6aa67be27f35f25561239f7e7988f77b1beb73))
* **session:** add --resume, session listing, and AI usage accounting ([f746c8e](https://github.com/voujr/voujr/commit/f746c8eeeccf7803de799558073e3e097aadc7fd))
* **store:** persist sessions, messages, executions, and a hash-chained audit log ([2880b41](https://github.com/voujr/voujr/commit/2880b416467a9929d30fb3ee1af16f3891e809d9))
* **store:** Postgres backend (pgx), verified against a real Postgres ([d50f04a](https://github.com/voujr/voujr/commit/d50f04aaed05df4db31bcf8d9da549333f58c4a0))
* **tools:** add describe/logs/events/rollout-restart and prometheus_query ([7431b92](https://github.com/voujr/voujr/commit/7431b92070f29cdda9b7f057c7c4713cfa7b50c2))


### Bug Fixes

* **docs:** update security contact email in SECURITY.md ([6cdbda2](https://github.com/voujr/voujr/commit/6cdbda2308cbbac7a4e4fcb38d7e7af5f1e647b3))
