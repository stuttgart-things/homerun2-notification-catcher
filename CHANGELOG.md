# [3.7.0](https://github.com/stuttgart-things/homerun2-notification-catcher/compare/v3.6.0...v3.7.0) (2026-10-21)


### Features

* **notify:** carry the objectId into the Teams card and webhook payload ([06d3d98](https://github.com/stuttgart-things/homerun2-notification-catcher/commit/06d3d9887cbfa0e48b933fb9ba69c4efe76c42f0)), closes [#64](https://github.com/stuttgart-things/homerun2-notification-catcher/issues/64) [#49](https://github.com/stuttgart-things/homerun2-notification-catcher/issues/49) [#55](https://github.com/stuttgart-things/homerun2-notification-catcher/issues/55)

# [3.6.0](https://github.com/stuttgart-things/homerun2-notification-catcher/compare/v3.5.0...v3.6.0) (2026-10-21)


### Features

* **sink:** assert-card — wait for a notification in the sink ([03f8991](https://github.com/stuttgart-things/homerun2-notification-catcher/commit/03f8991d370d40ddc20faa6048ed1d50a83d0f52)), closes [#62](https://github.com/stuttgart-things/homerun2-notification-catcher/issues/62) [#55](https://github.com/stuttgart-things/homerun2-notification-catcher/issues/55)

# [3.5.0](https://github.com/stuttgart-things/homerun2-notification-catcher/compare/v3.4.0...v3.5.0) (2026-10-21)


### Features

* **kcl:** publish the sink as its own kustomize artifact ([624d46f](https://github.com/stuttgart-things/homerun2-notification-catcher/commit/624d46f964b7ee06ce5685b9bc0bfe1546fe0804)), closes [#55](https://github.com/stuttgart-things/homerun2-notification-catcher/issues/55)

# [3.4.0](https://github.com/stuttgart-things/homerun2-notification-catcher/compare/v3.3.1...v3.4.0) (2026-10-21)


### Features

* **notify:** log each delivery at INFO ([#60](https://github.com/stuttgart-things/homerun2-notification-catcher/issues/60)) ([6d597d0](https://github.com/stuttgart-things/homerun2-notification-catcher/commit/6d597d0fd207b0ee881a1c4c85d3e3c49870bbe3))
* **sink:** serve the page's assets from the binary, no CDN ([007913b](https://github.com/stuttgart-things/homerun2-notification-catcher/commit/007913ba8cc358ed33bccaf0ad8b2285cce5a98d)), closes [#55](https://github.com/stuttgart-things/homerun2-notification-catcher/issues/55)

## [3.3.1](https://github.com/stuttgart-things/homerun2-notification-catcher/compare/v3.3.0...v3.3.1) (2026-10-21)


### Bug Fixes

* **deps:** update module github.com/alicebob/miniredis/v2 to v2.40.0 ([044eef3](https://github.com/stuttgart-things/homerun2-notification-catcher/commit/044eef317529ee996b07864bed9314ccaf009a87))

# [3.3.0](https://github.com/stuttgart-things/homerun2-notification-catcher/compare/v3.2.1...v3.3.0) (2026-10-21)


### Features

* **kcl:** deploy the notification sink (Deployment, Service, HTTPRoute) ([83709f0](https://github.com/stuttgart-things/homerun2-notification-catcher/commit/83709f0c2aa43e59cc00d263be851bd4864fd8ff)), closes [#54](https://github.com/stuttgart-things/homerun2-notification-catcher/issues/54)

## [3.2.1](https://github.com/stuttgart-things/homerun2-notification-catcher/compare/v3.2.0...v3.2.1) (2026-10-21)


### Bug Fixes

* **deps:** build with Go 1.26.9, required by homerun-library v4.5.2 ([8028184](https://github.com/stuttgart-things/homerun2-notification-catcher/commit/8028184dea18781a0ea48affbc4d5afeac814f5e))
* **deps:** update module github.com/stuttgart-things/homerun-library/v4 to v4.5.2 ([5238a97](https://github.com/stuttgart-things/homerun2-notification-catcher/commit/5238a97142980074be23cd089fed61e7e057168f))

# [3.2.0](https://github.com/stuttgart-things/homerun2-notification-catcher/compare/v3.1.0...v3.2.0) (2026-10-21)


### Features

* generic notification sink in the homerun2-core-catcher design ([0841006](https://github.com/stuttgart-things/homerun2-notification-catcher/commit/0841006631169bb9a79cc7d96fc713728bce91e0)), closes [#52](https://github.com/stuttgart-things/homerun2-notification-catcher/issues/52)
* **sink:** notification list with channels column, tabs per system ([78fe743](https://github.com/stuttgart-things/homerun2-notification-catcher/commit/78fe7432043fa0ed882e02d79e32a3bfe395447f)), closes [#52](https://github.com/stuttgart-things/homerun2-notification-catcher/issues/52)
* teams-mock subcommand, a Teams-shaped webhook sink with HTML card view and test API ([1497fdf](https://github.com/stuttgart-things/homerun2-notification-catcher/commit/1497fdff19113d2c41bb348d80ffcf9aac94f461)), closes [#52](https://github.com/stuttgart-things/homerun2-notification-catcher/issues/52)

# [3.1.0](https://github.com/stuttgart-things/homerun2-notification-catcher/compare/v3.0.3...v3.1.0) (2026-10-18)


### Features

* tags_exclude filter and configurable Teams card facts ([b902209](https://github.com/stuttgart-things/homerun2-notification-catcher/commit/b902209678bca66f44ab9d1f5a31bb191bf96bf1))

## [3.0.3](https://github.com/stuttgart-things/homerun2-notification-catcher/compare/v3.0.2...v3.0.3) (2026-10-07)


### Bug Fixes

* **teams:** give the Adaptive Card a plain-text fallbackText ([3e2df09](https://github.com/stuttgart-things/homerun2-notification-catcher/commit/3e2df090c0d75fcc9194e2579e3c0191944256c6)), closes [stuttgart-things/stuttgart-things#3490](https://github.com/stuttgart-things/stuttgart-things/issues/3490)

## [3.0.2](https://github.com/stuttgart-things/homerun2-notification-catcher/compare/v3.0.1...v3.0.2) (2026-09-25)


### Bug Fixes

* check the Close errors when the catcher fails to start ([93f2d7c](https://github.com/stuttgart-things/homerun2-notification-catcher/commit/93f2d7cc66daf1ba8e08b6b9c942fd93dd5f28e9)), closes [#43](https://github.com/stuttgart-things/homerun2-notification-catcher/issues/43)
* start new consumer groups at $ and handle messages in stream order ([0c94354](https://github.com/stuttgart-things/homerun2-notification-catcher/commit/0c94354f29767f9e5c00ef1838598f74c454ebcd)), closes [stuttgart-things/flux#431](https://github.com/stuttgart-things/flux/issues/431) [#43](https://github.com/stuttgart-things/homerun2-notification-catcher/issues/43)

## [3.0.1](https://github.com/stuttgart-things/homerun2-notification-catcher/compare/v3.0.0...v3.0.1) (2026-09-12)


### Bug Fixes

* wait for Redis at startup instead of exiting, and stop waiting on SIGTERM ([3106063](https://github.com/stuttgart-things/homerun2-notification-catcher/commit/31060633c5a112b888b36441d0c493956e497c2c)), closes [#40](https://github.com/stuttgart-things/homerun2-notification-catcher/issues/40)

# [3.0.0](https://github.com/stuttgart-things/homerun2-notification-catcher/compare/v2.1.2...v3.0.0) (2026-08-20)


* chore(deps)!: migrate to homerun-library v4 ([#38](https://github.com/stuttgart-things/homerun2-notification-catcher/issues/38)) ([6c1d03e](https://github.com/stuttgart-things/homerun2-notification-catcher/commit/6c1d03e31e740f416e80895843be66298e5c1275)), closes [homerun-library#103](https://github.com/homerun-library/issues/103)


### BREAKING CHANGES

* homerun-library moved to the /v4 module path and renamed
Message.Url to Message.URL.

- Import path github.com/stuttgart-things/homerun-library/v3 -> /v4.
- Message.Url -> Message.URL. The JSON tag stays "url", so nothing on the wire,
  in Redis JSON or in the RediSearch index changes; this is a compile-time
  rename only.

What v4 brings beyond the rename:

- RediSearch indexes the message's own timestamp instead of the moment of
  indexing, as a NUMERIC field so range queries are expressible at all

## [2.1.2](https://github.com/stuttgart-things/homerun2-notification-catcher/compare/v2.1.1...v2.1.2) (2026-05-28)


### Bug Fixes

* **kcl:** stop emitting Namespace from kustomize OCI ([6dc22d7](https://github.com/stuttgart-things/homerun2-notification-catcher/commit/6dc22d76616557feed24f2856947cee33bd1d44f)), closes [#25](https://github.com/stuttgart-things/homerun2-notification-catcher/issues/25)

## [2.1.1](https://github.com/stuttgart-things/homerun2-notification-catcher/compare/v2.1.0...v2.1.1) (2026-05-26)


### Bug Fixes

* **config:** walk YAML node tree for env interpolation, ignore comments ([#24](https://github.com/stuttgart-things/homerun2-notification-catcher/issues/24)) ([32f9ef6](https://github.com/stuttgart-things/homerun2-notification-catcher/commit/32f9ef6855dc62b4c1b97e10e5828aea20873259)), closes [#23](https://github.com/stuttgart-things/homerun2-notification-catcher/issues/23)

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
