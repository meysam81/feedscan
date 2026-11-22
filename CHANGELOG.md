# Changelog

## [0.0.4](https://github.com/meysam81/feedscan/compare/v0.0.3...v0.0.4) (2026-11-22)


### Bug Fixes

* **checkpoint:** make file lock build on windows ([#17](https://github.com/meysam81/feedscan/issues/17)) ([c4aac6b](https://github.com/meysam81/feedscan/commit/c4aac6b91b8dc72912c527e44a854c2ac919261c))

## [0.0.3](https://github.com/meysam81/feedscan/compare/v0.0.2...v0.0.3) (2026-11-22)


### ⚠ BREAKING CHANGES

* a bare `feedscan` lists posts instead of scanning, also when FEEDSCAN_URL is set. Use `feedscan scan` to scan.

### Features

* add cursor and move the logic to internal ([998d063](https://github.com/meysam81/feedscan/commit/998d06381b846593ae75493fc204b93c520cfb4b))
* custom http headers from cli and file ([86ff288](https://github.com/meysam81/feedscan/commit/86ff288f9323f7198a1a1dd32579dad24cfe25d4))
* list blog posts by default ([#16](https://github.com/meysam81/feedscan/issues/16)) ([305b6c3](https://github.com/meysam81/feedscan/commit/305b6c3bd381ca37244f54dd4897567e5e830ced))


### Bug Fixes

* **deps-security:** update module golang.org/x/net to v0.62.0 [security] ([#13](https://github.com/meysam81/feedscan/issues/13)) ([9251039](https://github.com/meysam81/feedscan/commit/92510393b8e39edf416cbbc2b5074f408e4603a3))
* do not track checkpoint files ([449b1af](https://github.com/meysam81/feedscan/commit/449b1afc21d1831172a9a91adc665e0631411eae))
* use dynamic domain name for checkpoint generation by default ([d89d175](https://github.com/meysam81/feedscan/commit/d89d17510e734862151905c442ce3fad058a546a))


### Build & Dependencies

* **deps:** update actions/checkout action to v7 ([#8](https://github.com/meysam81/feedscan/issues/8)) ([716dd3a](https://github.com/meysam81/feedscan/commit/716dd3a39de22bb375535cdd0e14f2ecb77c3c0f))
* **deps:** update actions/setup-go action to v7 ([#9](https://github.com/meysam81/feedscan/issues/9)) ([91778d7](https://github.com/meysam81/feedscan/commit/91778d7af3c2f817d8ba37a4eefe47e9c52834dc))
* **deps:** update goreleaser/goreleaser-action action to v7 ([#11](https://github.com/meysam81/feedscan/issues/11)) ([17eebfa](https://github.com/meysam81/feedscan/commit/17eebfa832a3f61c3d5c763ac3a63553d80b2f0b))
* **deps:** update non-major dependencies ([#4](https://github.com/meysam81/feedscan/issues/4)) ([4c0dca7](https://github.com/meysam81/feedscan/commit/4c0dca78a8eb6c7551f8fbd3de1f3d6575e0c3cd))


### Chores

* **pre-commit:** freeze hooks to commit SHAs ([#15](https://github.com/meysam81/feedscan/issues/15)) ([5ca4927](https://github.com/meysam81/feedscan/commit/5ca492735ea2f60cc70a7422e7e94f42010f53ff))


### CI

* pin actions to commit SHAs, release-please manifest config ([#12](https://github.com/meysam81/feedscan/issues/12)) ([1dd2246](https://github.com/meysam81/feedscan/commit/1dd2246d5db8c042044119bb2eac9512bbf527bc))

## [0.0.2](https://github.com/meysam81/feedscan/compare/v0.0.1...v0.0.2) (2026-05-09)


### Features

* **CI:** publish brew using casks ([379ede9](https://github.com/meysam81/feedscan/commit/379ede9b5d586fecb3a174f72e2ddc2007c110a5))

## 0.0.1 (2026-05-08)


### Features

* add max-urls and sort ([73afccb](https://github.com/meysam81/feedscan/commit/73afccb14fdb3867c000350a6ce056a0d3192e87))
* **CI:** add release job ([3a1b08f](https://github.com/meysam81/feedscan/commit/3a1b08f0a854720e0426fef4e5a06ec14b226475))
* initial commit ([05449bf](https://github.com/meysam81/feedscan/commit/05449bfa438f64674b9bd13b730de90fa5f0aa94))
* initial commit ([7e77315](https://github.com/meysam81/feedscan/commit/7e77315309a256db32fe55c9171959631d5e6870))
