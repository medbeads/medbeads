# R12: 組織署名・記載者由来・knowledge release

## 1. 目的

単一病院では電子カルテで認証された記載者を病院システム鍵で証明し、将来は同じBeadを複数病院の
trust domainで検証できるようにする。SHA-256は内容同一性を証明するが作成組織を証明しないため、
署名とcontent addressを別々の責務として扱う。

## 2. 信頼境界

- `organization_id`: セキュリティ上の病院識別子。名称変更や同名病院の影響を受けない。
- `organization_name`: 署名時点の表示用スナップショット。認可判断には使わない。
- `actor_id`: 電子カルテで認証された記載者・承認者の組織内ID。
- `source_system_id`: 記載者を認証しMedBeadsへ渡した電子カルテ／ガバナンスシステム。
- `key_id`: 病院または連携システムの公開鍵識別子。
- `tenant_id`: クラウド上の保存・運用境界。病院IDとは別で、将来1 tenantが複数組織を信頼できる。

初期構成では医療従事者ごとに秘密鍵を配らない。電子カルテが認証した`actor_id`を含むstatementを
病院システム鍵で署名する。秘密鍵はPod/SQLiteへ保存しない。CLIのローカル鍵ファイルは単一病院の
bootstrap用であり、本番ではKMS/HSMへ置換する。

## 3. signature_attestation Bead

既存の`attestation`は診療記録訂正の承認状態を決めるため、暗号署名には使わない。
`signature_attestation`は次のstatementをRFC 8785 JCSでcanonicalizeし、Ed25519で署名する。

```json
{
  "schema": "medbeads.signing_statement.v1",
  "purpose": "clinical_origin | fhir_import | knowledge_release",
  "subject_bead_id": "<64-hex>",
  "organization_id": "org:hospital-a",
  "organization_name": "A病院",
  "source_system_id": "ehr:hospital-a",
  "actor": {
    "actor_id": "ehr:user:123",
    "display_name": "記載時点の表示名",
    "role": "physician"
  },
  "key_id": "org:hospital-a#system-signing-1",
  "algorithm": "Ed25519",
  "signed_at": "2026-07-14T10:00:00+09:00"
}
```

署名Beadの`parents`は必ず`subject_bead_id`を含む。署名を追加してもsubjectのBead IDは変わらず、
複数人・複数組織が同じsubjectへ独立に署名できる。

purposeの意味を混同しない。

- `clinical_origin`: 電子カルテとの契約・Provenance検証に基づき、病院が診療記録の由来を保証する。
- `fhir_import`: connectorがFHIR resourceを取得・変換したことを保証する。医師本人の署名とは主張しない。
- `knowledge_release`: link_rule集合の公開承認。

旧`bead.Signature`はPod meta互換のため残すが、hash対象外かつ検証文脈がないため信頼判断に使わない。

## 4. 公開Trust Policy

`medbeads.trust_policy.v1`はtenant、組織、公開鍵、鍵ごとの`trusted_for`、有効期間、失効日時、
knowledge releaseの必要承認人数を持つ。attestation自身が提示する公開鍵をそのまま信じてはならず、
必ずoperator管理のpolicyに登録済みの鍵で検証する。

複数病院構成では、病院Bの鍵を`clinical_origin`だけ信頼し、病院Aのlink_rule公開権限は与えない、
という分離が可能である。`allow_cross_organization_approvals`は既定falseとする。

## 5. knowledge_release

`knowledge_release` Beadは閉じた`rule_bead_ids`集合、組織、版表示、公開日時、適用開始・終了を持つ。
その`parents`はrule集合と完全一致しなければならない。release自身に対する
`purpose=knowledge_release`署名がpolicy所定数そろった場合だけ、clinical_linksの新目標世代へ
切り替えられる。

manifestへrelease未記載のlink_ruleを混ぜた場合は全体を拒否する。鍵失効、署名改ざん、未信頼鍵、
将来日付、適用期間外もfail closedとする。

## 6. 日時用語

- `published_at`: 公開日時
- `effective_from`: 適用開始日時
- `effective_to`: 適用終了日時／有効期限
- `signed_at`: 署名日時
- `performed_at`: 手術・処置など医療行為の実施日時

ルールについて日本語の「施行日」は使わない。手術施行日との混同を避ける。

## 7. CLI

```bash
# 単一病院bootstrap（秘密鍵は本番ではKMS/HSMへ移す）
medbeadsd trust init -data DATA \
  -organization-id org:hospital-a -organization-name A病院

# FHIR取り込み由来の署名
medbeadsd trust attest -data DATA -subject BEAD_ID \
  -purpose fhir_import -actor-id ehr:user:123 \
  -source-system-id ehr:hospital-a

# link_rule集合をreleaseして署名
medbeadsd trust release -data DATA -release-id rules-2026-07 \
  -rule-ids RULE_ID -actor-id ehr:committee:1 \
  -source-system-id governance:hospital-a

# 出力された閉じたknowledge ID集合を段階的に有効化
medbeadsd reproject -data DATA -trust-policy DATA/trust/policy.json \
  -knowledge-ids RULE_ID,RELEASE_ID,ATTESTATION_ID

# 以後、serveはactive manifestの署名を起動時に再検証
medbeadsd serve -data DATA -trust-policy DATA/trust/policy.json
```

## 8. 残る運用課題

- ローカル秘密鍵ファイルをKMS/HSM signer interfaceへ置換する。
- trust policy変更自体の監査・複数承認を追加する。
- 稼働中にreleaseが期限切れ／鍵失効した場合の自動停止・rollback policyを追加する。
- 病院間患者同一性と患者同意は署名とは別レイヤーで実装する。
