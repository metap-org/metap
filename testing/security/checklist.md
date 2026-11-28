# Checklist test bảo mật

Cập nhật khi thêm hoặc đổi test bảo mật. Mỗi hàng chỉ tới test thật; đọc test để biết chính xác nó
khẳng định gì. Đây là danh sách chọn lọc theo tính chất, không phải mọi test. Có tên ở đây nghĩa là
tính chất đó có test chặn hồi quy, không phải là đã được audit.

## Token và phiên

| Tính chất | File test: hàm |
|---|---|
| Token hết hạn bị từ chối | `pkg/auth/jwks/static_test.go`: `TestDecodeAccessTokenRejectsAnExpiredToken` |
| Token ký bằng khoá khác bị từ chối | `pkg/auth/jwks/static_test.go`: `TestDecodeAccessTokenRejectsATokenSignedByADifferentKey` |
| Khoá đã gỡ khỏi JWKS không còn được chấp nhận | `pkg/auth/jwks/jwks_test.go`: `TestJwksClientRejectsATokenSignedByAKeyNoLongerPublishedInTheJwks` |
| Token của issuer khác bị từ chối | `pkg/auth/jwks/single_issuer_test.go`: `TestAutoVerifierRejectsATokenFromAnotherIssuer` |
| Token cho downstream không dùng được như token nền tảng | `pkg/auth/jwks/downstream_test.go`: `TestADownstreamTokenIsNotAnOrdinaryPlatformToken` |
| Thành viên bị thu hồi hoặc vô hiệu không qua được | `internal/authctx/resolver_test.go`: `TestARevokedOrDisabledMemberGetsNoContextAndNoRoleRead` |
| Vô hiệu, xoá hoặc đổi mật khẩu làm mất phiên | `pkg/auth/identity/identity_postgres_test.go`: `TestSessionsAreRevokedByDisableRemoveAndPasswordChange` |
| Gọi bằng cookie mà thiếu header CSRF bị từ chối | `internal/httpapi/ops_sessions_postgres_test.go`: `TestSessionOperationsOverREST`; `pkg/kit/httpx/cookieauth_test.go`: `TestCsrfMatchesOnlyWhenBothArePresentAndEqual` |

## Đăng nhập

| Tính chất | File test: hàm |
|---|---|
| Khoá tài khoản sau nhiều lần sai, kể cả gửi song song | `pkg/auth/identity/lockout_postgres_test.go`: `TestParallelWrongGuessesCannotOutrunTheLockout`, `TestParallelSignInGuessesLockTheAccount` |
| Tài khoản đang khoá không lộ mật khẩu đúng | `pkg/auth/identity/lockout_postgres_test.go`: `TestALockedSignInDoesNotTellTheRightPasswordApart` |
| Route đăng nhập có rate limit riêng chặt hơn | `internal/httpapi/auth_ratelimit_postgres_test.go`: `TestAuthRoutesShareAStricterPerIPLimit`, `TestSSOStartRoutesShareTheCredentialBucket` |
| Rate limit sau proxy đếm theo người gọi | `pkg/httpmw/ratelimit_behind_test.go`: `TestRateLimitBehindCountsEachCallerSeparately` |
| Mã TOTP không dùng lại được | `pkg/auth/identity/mfa_test.go`: `TestMatchTOTPWindowAndReplay` |
| SAML từ chối phản hồi giả, phát lại, không được yêu cầu; chỉ đăng nhập trình duyệt đã bắt đầu | `internal/httpapi/saml_postgres_test.go`: `TestSAMLRefusesForgedReplayedAndUnsolicitedResponses`, `TestSAMLAnswerOnlySignsInTheBrowserThatStarted` |
| SSO: ràng buộc trình duyệt, địa chỉ đã xác minh, domain đã chứng minh | `internal/httpapi/sso_postgres_test.go`: `TestSSOCallbackNeedsTheBrowserThatStarted`, `TestSSORefusesAnAddressTheProviderDidNotVerify`, `TestSSODomainMustBeVerified` |
| Lời mời không vượt được SSO bắt buộc | `internal/httpapi/sso_postgres_test.go`: `TestAcceptingAnInvitationDoesNotBypassEnforcedSSO` |
| OIDC sai nonce bị từ chối | `pkg/auth/sso/oidc_test.go`: `TestWrongNonceIsRejected` |

