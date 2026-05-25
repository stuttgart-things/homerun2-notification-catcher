# [2.1.0](https://github.com/stuttgart-things/homerun2-notification-catcher/compare/v2.0.1...v2.1.0) (2026-05-25)


### Features

* **kcl:** default redisStream to messages, not alerts ([#21](https://github.com/stuttgart-things/homerun2-notification-catcher/issues/21)) ([79aa966](https://github.com/stuttgart-things/homerun2-notification-catcher/commit/79aa9668cfbae83ecfa9395aebd4fcb71ab6c5da))

## [2.0.1](https://github.com/stuttgart-things/homerun2-notification-catcher/compare/v2.0.0...v2.0.1) (2026-05-25)


### Bug Fixes

* **kcl:** emit Redis password via data not stringData ([#20](https://github.com/stuttgart-things/homerun2-notification-catcher/issues/20)) ([965da33](https://github.com/stuttgart-things/homerun2-notification-catcher/commit/965da332acc1c850eda9c865c32c5f58bc0ba302))

# [2.0.0](https://github.com/stuttgart-things/homerun2-notification-catcher/compare/v1.4.1...v2.0.0) (2026-05-25)


* feat(kcl)!: decouple notify ConfigMap from the chart ([#19](https://github.com/stuttgart-things/homerun2-notification-catcher/issues/19)) ([8c9582d](https://github.com/stuttgart-things/homerun2-notification-catcher/commit/8c9582d62c6d3a9937f02dace6b6e69d1e9f40de))


### BREAKING CHANGES

* callers that relied on the chart's inline `<name>-notify`
ConfigMap must now supply their own. See docs/deployment.md.

Co-authored-by: Claude Opus 4.7 (1M context) <noreply@anthropic.com>

## [1.4.1](https://github.com/stuttgart-things/homerun2-notification-catcher/compare/v1.4.0...v1.4.1) (2026-05-25)


### Bug Fixes

* quote $${VAR} placeholders in notifyConfig + README tryout section ([ca44346](https://github.com/stuttgart-things/homerun2-notification-catcher/commit/ca44346dd5374c60775ec518ec3fb2c8539fd7c9))

# [1.4.0](https://github.com/stuttgart-things/homerun2-notification-catcher/compare/v1.3.0...v1.4.0) (2026-05-25)


### Features

* DRY_RUN mode — log which outputs would fire without sending ([472d23e](https://github.com/stuttgart-things/homerun2-notification-catcher/commit/472d23ed93fde05d19f0bcb95313ccd0df07b522))

# [1.3.0](https://github.com/stuttgart-things/homerun2-notification-catcher/compare/v1.2.0...v1.3.0) (2026-05-25)


### Features

* KCL manifests + Flux deployment surface ([10d2e3b](https://github.com/stuttgart-things/homerun2-notification-catcher/commit/10d2e3bb1fff4eb4300ed5a14222cfed38d600e6))

# [1.2.0](https://github.com/stuttgart-things/homerun2-notification-catcher/compare/v1.1.0...v1.2.0) (2026-05-25)


### Features

* YAML-driven multi-sink dispatcher + smoke subcommand ([31e721a](https://github.com/stuttgart-things/homerun2-notification-catcher/commit/31e721a122b0be7bf522619f940d8b28317691d2))

# [1.1.0](https://github.com/stuttgart-things/homerun2-notification-catcher/compare/v1.0.0...v1.1.0) (2026-05-24)


### Features

* add notify package with MS Teams notifier ([a32e4fd](https://github.com/stuttgart-things/homerun2-notification-catcher/commit/a32e4fd5d8e131c0a138674060cf8ce65a4ec477)), closes [#6](https://github.com/stuttgart-things/homerun2-notification-catcher/issues/6)

# 1.0.0 (2026-05-24)


### Features

* scaffold notification-catcher from core-catcher ([fbae1cc](https://github.com/stuttgart-things/homerun2-notification-catcher/commit/fbae1ccd34daa841ebaaf30a6d22023383b10763)), closes [#6](https://github.com/stuttgart-things/homerun2-notification-catcher/issues/6) [#5](https://github.com/stuttgart-things/homerun2-notification-catcher/issues/5) [#5](https://github.com/stuttgart-things/homerun2-notification-catcher/issues/5)
