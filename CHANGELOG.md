# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.1.1] - 2025-01-XX

### Fixed
- **CRITICAL BUG**: Fixed infinite redeploy loop affecting all ZarfPackages after installation
  - **Root Cause**: `handleInstalled()` was unconditionally updating `LastAppliedSpecHash` on every observation, triggering continuous deployment churn
  - **Impact**: All installed packages would repeatedly cycle between "DeploymentCancelled" and "Redeploying" states
  - **Fix**: Removed line 398 that caused hash updates during observation - hash now only updates on initial creation or after successful deployment
  - **Prevention**: Added comprehensive unit tests to ensure hash stability across multiple observations
- **Log Formatting**: Fixed Zarf library logs appearing as raw JSON strings
  - **Issue**: Logs displayed as compact JSON blobs like `{"time":"...","level":"INFO","msg":"deploying component","name":"cert-manager"}` making troubleshooting difficult
  - **Fix**: Implemented JSON parsing in `logrSlogBridge` to extract structured fields and emit clean key-value pairs
  - **Result**: Logs now appear as `deploying component {"name": "cert-manager"}` with proper structure
  - **Impact**: Significantly improved operator/developer experience when debugging deployments

### Added
- Unit test `TestHandleInstalledHashStability`: Validates hash doesn't change when already set and spec hasn't changed
- Unit test `TestSpecHashIdempotency`: Verifies hash computation produces identical results across 100 iterations
- Unit test `TestMultipleObservationsNoHashChange`: Tests 10 consecutive observations don't trigger hash changes
- Helper functions for structured logging: `tryLogJSON()`, `logAtLevel()`, `extractKeyValuePairs()`, `getStringField()`

## [1.1.0] - 2025-01-XX

### Added
- **Architecture Auto-Detection**: ZarfPackage `architecture` field is now optional - automatically detects cluster architecture from node labels
- **Enhanced Logging**: Support for `ZARF_LOG_LEVEL` environment variable (DEBUG, INFO, WARN, ERROR) with structured JSON logging
- **Comprehensive Testing**: 30+ edge case unit tests covering architecture detection, component filtering, variables, sources, and timeouts
- **Performance Benchmarks**: 6 benchmark tests validating reconciliation performance (<100ms target achieved at 1.4μs)
- **E2E Test Suite**: 4 E2E test files covering architecture detection, CLI tools, concurrent deployments, and security
- **Security Documentation**: Complete SECURITY.md with RBAC review, security context configuration, CVE scanning, and compliance guidelines
- **Production Security Context**: Read-only root filesystem support with proper volume mounts
- **CLI Tools in Image**: kubectl and busybox utilities included in provider image for debugging
- Helper functions: `countNodeArchitectures()` and `findMajorityArchitecture()` for cleaner architecture detection

### Changed
- Architecture detection now filters unschedulable nodes (`node.Spec.Unschedulable`) per requirements
- Architecture detection supports both `kubernetes.io/arch` and legacy `beta.kubernetes.io/arch` labels
- Improved logging throughout with structured fields for better observability
- golangci-lint version updated to v2.5.0 (from v2.1.2)
- Refactored `detectClusterArchitecture()` to reduce cyclomatic complexity from 11 to under 10

### Fixed
- **Critical Bug**: Architecture detection was not filtering unschedulable nodes, causing incorrect architecture selection in some clusters
- Applied De Morgan's law to hex character validation in tests (staticcheck QF1001)
- Code formatting issues (gofmt) in multiple files

### Documentation
- Updated README.md with architecture auto-detection documentation and examples
- Added SECURITY.md (485 lines) covering RBAC, security context, secrets, image security, network policies, and compliance
- Added ENHANCED_TESTING_SUMMARY.md documenting all testing improvements
- Added example: `architecture-optional.yaml` demonstrating optional architecture field
- Updated TASKS.md with completed Enhanced Testing Requirements (88% complete)

### Performance
- No-op reconciliation: **1.4μs** (71,000x faster than 100ms target)
- Architecture detection (10 nodes): **75.6μs**
- Spec hash computation: **0.86μs** (normal), **10.8μs** (50 components, 100 variables)
- Concurrent reconciliation: **0.59μs** (no degradation)

## [1.0.1] - 2025-XX-XX

### Fixed
- Initial bug fixes and improvements

## [1.0.0] - 2025-XX-XX

### Added
- Initial release of provider-zarf
- Support for deploying Zarf packages from OCI registries
- Component filtering and variable substitution
- Circuit breaker for failed deployments
- Finalizer-based cleanup

[Unreleased]: https://github.com/enel1221/provider-zarf/compare/v1.0.1...HEAD
[1.0.1]: https://github.com/enel1221/provider-zarf/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/enel1221/provider-zarf/releases/tag/v1.0.0