## OAuth2 (metap làm server)

| Tính chất | File test: hàm |
|---|---|
| Client secret không lưu dạng rõ; client bị thu hồi không xác thực được | `pkg/auth/oauthserver/oauth_server_postgres_test.go`: `TestClientSecretIsNeverStoredInPlaintextAndVerifiesCorrectly`, `TestRevokedClientFailsSecretVerificationEvenWithTheRightSecret` |
| `redirect_uri` phải khớp | `pkg/auth/oauthserver/oauth_server_postgres_test.go`: `TestAuthorizationCodeRejectsAMismatchedRedirectURI` |
| Dùng lại refresh token thu hồi cả chuỗi | `pkg/auth/oauthserver/oauth_server_postgres_test.go`: `TestRefreshTokenReuseRevokesTheWholeChain` |
| PKCE: verifier sai bị từ chối; client public bắt buộc PKCE | `pkg/auth/oauthserver/pkce_test.go`: `TestVerifyPKCERejectsAWrongVerifier`; `internal/httpapi/oauth2_postgres_test.go`: `TestOAuth2PublicClientRequiresPKCE` |
| Consent chỉ dùng một lần, đúng người | `pkg/auth/oauthserver/consent_postgres_test.go`: `TestPendingAuthorizationIsConsumedExactlyOnce`, `TestConsumePendingAuthorizationRejectsAMismatchedUser` |
| Scope không vượt phần được cấp | `pkg/auth/oauthserver/scope_test.go`: `TestScopeSubsetRejectsAnythingNotGranted` |
| Scope thu hẹp quyền của token (token chỉ đọc không ghi được) | `internal/httpapi/oauth2_postgres_test.go`: `TestOAuth2FullAuthorizationCodeFlowWithPKCEAndScopeEnforcement` |

## Phân quyền

| Tính chất | File test: hàm |
|---|---|
| Mỗi route nằm trong đúng một nhóm quyền; thêm, bớt route là thấy trong diff | `internal/httpapi/route_inventory_postgres_test.go`: `TestRouteInventory` |
| Không có policy khớp thì chỉ admin qua | `internal/httpapi/gate_test.go`: `TestGateAdminPassesAndNoPolicyDeniesEveryoneElse` |
| Thao tác quản trị từ chối đúng người | `internal/httpapi/admin_access_postgres_test.go`: `TestEveryAdministrativeOperationRefusesWhoItShould` |
| Deny thắng allow | `pkg/permission/condition_test.go`: `TestDenyOverridesAllowEvenWhenAnAllowPolicyAlsoMatches` |
| Dòng deny thu hẹp câu `WHERE`, không nới ra | `pkg/query/condition_to_sql_test.go`: `TestDenyRowNarrowsTheWhereClauseWithAndNotInsteadOfWideningItViaOr` |
| Role theo phạm vi không thấy ngoài phạm vi; admin theo phạm vi không phải admin tenant | `pkg/crud/scoped_roles_postgres_test.go`: `TestScopedRoleDoesNotLeakAcrossScopes`; `pkg/permission/scope_test.go`: `TestScopedAdminIsNotATenantAdmin` |
| Phạm vi rỗng từ chối mọi thứ | `pkg/permission/permissionservice_test.go`: `TestAnEmptyScopeDeniesEverythingEvenForAnAdminUser` |
| Backend có gác quyền từ chối trước khi tới dữ liệu; lỗi đọc policy không thành allow | `pkg/crud/gated_test.go`: `TestGatedRefusesEveryOperationBeforeTheBackendWhenNothingIsAllowed`, `TestGatedAPolicyFailureIsAnErrorNotAnAllow` |
| Action khai báo đòi quyền entity | `internal/httpapi/routes_actions_postgres_test.go`: `TestDeclaredActionRequiresEntityPermission` |
| Lọc chỉ theo field list view khai | `pkg/query/query_planner_test.go`: `TestPlanListRefusesAFilterNotInTheListViewsFilters` |
| `AUTH_CONTEXT_ENTITY`: deny theo thuộc tính có hiệu lực; không đánh giá được thì từ chối; bản ghi không giả được danh tính | `pkg/app/auth_context_postgres_test.go`: `TestADenyOnAContextAttributeHoldsAndFollowsTheRecord`, `TestARequestIsRefusedWhenTheContextAttributesCannotBeRead`, `TestAContextRecordCannotSpeakForTheCaller` |

## Cô lập tenant

| Tính chất | File test: hàm |
|---|---|
| Tenant dùng schema riêng không rò sang nhau; tên schema độc hại bị từ chối | `pkg/tenancy/router_postgres_test.go`: `TestRegisteredSchemaTenantRoutesToItsSchemaAndDoesNotLeak`, `TestMaliciousSchemaNameIsRejectedBeforeUse` |
| Tenant bị tạm ngưng hoặc đang migrate bị từ chối | `pkg/tenancy/router_postgres_test.go`: `TestSuspendedTenantIsRejected`, `TestMigratingTenantIsRejected` |
| Câu list loại dòng của tenant khác | `pkg/query/query_planner_postgres_test.go`: `TestPlanListTenantScopingActuallyExcludesOtherTenantsRows` |
| Audit không nhận bản ghi sai tenant; export không lẫn tenant khác | `pkg/audit/postgresstore_e2e_test.go`: `TestRecordRejectsAMismatchedTenantID`; `internal/httpapi/audit_export_postgres_test.go`: `TestAuditExportCSVAndJSON` |

## Secret, đầu ra, audit

| Tính chất | File test: hàm |
|---|---|
| Webhook cron chặn địa chỉ nội bộ và metadata cloud, kể cả IPv4-mapped IPv6 | `pkg/jobs/scheduler/ssrfguard_test.go`: `TestBlocksCloudMetadataAndPrivateRanges`, `TestIPv4MappedIPv6DoesNotBypassTheV4Rules`, `TestCheckRejectsALiteralMetadataAddressEndToEnd` |
| Tenant không tự đặt được header cấm; credential bị che trong phản hồi | `pkg/jobs/scheduler/ssrfguard_test.go`: `TestForbiddenHeadersAreRejectedRegardlessOfCase`; `pkg/jobs/scheduler/webhook_test.go`: `TestRedactRemovesAnEchoedCredentialFromAResponseBody` |
| Secret chỉ lưu tham chiếu; không khoá nào vừa secret vừa public; credential có xuống dòng bị từ chối | `pkg/config/store_e2e_test.go`: `TestSecretMarkerFlowStoresOnlyTheReferenceNeverThePlaintext`; `pkg/config/keys_test.go`: `TestNoKeyIsBothSecretAndPublic`, `TestACredentialWithANewlineIsRefusedAsHeaderInjection` |
| Giá trị theme dạng injection bị từ chối | `pkg/config/keys_test.go`: `TestThePublicThemeValidatorsRejectInjectionShapedValues` |
| Chuỗi audit phát hiện sửa đổi; field bị che mất cả giá trị cũ và mới | `pkg/audit/chain_e2e_test.go`: `TestChainVerifiesAndDetectsTampering`; `pkg/audit/entry_test.go`: `TestARedactedFieldKeepsTheFactItChangedButLosesBothValues` |
| Tên bảng attachment dạng injection bị từ chối | `pkg/attachments/attachments_test.go`: `TestValidateTableNameRejectsInjectionShapedInput` |

## Chưa có test

Chỉ ghi những gì đã thấy thiếu khi lập bảng, không phải kết quả rà soát đầy đủ.

- Qua HTTP: không thấy test gửi token hết hạn hoặc bị sửa chữ ký vào một route. Hiện chỉ test ở tầng
  verifier.
- Fuzzing.

## Công cụ quét

- CI: `govulncheck` (CVE của dependency), Semgrep (`p/golang`, `p/secrets`, `.semgrep.yml`, chặn build).
- CodeQL (`.github/workflows/codeql.yml`): kết quả ở tab Security, không chặn build. Sau khi merge,
  xem lại cảnh báo trên nhánh chính: check xanh trên PR không bảo đảm không có cảnh báo mới.
- ZAP: `zap/run.sh`, chạy tay ([README](../README.md)). Không hiểu phân quyền đa tenant, không thay
  các test trên.
